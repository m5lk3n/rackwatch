package main

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"time"
	"unsafe"

	ui "github.com/metaspartan/gotui"
	"golang.org/x/sys/unix"
)

// On the Linux text console, taps only reach the terminal through gpm, which
// treats a touchscreen like a mouse and drifts away from the finger. So
// rackwatch reads the touchscreen's evdev device itself and turns each touch
// into the mouse events a terminal would send.

// Linux input event types and codes, from <linux/input-event-codes.h>.
const (
	evSyn     = 0x00
	evKey     = 0x01
	evAbs     = 0x03
	synReport = 0
	btnTouch  = 0x14a
	absX      = 0x00
	absY      = 0x01
)

// touchRetry is how long to wait before opening the device again, after it
// was missing or unplugged.
const touchRetry = 5 * time.Second

var touchLog = journalLog("touch: ")

// axisRange is what the device reports as an axis's lowest and highest value.
type axisRange struct{ min, max int32 }

// touchMapper turns device coordinates into screen cells.
type touchMapper struct {
	cfg    touchConfig
	xRange axisRange
	yRange axisRange
}

// cell maps the device position x, y onto a w by h screen.
func (m touchMapper) cell(x, y int32, w, h int) (int, int) {
	fx, fy := m.xRange.fraction(x), m.yRange.fraction(y)
	if m.cfg.SwapXY {
		fx, fy = fy, fx
	}
	if m.cfg.InvertX {
		fx = 1 - fx
	}
	if m.cfg.InvertY {
		fy = 1 - fy
	}
	return min(int(fx*float64(w)), w-1), min(int(fy*float64(h)), h-1)
}

// fraction is how far v lies along the axis, from 0 to 1.
func (r axisRange) fraction(v int32) float64 {
	return min(1, max(0, float64(v-r.min)/float64(r.max-r.min)))
}

// touchDecoder follows a touchscreen's events. The device sends a frame of
// events closed by SYN_REPORT; the touch and its position arrive in the same
// frame, in no fixed order, so presses and releases go out with the frame.
type touchDecoder struct {
	x, y              int32
	pressed, released bool
}

// event takes one input event and returns what the frame it closes amounts to.
func (d *touchDecoder) event(typ, code uint16, value int32) (press, release bool) {
	switch typ {
	case evAbs:
		switch code {
		case absX:
			d.x = value
		case absY:
			d.y = value
		}
	case evKey:
		if code == btnTouch {
			if value == 1 {
				d.pressed = true
			} else if value == 0 {
				d.released = true
			}
		}
	case evSyn:
		if code == synReport {
			press, release = d.pressed, d.released
			d.pressed, d.released = false, false
		}
	}
	return press, release
}

// readTouchscreen sends a press and a release on out for each touch on the
// configured device, as mouse events at the touched cell. It runs for good,
// opening the device again whenever it goes away.
func readTouchscreen(cfg touchConfig, out chan<- ui.Event) {
	var lastErr string
	for {
		err := readTouches(cfg, out)
		// Log each new problem once, not every touchRetry.
		if err.Error() != lastErr {
			touchLog.Print(err)
			lastErr = err.Error()
		}
		time.Sleep(touchRetry)
	}
}

// readTouches reads the device until it fails.
func readTouches(cfg touchConfig, out chan<- ui.Event) error {
	f, err := os.Open(cfg.Device)
	if err != nil {
		return err
	}
	defer f.Close()
	m := touchMapper{cfg: cfg}
	if m.xRange, err = absRange(f, absX); err != nil {
		return err
	}
	if m.yRange, err = absRange(f, absY); err != nil {
		return err
	}
	touchLog.Printf("reading %s, x %d..%d, y %d..%d", cfg.Device, m.xRange.min, m.xRange.max, m.yRange.min, m.yRange.max)

	// struct input_event: a struct timeval, then type, code and value.
	tv := int(unsafe.Sizeof(unix.Timeval{}))
	buf := make([]byte, tv+8)
	r := bufio.NewReader(f)
	var d touchDecoder
	for {
		if _, err := io.ReadFull(r, buf); err != nil {
			return fmt.Errorf("%s: %w", cfg.Device, err)
		}
		typ := binary.NativeEndian.Uint16(buf[tv:])
		code := binary.NativeEndian.Uint16(buf[tv+2:])
		value := int32(binary.NativeEndian.Uint32(buf[tv+4:]))
		press, release := d.event(typ, code, value)
		if !press && !release {
			continue
		}
		w, h := ui.TerminalDimensions()
		x, y := m.cell(d.x, d.y, w, h)
		at := ui.Mouse{X: x, Y: y}
		if press {
			out <- ui.Event{Type: ui.MouseEvent, ID: "<MouseLeft>", Payload: at}
		}
		if release {
			out <- ui.Event{Type: ui.MouseEvent, ID: "Unknown_Mouse_Button", Payload: at}
		}
	}
}

// absRange asks the device for an axis's range (EVIOCGABS).
func absRange(f *os.File, axis uint) (axisRange, error) {
	var info struct{ value, min, max, fuzz, flat, resolution int32 }
	req := 2<<30 | uintptr(unsafe.Sizeof(info))<<16 | 'E'<<8 | uintptr(0x40+axis)
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, f.Fd(), req, uintptr(unsafe.Pointer(&info))); errno != 0 {
		return axisRange{}, fmt.Errorf("%s: reading the range of axis %d: %w (is it a touchscreen?)", f.Name(), axis, errno)
	}
	if info.max <= info.min {
		return axisRange{}, fmt.Errorf("%s: axis %d has no range", f.Name(), axis)
	}
	return axisRange{info.min, info.max}, nil
}
