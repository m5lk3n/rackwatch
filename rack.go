package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	_ "image/png" // decoder for rack pictures that are PNGs
	"os"
)

// minCutout is the smallest yellow area, in image pixels, taken for a cutout.
const minCutout = 20

// rackImage is a picture of a rack with the area where its screen is, for the
// web dashboard to draw itself into.
type rackImage struct {
	jpeg   []byte // the picture, with the cutout filled in, as JPEG
	etag   string
	size   image.Point     // the picture's size in pixels
	cutout image.Rectangle // where the screen is
}

// webRack is what the page needs to know about the rack picture.
type webRack struct {
	Width  int `json:"width"`
	Height int `json:"height"`
	X      int `json:"x"` // the cutout
	Y      int `json:"y"`
	W      int `json:"w"`
	H      int `json:"h"`
}

func (r *rackImage) web() *webRack {
	if r == nil {
		return nil
	}
	c := r.cutout
	return &webRack{r.size.X, r.size.Y, c.Min.X, c.Min.Y, c.Dx(), c.Dy()}
}

// loadRackImage reads a PNG or JPEG of a rack whose screen is painted yellow
// (#ffff00), and fills the yellow with bg so none of it shows around the
// dashboard drawn on top.
func loadRackImage(path string, bg color.RGBA) (*rackImage, error) {
	f, err := os.Open(path) // #nosec G304 -- the path comes from the config file
	if err != nil {
		return nil, fmt.Errorf("web.custom: %w", err)
	}
	defer func() { _ = f.Close() }()
	src, _, err := image.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("web.custom: %s: %w", path, err)
	}
	img := image.NewRGBA(src.Bounds())
	draw.Draw(img, img.Bounds(), src, src.Bounds().Min, draw.Src)

	cut, err := findCutout(img)
	if err != nil {
		return nil, fmt.Errorf("web.custom: %s: %w", path, err)
	}
	for y := cut.Min.Y; y < cut.Max.Y; y++ {
		for x := cut.Min.X; x < cut.Max.X; x++ {
			img.SetRGBA(x, y, bg)
		}
	}
	// Fill what's left of the yellow just outside the box too, like the
	// blurred edge of a JPEG.
	edge := cut.Inset(-2).Intersect(img.Bounds())
	for y := edge.Min.Y; y < edge.Max.Y; y++ {
		for x := edge.Min.X; x < edge.Max.X; x++ {
			if yellowish(img.RGBAAt(x, y)) {
				img.SetRGBA(x, y, bg)
			}
		}
	}

	// JPEG, since it's a photo: a fraction of the size of a PNG, and on a
	// Raspberry Pi a much shorter wait at startup.
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		return nil, fmt.Errorf("web.custom: %w", err)
	}
	sum := sha256.Sum256(buf.Bytes())
	b := img.Bounds()
	return &rackImage{
		jpeg:   buf.Bytes(),
		etag:   `"` + hex.EncodeToString(sum[:8]) + `"`,
		size:   b.Size(),
		cutout: cut.Sub(b.Min),
	}, nil
}

// findCutout returns the bounding box of the yellow pixels, which must form a
// solid rectangle.
func findCutout(img *image.RGBA) (image.Rectangle, error) {
	var box image.Rectangle
	n := 0
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if !yellowish(img.RGBAAt(x, y)) {
				continue
			}
			n++
			box = box.Union(image.Rect(x, y, x+1, y+1))
		}
	}
	switch {
	case n == 0:
		return box, errors.New("no yellow (#ffff00) area marking the screen")
	case box.Dx() < minCutout || box.Dy() < minCutout:
		return box, fmt.Errorf("the yellow area is only %d × %d pixels", box.Dx(), box.Dy())
	case n < box.Dx()*box.Dy()*9/10:
		return box, fmt.Errorf("the yellow pixels between %v and %v don't form one solid rectangle", box.Min, box.Max)
	}
	return box, nil
}

// yellowish reports whether c is close to #ffff00, allowing for JPEG noise.
func yellowish(c color.RGBA) bool {
	return c.R >= 0xe0 && c.G >= 0xe0 && c.B <= 0x50
}
