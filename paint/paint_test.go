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
	blitImage(dst, src, 2, 2, dst.Rect)
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
	blitImage(dst, src, -1, -1, dst.Rect)
	blitImage(dst, src, 3, 3, dst.Rect)
}

func TestFontsMeasureAndMetrics(t *testing.T) {
	f := NewFonts()
	w := f.Measure("Hello", css.Sans, 16, 400, false)
	if w <= 0 {
		t.Fatalf("measure = %v", w)
	}
	// The real Inter Bold face measures at least as wide as Regular.
	if wb := f.Measure("Hello", css.Sans, 16, 700, false); wb < w {
		t.Errorf("bold %v < regular %v", wb, w)
	}
	// A real italic face measures (and renders) distinctly from regular.
	if wi := f.Measure("Hello", css.Sans, 16, 400, true); wi <= 0 {
		t.Errorf("italic measure = %v", wi)
	}
	// Cache hit on the second call for the same face.
	_ = f.Measure("world", css.Serif, 16, 400, false)
	_ = f.Measure("world", css.Serif, 16, 400, false)

	asc, lh := f.Metrics(css.Mono, 16, 400, false)
	if asc <= 0 || lh <= 0 {
		t.Errorf("metrics = %v %v", asc, lh)
	}
	// Mono bold/italic fall back to the single Go Mono face (no panic, measures).
	if f.Measure("x", css.Mono, 16, 700, true) <= 0 {
		t.Error("mono bold-italic fallback should still measure")
	}
	// Tiny size triggers the 1.15*size minimum line-height floor.
	_, lhTiny := f.Metrics(css.Sans, 1, 400, false)
	if lhTiny < 1 {
		t.Errorf("tiny line height = %v", lhTiny)
	}
	// Unknown family falls back to Sans (and size<1 clamps to 1).
	if f.Measure("x", css.FontFamily(99), 0.2, 400, false) <= 0 {
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

func TestPaintBorders(t *testing.T) {
	f := NewFonts()
	dst := white(20, 20)
	box := &layout.Box{
		Node: &dom.Node{Type: dom.Element, Tag: "div"},
		Style: &css.Style{Border: css.Borders{
			Top:    css.BorderSide{Width: 2, Style: css.BorderSolid, Color: css.Color{R: 255, A: 255}},
			Right:  css.BorderSide{Width: 2, Style: css.BorderSolid, Color: css.Color{G: 255, A: 255}},
			Bottom: css.BorderSide{Width: 2, Style: css.BorderSolid, Color: css.Color{B: 255, A: 255}},
			Left:   css.BorderSide{Width: 2, Style: css.BorderSolid, Color: css.Color{R: 255, G: 255, A: 255}},
		}},
		X: 2, Y: 2, W: 16, H: 16,
	}
	Paint(dst, box, f, nil)
	// Top edge red.
	if c := dst.RGBAAt(9, 2); c.R != 255 || c.B != 0 {
		t.Errorf("top border pixel = %+v want red", c)
	}
	// Left edge yellow.
	if c := dst.RGBAAt(2, 9); c.R != 255 || c.G != 255 || c.B != 0 {
		t.Errorf("left border pixel = %+v want yellow", c)
	}
	// Right edge green (x = 2+16-1 = 17).
	if c := dst.RGBAAt(17, 9); c.G != 255 || c.R != 0 {
		t.Errorf("right border pixel = %+v want green", c)
	}
	// Bottom edge blue (y = 2+16-1 = 17).
	if c := dst.RGBAAt(9, 17); c.B != 255 || c.R != 0 {
		t.Errorf("bottom border pixel = %+v want blue", c)
	}
	// Interior stays white.
	if c := dst.RGBAAt(9, 9); c.R != 255 || c.G != 255 || c.B != 255 {
		t.Errorf("interior pixel = %+v want white", c)
	}
}

// TestPaintRoundedBackground asserts exact corner geometry: a rounded-rect
// background leaves the extreme corner pixel uncovered (still white) while the
// centre and mid-edge are fully filled.
func TestPaintRoundedBackground(t *testing.T) {
	f := NewFonts()
	dst := white(40, 40)
	box := &layout.Box{
		Node:  &dom.Node{Type: dom.Element, Tag: "div"},
		Style: &css.Style{Background: css.Color{R: 10, G: 20, B: 30, A: 255}, BorderRadius: css.Length{Px: 10}},
		X:     0, Y: 0, W: 40, H: 40,
	}
	Paint(dst, box, f, nil)
	// Extreme top-left corner (0,0) is outside the rounded shape → stays white.
	if c := dst.RGBAAt(0, 0); c.R != 255 || c.G != 255 || c.B != 255 {
		t.Errorf("corner (0,0) = %+v, want white (outside radius)", c)
	}
	// The other three extreme corners too.
	for _, p := range [][2]int{{39, 0}, {0, 39}, {39, 39}} {
		if c := dst.RGBAAt(p[0], p[1]); c.R != 255 {
			t.Errorf("corner %v = %+v, want white", p, c)
		}
	}
	// Centre is fully filled with the background colour.
	if c := dst.RGBAAt(20, 20); c.R != 10 || c.G != 20 || c.B != 30 {
		t.Errorf("centre = %+v, want {10 20 30}", c)
	}
	// Mid top edge (well inside the straight run) is filled.
	if c := dst.RGBAAt(20, 0); c.R != 10 {
		t.Errorf("mid top edge = %+v, want filled", c)
	}
}

// TestPaintRoundedBackgroundPercent resolves a 50% radius against the smaller
// side (a square → a disc): the corner is empty, the centre filled.
func TestPaintRoundedBackgroundPercent(t *testing.T) {
	f := NewFonts()
	dst := white(30, 30)
	box := &layout.Box{
		Node:  &dom.Node{Type: dom.Element, Tag: "div"},
		Style: &css.Style{Background: css.Color{R: 0, G: 0, B: 0, A: 255}, BorderRadius: css.Length{Percent: 0.5, IsPercent: true}},
		X:     0, Y: 0, W: 30, H: 30,
	}
	Paint(dst, box, f, nil)
	if c := dst.RGBAAt(1, 1); c.R != 255 {
		t.Errorf("disc corner = %+v, want white", c)
	}
	if c := dst.RGBAAt(15, 15); c.R != 0 || c.A>>0 == 0 {
		t.Errorf("disc centre = %+v, want black", c)
	}
}

// TestBoxRadius exercises the radius resolution helper directly, including the
// no-radius, auto, and zero-size fallbacks.
func TestBoxRadius(t *testing.T) {
	mk := func(l css.Length, w, h float64) *layout.Box {
		return &layout.Box{Style: &css.Style{BorderRadius: l}, W: w, H: h}
	}
	if got := boxRadius(mk(css.Length{Px: 8}, 40, 40)); got != 8 {
		t.Errorf("px radius = %d want 8", got)
	}
	if got := boxRadius(mk(css.Length{Percent: 0.25, IsPercent: true}, 40, 80)); got != 10 {
		t.Errorf("percent radius = %d want 10 (25%% of min side 40)", got)
	}
	if got := boxRadius(mk(css.Length{}, 40, 40)); got != 0 {
		t.Errorf("zero radius = %d want 0", got)
	}
	if got := boxRadius(mk(css.Length{Auto: true}, 40, 40)); got != 0 {
		t.Errorf("auto radius = %d want 0", got)
	}
	if got := boxRadius(&layout.Box{Style: nil, W: 10, H: 10}); got != 0 {
		t.Errorf("nil style radius = %d want 0", got)
	}
}

// TestPaintRoundedBorderUniform strokes a uniform bordered rounded box: the
// extreme corner is not painted (rounded away) but a mid-edge border pixel is.
func TestPaintRoundedBorderUniform(t *testing.T) {
	f := NewFonts()
	dst := white(40, 40)
	side := css.BorderSide{Width: 1, Style: css.BorderSolid, Color: css.Color{R: 200, A: 255}}
	box := &layout.Box{
		Node:  &dom.Node{Type: dom.Element, Tag: "div"},
		Style: &css.Style{Border: css.Borders{Top: side, Right: side, Bottom: side, Left: side}, BorderRadius: css.Length{Px: 8}},
		X:     0, Y: 0, W: 40, H: 40,
	}
	Paint(dst, box, f, nil)
	// Extreme corner not stroked (rounded away) → white.
	if c := dst.RGBAAt(0, 0); c.R != 255 {
		t.Errorf("rounded border corner (0,0) = %+v, want white", c)
	}
	// Mid top edge has the border ink.
	if c := dst.RGBAAt(20, 0); c.R != 200 {
		t.Errorf("mid top border = %+v, want reddish ink", c)
	}
}

// TestUniformBorder covers the helper's true/false branches.
func TestUniformBorder(t *testing.T) {
	s := css.BorderSide{Width: 1, Style: css.BorderSolid, Color: css.Color{A: 255}}
	if !uniformBorder(css.Borders{Top: s, Right: s, Bottom: s, Left: s}) {
		t.Error("identical sides should be uniform")
	}
	diff := s
	diff.Width = 2
	if uniformBorder(css.Borders{Top: s, Right: diff, Bottom: s, Left: s}) {
		t.Error("differing sides should not be uniform")
	}
}

// TestPaintRoundedNonUniformBorderFallsBack: a rounded box whose borders differ
// per side falls back to straight per-edge fills (each edge still drawn).
func TestPaintRoundedNonUniformBorderFallsBack(t *testing.T) {
	f := NewFonts()
	dst := white(40, 40)
	box := &layout.Box{
		Node: &dom.Node{Type: dom.Element, Tag: "div"},
		Style: &css.Style{BorderRadius: css.Length{Px: 8}, Border: css.Borders{
			Top:  css.BorderSide{Width: 2, Style: css.BorderSolid, Color: css.Color{R: 255, A: 255}},
			Left: css.BorderSide{Width: 2, Style: css.BorderSolid, Color: css.Color{B: 255, A: 255}},
		}},
		X: 0, Y: 0, W: 40, H: 40,
	}
	Paint(dst, box, f, nil)
	if c := dst.RGBAAt(20, 0); c.R != 255 {
		t.Errorf("non-uniform top edge = %+v, want red straight fill", c)
	}
	if c := dst.RGBAAt(0, 20); c.B != 255 {
		t.Errorf("non-uniform left edge = %+v, want blue straight fill", c)
	}
}

func TestPaintBorderStyleNoneNotDrawn(t *testing.T) {
	f := NewFonts()
	dst := white(10, 10)
	// Width set but style none, and a zero-alpha colour: neither draws.
	box := &layout.Box{
		Node: &dom.Node{Type: dom.Element, Tag: "div"},
		Style: &css.Style{Border: css.Borders{
			Top:  css.BorderSide{Width: 3, Style: css.BorderNone, Color: css.Color{R: 255, A: 255}},
			Left: css.BorderSide{Width: 3, Style: css.BorderSolid, Color: css.Color{}}, // A==0
		}},
		X: 0, Y: 0, W: 10, H: 10,
	}
	Paint(dst, box, f, nil)
	if c := dst.RGBAAt(5, 1); c.R != 255 || c.G != 255 || c.B != 255 {
		t.Errorf("none-style border drew: %+v", c)
	}
}

func TestPaintAnonymousSkipsBorder(t *testing.T) {
	f := NewFonts()
	dst := white(10, 10)
	// An anonymous box carries a style with a border but must not paint it.
	box := &layout.Box{
		Anonymous: true,
		Style: &css.Style{Border: css.Borders{
			Top: css.BorderSide{Width: 3, Style: css.BorderSolid, Color: css.Color{R: 255, A: 255}},
		}},
		X: 0, Y: 0, W: 10, H: 10,
	}
	Paint(dst, box, f, nil)
	if c := dst.RGBAAt(5, 1); c.R != 255 || c.G != 255 || c.B != 255 {
		t.Errorf("anonymous box painted a border: %+v", c)
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

func TestPaintNestedChildBoxes(t *testing.T) {
	// A parent box with a child box exercises the recursive child descent.
	f := NewFonts()
	dst := white(30, 30)
	parent := &layout.Box{
		Node:  &dom.Node{Type: dom.Element, Tag: "div"},
		Style: &css.Style{Background: css.Color{R: 240, G: 240, B: 240, A: 255}},
		X:     0, Y: 0, W: 30, H: 30,
		Children: []*layout.Box{{
			Node:  &dom.Node{Type: dom.Element, Tag: "div"},
			Style: &css.Style{Background: css.Color{R: 10, G: 120, B: 200, A: 255}},
			X:     5, Y: 5, W: 10, H: 10,
		}},
	}
	Paint(dst, parent, f, nil)
	// The child's blue fill is present at its centre.
	if c := dst.RGBAAt(9, 9); c.B < 150 || c.R > 60 {
		t.Errorf("nested child fill = %+v want blue", c)
	}
	// The parent's grey shows outside the child.
	if c := dst.RGBAAt(20, 20); c.R != 240 {
		t.Errorf("parent fill = %+v want grey", c)
	}
}

func TestPaintVisibilityHiddenSkipsOwnPaintButNotChildren(t *testing.T) {
	// visibility:hidden must paint none of the box's OWN background/border,
	// but — unlike display:none or opacity:0 — must still recurse into
	// children: a descendant may reset visibility:visible and paint normally.
	// This is the exact mechanism a real site's `:host(...) slot{visibility:
	// hidden}` / sr-only idiom relies on to hide a wrapper while still
	// allowing a nested element to opt back in.
	f := NewFonts()
	dst := white(30, 30)
	parent := &layout.Box{
		Node:  &dom.Node{Type: dom.Element, Tag: "div"},
		Style: &css.Style{Visibility: css.VisibilityHidden, Background: css.Color{R: 240, G: 240, B: 240, A: 255}},
		X:     0, Y: 0, W: 30, H: 30,
		Children: []*layout.Box{{
			Node:  &dom.Node{Type: dom.Element, Tag: "div"},
			Style: &css.Style{Visibility: css.VisibilityVisible, Background: css.Color{R: 10, G: 120, B: 200, A: 255}},
			X:     5, Y: 5, W: 10, H: 10,
		}},
	}
	Paint(dst, parent, f, nil)
	// The hidden parent's grey background does NOT show.
	if c := dst.RGBAAt(20, 20); c.R != 255 {
		t.Errorf("hidden parent painted its background: %+v want untouched white", c)
	}
	// The child, which reset visibility back to visible, still paints.
	if c := dst.RGBAAt(9, 9); c.B < 150 || c.R > 60 {
		t.Errorf("visible child inside a hidden parent = %+v want blue", c)
	}
}

func TestPaintSubPixelBorderNotDrawn(t *testing.T) {
	// A sub-pixel border width rounds to 0 px; the zero-size fill guard skips it
	// without panicking or painting.
	f := NewFonts()
	dst := white(10, 10)
	box := &layout.Box{
		Node: &dom.Node{Type: dom.Element, Tag: "div"},
		Style: &css.Style{Border: css.Borders{
			Top: css.BorderSide{Width: 0.3, Style: css.BorderSolid, Color: css.Color{R: 255, A: 255}},
		}},
		X: 0, Y: 0, W: 10, H: 10,
	}
	Paint(dst, box, f, nil)
	if c := dst.RGBAAt(5, 0); c.R != 255 || c.G != 255 || c.B != 255 {
		t.Errorf("sub-pixel border drew: %+v", c)
	}
}

func TestMustParseFontPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("mustParseFont should panic on invalid font bytes")
		}
	}()
	mustParseFont([]byte("not a font"))
}
