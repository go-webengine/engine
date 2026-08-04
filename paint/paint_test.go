// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package paint

import (
	"image"
	"image/color"
	"testing"

	"github.com/go-webengine/engine/css"
	"github.com/go-webengine/engine/dom"
	"github.com/go-webengine/engine/layout"
)

func white(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = 255
	}
	return img
}

func TestBlendPixel(t *testing.T) {
	img := white(2, 1)
	blendPixel(img, 0, 0, css.Color{R: 0, G: 0, B: 0, A: 255}, 255) // full black
	if r, g, b, a := img.At(0, 0).RGBA(); r != 0 || g != 0 || b != 0 || a>>8 != 255 {
		t.Errorf("full cover = %v %v %v %v", r, g, b, a)
	}
	blendPixel(img, 1, 0, css.Color{R: 0, G: 0, B: 0, A: 255}, 128) // half black over white
	c := img.RGBAAt(1, 0)
	if c.R < 120 || c.R > 135 {
		t.Errorf("half cover R = %d, want ~128", c.R)
	}
}

func TestToPainter(t *testing.T) {
	if got := toPainter(css.Color{R: 1, G: 2, B: 3, A: 4}); got.R != 1 || got.G != 2 || got.B != 3 || got.A != 4 {
		t.Errorf("toPainter = %+v", got)
	}
}

func TestBlitImage(t *testing.T) {
	dst := white(10, 10)
	src := image.NewRGBA(image.Rect(0, 0, 3, 3))
	for i := range src.Pix {
		src.Pix[i] = 255
	}
	// paint src red
	for y := 0; y < 3; y++ {
		for x := 0; x < 3; x++ {
			src.SetRGBA(x, y, color.RGBA{200, 0, 0, 255})
		}
	}
	blitImage(dst, src, 2, 2)
	if c := dst.RGBAAt(3, 3); c.R != 200 || c.G != 0 {
		t.Errorf("blit pixel = %+v", c)
	}
	// pixel outside the blit remains white.
	if c := dst.RGBAAt(0, 0); c.R != 255 {
		t.Errorf("outside pixel changed: %+v", c)
	}
}

func TestBlitImageClipsOutOfBounds(t *testing.T) {
	dst := white(4, 4)
	src := image.NewRGBA(image.Rect(0, 0, 3, 3))
	for i := range src.Pix {
		src.Pix[i] = 255
	}
	// Draw partly off the top-left and bottom-right edges; must not panic.
	blitImage(dst, src, -1, -1)
	blitImage(dst, src, 3, 3)
}

func TestFontsMeasureAndMetrics(t *testing.T) {
	f := NewFonts()
	w := f.Measure("Hello", css.Sans, 16, 400)
	if w <= 0 {
		t.Fatalf("measure = %v", w)
	}
	// Bold is at least as wide as regular (faux-bold adds per-rune width).
	if wb := f.Measure("Hello", css.Sans, 16, 700); wb < w {
		t.Errorf("bold %v < regular %v", wb, w)
	}
	// Cache hit on the second call for the same face.
	_ = f.Measure("world", css.Serif, 16, 400)
	_ = f.Measure("world", css.Serif, 16, 400)

	asc, lh := f.Metrics(css.Mono, 16, 400)
	if asc <= 0 || lh <= 0 {
		t.Errorf("metrics = %v %v", asc, lh)
	}
	// Tiny size triggers the 1.15*size minimum line-height floor.
	_, lhTiny := f.Metrics(css.Sans, 1, 400)
	if lhTiny < 1 {
		t.Errorf("tiny line height = %v", lhTiny)
	}
	// Unknown family falls back to Sans (and size<1 clamps to 1).
	if f.Measure("x", css.FontFamily(99), 0.2, 400) <= 0 {
		t.Error("unknown family/size should still measure")
	}
}

func TestPaintEndToEnd(t *testing.T) {
	f := NewFonts()
	dst := white(60, 40)

	imgNode := &dom.Node{Type: dom.Element, Tag: "img"}
	src := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			src.SetRGBA(x, y, color.RGBA{0, 180, 0, 255})
		}
	}
	textStyle := &css.Style{Color: css.Color{R: 0, G: 0, B: 0, A: 255}, FontSize: 20, FontWeight: 700, FontFamily: css.Sans}

	box := &layout.Box{
		Style: &css.Style{Background: css.Color{R: 220, G: 220, B: 220, A: 255}},
		X:     0, Y: 0, W: 60, H: 40,
		Lines: []*layout.LineBox{{Items: []*layout.InlineItem{
			{Text: "H", Style: textStyle, X: 2, Y: 2, Ascent: 16, Width: 12},
			{Image: imgNode, X: 40, Y: 2, ImgW: 4, ImgH: 4},
		}}},
	}
	imgs := map[*dom.Node]image.Image{imgNode: src}
	Paint(dst, box, f, imgs)

	// Background fill applied.
	if c := dst.RGBAAt(1, 30); c.R != 220 || c.G != 220 || c.B != 220 {
		t.Errorf("background pixel = %+v", c)
	}
	// The image blitted green near (41,3).
	if c := dst.RGBAAt(41, 3); c.G < 150 || c.R > 60 {
		t.Errorf("image pixel = %+v", c)
	}
	// The glyph 'H' left ink darker than the grey background somewhere in its box.
	if !hasDarkInk(dst, image.Rect(2, 2, 16, 22)) {
		t.Error("expected glyph ink in the H region")
	}
}

func TestPaintNilAndEmpty(t *testing.T) {
	f := NewFonts()
	dst := white(4, 4)
	Paint(dst, nil, f, nil) // nil box: no panic
	// Box with a text item lacking style and an empty-text item: both skipped.
	box := &layout.Box{Lines: []*layout.LineBox{{Items: []*layout.InlineItem{
		{Text: "x"},                     // no Style → skipped
		{Text: "", Style: &css.Style{}}, // empty text → skipped
	}}}}
	Paint(dst, box, f, nil)
	// Image item with no bitmap in the map is skipped.
	box2 := &layout.Box{Lines: []*layout.LineBox{{Items: []*layout.InlineItem{
		{Image: &dom.Node{}, X: 0, Y: 0},
	}}}}
	Paint(dst, box2, f, map[*dom.Node]image.Image{})
}

func hasDarkInk(img *image.RGBA, r image.Rectangle) bool {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if img.RGBAAt(x, y).R < 200 {
				return true
			}
		}
	}
	return false
}
