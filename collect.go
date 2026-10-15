package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// rawSample holds counters and gauges as read from a source. Rates (CPU, network)
// need two samples, so they are derived later by derive.
type rawSample struct {
	at        time.Time
	cpuIdle   float64 // cumulative idle+iowait time, any unit
	cpuTotal  float64 // cumulative total time, same unit as cpuIdle
	memTotal  float64
	memAvail  float64
	diskSize  float64 // root filesystem, bytes
	diskFree  float64
	diskAvail float64
	tempC     float64 // hottest sensor; NaN if none
	netBytes  float64 // rx+tx across physical-ish interfaces; NaN if none
}

// metrics is what the dashboard shows. NaN means unknown.
type metrics struct {
	cpu  float64 // percent
	mem  float64 // percent
	disk float64 // percent
	temp float64 // celsius
	net  float64 // bytes per second
}

func unknownMetrics() metrics {
	nan := math.NaN()
	return metrics{nan, nan, nan, nan, nan}
}

func derive(prev *rawSample, cur rawSample) metrics {
	m := unknownMetrics()
	if cur.memTotal > 0 {
		m.mem = 100 * (cur.memTotal - cur.memAvail) / cur.memTotal
	}
	if used := cur.diskSize - cur.diskFree; used+cur.diskAvail > 0 {
		m.disk = 100 * used / (used + cur.diskAvail)
	}
	m.temp = cur.tempC
	if prev == nil {
		return m
	}
	// Negative deltas mean a counter reset (reboot, exporter restart): skip the rate.
	if dt, di := cur.cpuTotal-prev.cpuTotal, cur.cpuIdle-prev.cpuIdle; dt > 0 && di >= 0 {
		m.cpu = clamp(100*(dt-di)/dt, 0, 100)
	}
	if secs, db := cur.at.Sub(prev.at).Seconds(), cur.netBytes-prev.netBytes; secs > 0 && db >= 0 {
		m.net = db / secs
	}
	return m
}

func clamp(v, lo, hi float64) float64 {
	return math.Max(lo, math.Min(hi, v))
}

// skipInterface filters loopback and virtual interfaces so container traffic
// is not counted twice.
func skipInterface(dev string) bool {
	if dev == "lo" {
		return true
	}
	for _, p := range []string{"veth", "docker", "br-", "virbr", "cni", "flannel"} {
		if strings.HasPrefix(dev, p) {
			return true
		}
	}
	return false
}

type collector func(ctx context.Context) (rawSample, error)

func newCollector(source string) collector {
	if source == sourceLocal {
		return collectLocal
	}
	client := &http.Client{}
	return func(ctx context.Context) (rawSample, error) {
		return collectNodeExporter(ctx, client, source)
	}
}

// --- local ---------------------------------------------------------------

func collectLocal(context.Context) (rawSample, error) {
	s := rawSample{at: time.Now(), tempC: math.NaN(), netBytes: math.NaN()}
	if err := readProcStat(&s); err != nil {
		return s, err
	}
	if err := readMeminfo(&s); err != nil {
		return s, err
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs("/", &st); err == nil {
		bs := float64(st.Bsize)
		s.diskSize, s.diskFree, s.diskAvail = float64(st.Blocks)*bs, float64(st.Bfree)*bs, float64(st.Bavail)*bs
	}
	s.tempC = readLocalTemp()
	s.netBytes = readNetDev()
	return s, nil
}

func readProcStat(s *rawSample) error {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		return errors.New("/proc/stat: empty")
	}
	// cpu user nice system idle iowait irq softirq steal guest guest_nice
	fields := strings.Fields(sc.Text())
	if len(fields) < 9 || fields[0] != "cpu" {
		return errors.New("/proc/stat: unexpected format")
	}
	for i, f := range fields[1:9] { // guest time is already included in user
		v, err := strconv.ParseFloat(f, 64)
		if err != nil {
			return fmt.Errorf("/proc/stat: %w", err)
		}
		s.cpuTotal += v
		if i == 3 || i == 4 {
			s.cpuIdle += v
		}
	}
	return nil
}

func readMeminfo(s *rawSample) error {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		v, _ := strconv.ParseFloat(fields[1], 64)
		switch fields[0] {
		case "MemTotal:":
			s.memTotal = v * 1024
		case "MemAvailable:":
			s.memAvail = v * 1024
		}
	}
	return sc.Err()
}

func readLocalTemp() float64 {
	paths, _ := filepath.Glob("/sys/class/hwmon/hwmon*/temp*_input")
	if len(paths) == 0 {
		paths, _ = filepath.Glob("/sys/class/thermal/thermal_zone*/temp")
	}
	hottest := math.NaN()
	for _, p := range paths {
		b, err := os.ReadFile(p) // #nosec G304 -- p comes from a fixed sysfs glob
		if err != nil {
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(string(b)), 64)
		if err != nil {
			continue
		}
		if c := v / 1000; math.IsNaN(hottest) || c > hottest {
			hottest = c
		}
	}
	return hottest
}

func readNetDev() float64 {
	f, err := os.Open("/proc/net/dev")
	if err != nil {
		return math.NaN()
	}
	defer func() { _ = f.Close() }()
	total, found := 0.0, false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		dev, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok || skipInterface(strings.TrimSpace(dev)) {
			continue
		}
		// rx: bytes packets errs drop fifo frame compressed multicast | tx: bytes ...
		fields := strings.Fields(rest)
		if len(fields) < 9 {
			continue
		}
		rx, _ := strconv.ParseFloat(fields[0], 64)
		tx, _ := strconv.ParseFloat(fields[8], 64)
		total += rx + tx
		found = true
	}
	if !found {
		return math.NaN()
	}
	return total
}

// --- node_exporter -------------------------------------------------------

const maxMetricsBody = 32 << 20

func collectNodeExporter(ctx context.Context, client *http.Client, url string) (rawSample, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return rawSample{}, err
	}
	req.Header.Set("Accept", "text/plain")
	resp, err := client.Do(req)
	if err != nil {
		return rawSample{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return rawSample{}, fmt.Errorf("HTTP %s", resp.Status)
	}
	s, err := parseNodeExporter(io.LimitReader(resp.Body, maxMetricsBody))
	s.at = time.Now()
	return s, err
}

func parseNodeExporter(r io.Reader) (rawSample, error) {
	s := rawSample{}
	hwmon, thermal := math.NaN(), math.NaN()
	net, sawCPU, sawMem := 0.0, false, false
	netFound := false

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		name, labels, value, err := parsePromLine(line)
		if err != nil {
			continue
		}
		switch name {
		case "node_cpu_seconds_total":
			sawCPU = true
			s.cpuTotal += value
			if m := labels["mode"]; m == "idle" || m == "iowait" {
				s.cpuIdle += value
			}
		case "node_memory_MemTotal_bytes":
			sawMem = true
			s.memTotal = value
		case "node_memory_MemAvailable_bytes":
			s.memAvail = value
		case "node_filesystem_size_bytes", "node_filesystem_free_bytes", "node_filesystem_avail_bytes":
			if labels["mountpoint"] != "/" {
				continue
			}
			switch name {
			case "node_filesystem_size_bytes":
				s.diskSize = value
			case "node_filesystem_free_bytes":
				s.diskFree = value
			default:
				s.diskAvail = value
			}
		case "node_hwmon_temp_celsius":
			hwmon = maxNaN(hwmon, value)
		case "node_thermal_zone_temp":
			thermal = maxNaN(thermal, value)
		case "node_network_receive_bytes_total", "node_network_transmit_bytes_total":
			if !skipInterface(labels["device"]) {
				net += value
				netFound = true
			}
		}
	}
	if err := sc.Err(); err != nil {
		return s, err
	}
	if !sawCPU && !sawMem {
		return s, errors.New("no node_exporter metrics in response")
	}
	s.tempC = hwmon
	if math.IsNaN(s.tempC) {
		s.tempC = thermal
	}
	s.netBytes = math.NaN()
	if netFound {
		s.netBytes = net
	}
	return s, nil
}

func maxNaN(a, b float64) float64 {
	if math.IsNaN(a) || b > a {
		return b
	}
	return a
}

// parsePromLine parses one sample of the Prometheus text exposition format:
//
//	name{label="value",...} 1.5e+06 [timestamp]
func parsePromLine(line string) (name string, labels map[string]string, value float64, err error) {
	i := strings.IndexAny(line, "{ \t")
	if i <= 0 {
		return "", nil, 0, errors.New("malformed sample")
	}
	name, rest := line[:i], line[i:]
	if rest[0] == '{' {
		labels, rest, err = parsePromLabels(rest[1:])
		if err != nil {
			return "", nil, 0, err
		}
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return "", nil, 0, errors.New("missing value")
	}
	value, err = strconv.ParseFloat(fields[0], 64)
	return name, labels, value, err
}

// parsePromLabels parses `k="v",k2="v2"}` and returns the remainder after '}'.
func parsePromLabels(s string) (map[string]string, string, error) {
	labels := map[string]string{}
	for {
		s = strings.TrimLeft(s, " ,")
		if s == "" {
			return nil, "", errors.New("unterminated labels")
		}
		if s[0] == '}' {
			return labels, s[1:], nil
		}
		eq := strings.IndexByte(s, '=')
		if eq < 0 || eq+1 >= len(s) || s[eq+1] != '"' {
			return nil, "", errors.New("malformed label")
		}
		key := strings.TrimSpace(s[:eq])
		s = s[eq+2:]
		var val strings.Builder
		closed := false
		for j := 0; j < len(s); j++ {
			c := s[j]
			if c == '\\' && j+1 < len(s) {
				j++
				switch s[j] {
				case 'n':
					val.WriteByte('\n')
				default:
					val.WriteByte(s[j])
				}
				continue
			}
			if c == '"' {
				s = s[j+1:]
				closed = true
				break
			}
			val.WriteByte(c)
		}
		if !closed {
			return nil, "", errors.New("unterminated label value")
		}
		labels[key] = val.String()
	}
}
