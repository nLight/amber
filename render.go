package main

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"
	"strconv"
	"strings"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/gofont/gomonobold"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

const (
	screenW = 600
	screenH = 800
	margin  = 10
	headerH = 72 // rows start here
	footerH = 28 // and end this far above the bottom edge
	rowGap  = 8
)

// E Ink panels show 16 gray levels; every tone used here is one of them, so the
// controller never has to dither.
const (
	black uint8 = 0x00
	ink1  uint8 = 0x33
	ink2  uint8 = 0x55
	ink3  uint8 = 0x88
	ink4  uint8 = 0xBB
	ink5  uint8 = 0xDD
	paper uint8 = 0xFF
)

type faces struct {
	tiny, small, smallB, mid, title, big font.Face
}

func loadFaces() *faces {
	reg, _ := opentype.Parse(gomono.TTF)
	bold, _ := opentype.Parse(gomonobold.TTF)
	face := func(f *opentype.Font, size float64) font.Face {
		fc, err := opentype.NewFace(f, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
		if err != nil {
			panic(err)
		}
		return fc
	}
	return &faces{
		tiny:   face(reg, 12),
		small:  face(reg, 14),
		smallB: face(bold, 14),
		mid:    face(bold, 20),
		title:  face(bold, 28),
		big:    face(bold, 46),
	}
}

// ---------------------------------------------------------------- canvas

type canvas struct {
	*image.Gray
	f *faces
}

func (c *canvas) fill(r image.Rectangle, v uint8) {
	draw.Draw(c.Gray, r, image.NewUniform(color.Gray{v}), image.Point{}, draw.Src)
}

func (c *canvas) set(x, y int, v uint8) {
	if (image.Point{x, y}).In(c.Rect) {
		c.Pix[c.PixOffset(x, y)] = v
	}
}

func (c *canvas) hline(x0, x1, y int, v uint8) { c.fill(image.Rect(x0, y, x1, y+1), v) }
func (c *canvas) vline(x, y0, y1 int, v uint8) { c.fill(image.Rect(x, y0, x+1, y1), v) }

func (c *canvas) dotted(x0, x1, y int, v uint8, step int) {
	for x := x0; x < x1; x += step {
		c.set(x, y, v)
	}
}

func (c *canvas) vdotted(x, y0, y1 int, v uint8, step int) {
	for y := y0; y < y1; y += step {
		c.set(x, y, v)
	}
}

func (c *canvas) stroke(r image.Rectangle, v uint8, w int) {
	c.fill(image.Rect(r.Min.X, r.Min.Y, r.Max.X, r.Min.Y+w), v)
	c.fill(image.Rect(r.Min.X, r.Max.Y-w, r.Max.X, r.Max.Y), v)
	c.fill(image.Rect(r.Min.X, r.Min.Y, r.Min.X+w, r.Max.Y), v)
	c.fill(image.Rect(r.Max.X-w, r.Min.Y, r.Max.X, r.Max.Y), v)
}

// hatch fills r with 45° lines — the "previous period" texture.
func (c *canvas) hatch(r image.Rectangle, v uint8, step int) {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if (x+y)%step == 0 {
				c.set(x, y, v)
			}
		}
	}
}

// checker fills r with a 50% dot pattern.
func (c *canvas) checker(r image.Rectangle, v uint8) {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if (x+y)%2 == 0 {
				c.set(x, y, v)
			}
		}
	}
}

func (c *canvas) line(x0, y0, x1, y1 float64, v uint8, thick int) {
	n := int(math.Max(math.Abs(x1-x0), math.Abs(y1-y0))) + 1
	for i := 0; i <= n; i++ {
		t := float64(i) / float64(n)
		x := int(math.Round(x0 + (x1-x0)*t))
		y := int(math.Round(y0 + (y1-y0)*t))
		c.fill(image.Rect(x-thick/2, y-thick/2, x-thick/2+thick, y-thick/2+thick), v)
	}
}

// triangle draws a small filled arrow centered at (cx, cy).
func (c *canvas) triangle(cx, cy, size int, up bool, v uint8) {
	for row := 0; row < size; row++ {
		half := row * size / (2 * max(1, size-1))
		y := cy - size/2 + row
		if !up {
			y = cy + size/2 - row
		}
		c.hline(cx-half, cx+half+1, y, v)
	}
}

func textWidth(f font.Face, s string) int { return font.MeasureString(f, s).Ceil() }

func (c *canvas) text(f font.Face, x, y int, s string, v uint8) int {
	d := font.Drawer{Dst: c.Gray, Src: image.NewUniform(color.Gray{v}), Face: f, Dot: fixed.P(x, y)}
	d.DrawString(s)
	return d.Dot.X.Ceil()
}

func (c *canvas) textRight(f font.Face, x, y int, s string, v uint8) int {
	w := textWidth(f, s)
	c.text(f, x-w, y, s, v)
	return x - w
}

// quantize snaps anti-aliased text edges to the panel's 16 levels.
func (c *canvas) quantize() {
	for i, p := range c.Pix {
		c.Pix[i] = uint8((int(p) + 8) / 17 * 17)
	}
}

// ---------------------------------------------------------------- formatting

func fmtCount(v float64) string {
	switch {
	case v >= 100000:
		return fmt.Sprintf("%.0fk", v/1000)
	case v >= 10000:
		return fmt.Sprintf("%.1fk", v/1000)
	}
	return strconv.FormatFloat(v, 'f', 0, 64)
}

func maxOf(s Series) float64 {
	m := 0.0
	for _, v := range s {
		m = math.Max(m, v)
	}
	return m
}

func upper(s string) string { return strings.ToUpper(s) }
