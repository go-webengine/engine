// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package paint

import (
	"image"
	"math"
	"testing"

	"github.com/go-webengine/engine/css"
	"github.com/go-webengine/engine/dom"
	"github.com/go-webengine/engine/layout"
)

// square is a closed square path.
func square(x, y, s float64) Path {
	var p Path
	p.RoundRect(Rect{X: x, Y: y, W: s, H: s}, 0)
	return p
}

// testDrawing is a 20×20 drawing: a red square top left, a linear gradient
// top right, and a green square bottom left clipped to its left half.
func testDrawing() *VectorImage {
	return NewVectorImage(Rect{W: 20, H: 20}, []VectorShape{
		{Path: square(0, 0, 10), Color: rgb(255, 0, 0)},
		{Path: square(10, 0, 10), Gradient: &Shading{X0: 10, X1: 20, Stops: []GradientStop{{0, rgb(0, 0, 255)}, {1, rgb(0, 255, 255)}}}},
		{Path: square(0, 10, 10), Color: rgb(0, 160, 0), Clips: []VectorClip{{Path: square(0, 10, 5)}}},
	})
}

func TestNewVectorImage(t *testing.T) {
	v := testDrawing()
	if v == nil || v.Key == "" || !v.Clipped() || v.Radial() {
		t.Fatalf("%+v", v)
	}
	if w := testDrawing(); w.Key != v.Key {
		t.Error("the same drawing has two keys")
	}
	w := testDrawing()
	w = NewVectorImage(w.ViewBox, append(w.Shapes[:1:1], VectorShape{Path: square(0, 0, 10), EvenOdd: true, Color: rgb(255, 0, 0)}))
	if w == nil || w.Key == v.Key {
		t.Error("an even-odd shape does not change the key")
	}
	nan := math.NaN()
	for name, mk := range map[string]func() *VectorImage{
		"empty box":      func() *VectorImage { return NewVectorImage(Rect{W: 0, H: 1}, testDrawing().Shapes) },
		"box not finite": func() *VectorImage { return NewVectorImage(Rect{X: nan, W: 1, H: 1}, testDrawing().Shapes) },
		"no shapes":      func() *VectorImage { return NewVectorImage(Rect{W: 1, H: 1}, nil) },
		"point not finite": func() *VectorImage {
			p := square(0, 0, 1)
			p.Ops[1].P[0].X = math.Inf(1)
			return NewVectorImage(Rect{W: 1, H: 1}, []VectorShape{{Path: p}})
		},
		"clip not finite": func() *VectorImage {
			p := square(0, 0, 1)
			p.Ops[0].P[0].Y = nan
			return NewVectorImage(Rect{W: 1, H: 1}, []VectorShape{{Path: square(0, 0, 1), Clips: []VectorClip{{Path: p}}}})
		},
		"gradient not finite": func() *VectorImage {
			return NewVectorImage(Rect{W: 1, H: 1}, []VectorShape{{Path: square(0, 0, 1), Gradient: &Shading{X1: nan, Stops: []GradientStop{{0, rgb(1, 2, 3)}, {1, rgb(1, 2, 3)}}}}})
		},
		"gradient of no stop": func() *VectorImage {
			return NewVectorImage(Rect{W: 1, H: 1}, []VectorShape{{Path: square(0, 0, 1), Gradient: &Shading{X1: 1}}})
		},
		"too many operations": func() *VectorImage {
			var p Path
			for i := 0; i <= MaxVectorOps; i++ {
				p.LineTo(float64(i), 1)
			}
			return NewVectorImage(Rect{W: 1, H: 1}, []VectorShape{{Path: p}})
		},
	} {
		if mk() != nil {
			t.Errorf("%s: an image", name)
		}
	}
	// A radial gradient of one stop, and stops out of order or not numbers.
	r := NewVectorImage(Rect{W: 1, H: 1}, []VectorShape{
		{Path: square(0, 0, 1), Gradient: &Shading{Radial: true, RX: 1, RY: 1, R1: 1, Stops: []GradientStop{{0.5, rgb(9, 9, 9)}}}},
		{Path: square(0, 0, 1), Gradient: &Shading{X1: 1, Stops: []GradientStop{{0.6, rgb(1, 0, 0)}, {0.2, rgb(2, 0, 0)}, {nan, rgb(3, 0, 0)}, {1.5, rgb(4, 0, 0)}}}},
	})
	if r == nil || !r.Radial() || r.Clipped() {
		t.Fatalf("%+v", r)
	}
	if s := r.Shapes[0].Gradient.Stops; len(s) != 2 || s[0].Pos != 0 || s[1].Pos != 1 || s[1].Color != rgb(9, 9, 9) {
		t.Errorf("one stop: %+v", s)
	}
	s := r.Shapes[1].Gradient.Stops
	for i := 1; i < len(s); i++ {
		if !(s[i].Pos > s[i-1].Pos) || s[i].Pos > 1 || s[0].Pos != 0.6 {
			t.Errorf("stops not increasing in [0.6,1]: %+v", s)
		}
	}
}

func TestVectorSource(t *testing.T) {
	var none *VectorSource
	if none.Image() != nil {
		t.Error("a nil source has an image")
	}
	v := testDrawing()
	if VectorOf(v).Image() != v || VectorOf(nil).Image() != nil {
		t.Error("VectorOf")
	}
	calls := 0
	s := NewVectorSource(func() *VectorImage { calls++; return v })
	if s.Image() != v || s.Image() != v || calls != 1 {
		t.Errorf("built %d times", calls)
	}
	if NewVectorSource(nil).Image() != nil {
		t.Error("no build, an image")
	}
}

// limitedCanvas is a refCanvas that groups and says what it supports.
type limitedCanvas struct {
	*refCanvas
	support VectorSupport
	groups  int
}

func (c *limitedCanvas) BeginGroup()                  { c.groups++ }
func (c *limitedCanvas) EndGroup()                    {}
func (c *limitedCanvas) VectorSupport() VectorSupport { return c.support }

// taking is a refCanvas whose VectorImage takes every image.
type taking struct {
	*refCanvas
	took int
}

func (c *taking) VectorImage(*VectorImage, Rect) bool { c.took++; return true }

// declining is a refCanvas whose VectorImage takes nothing.
type declining struct {
	*refCanvas
	asked int
}

func (c *declining) VectorImage(*VectorImage, Rect) bool { c.asked++; return false }

// An <img> and a background tile whose bitmaps have a vector form draw it:
// the red square, the gradient through its square, the clipped green half,
// at the image's box; a filtered image is its bitmap.
func TestPaintVectorWithDrawsTheVectorForm(t *testing.T) {
	n := &dom.Node{Type: dom.Element, Tag: "img"}
	bitmap := image.NewRGBA(image.Rect(0, 0, 40, 40))
	vecs := VectorImages{bitmap: VectorOf(testDrawing())}
	item := func(st *css.Style) *layout.Box {
		return &layout.Box{Node: div(), W: 100, H: 100, Style: &css.Style{}, Lines: []*layout.LineBox{{Items: []*layout.InlineItem{{Image: n, Style: st, X: 10, Y: 10, Width: 40, LineHeight: 40}}}}}
	}
	imgs := map[*dom.Node]image.Image{n: bitmap}
	check := func(c *refCanvas, ox, oy int) {
		t.Helper()
		for _, p := range []struct {
			x, y int
			want css.Color
		}{
			{ox + 5, oy + 5, rgb(255, 0, 0)},       // red
			{ox + 5, oy + 25, rgb(0, 160, 0)},      // green, inside the clip
			{ox + 15, oy + 25, rgb(255, 255, 255)}, // outside it
			{ox + 5, oy + 35, rgb(255, 255, 255)},  // below it
		} {
			if got := c.img.RGBAAt(p.x, p.y); got.R != p.want.R || got.G != p.want.G || got.B != p.want.B {
				t.Errorf("(%d,%d) = %v, want %v", p.x, p.y, got, p.want)
			}
		}
		if got := c.img.RGBAAt(ox+25, oy+5); got.R != 0 || got.B != 255 {
			t.Errorf("the gradient's start = %v", got)
		}
	}
	c := newRefCanvas(100, 100)
	st := PaintVectorWith(c, item(&css.Style{}), NewFonts(), imgs, nil, vecs, image.Rect(0, 0, 100, 100))
	if st.Vectors != 1 || st.Images != 0 || c.images != 0 || c.shades != 1 {
		t.Fatalf("stats %+v, images %d, shades %d", st, c.images, c.shades)
	}
	check(c, 10, 10)
	// object-fit keeps the drawing in its box when it does not cut it
	c = newRefCanvas(100, 100)
	if st := PaintVectorWith(c, item(&css.Style{ObjectFit: css.ObjectFitContain}), NewFonts(), imgs, nil, vecs, image.Rect(0, 0, 100, 100)); st.Vectors != 1 {
		t.Errorf("contain: %+v", st)
	}
	check(c, 10, 10)
	// filtered: the bitmap
	c = newRefCanvas(100, 100)
	if st := PaintVectorWith(c, item(&css.Style{Filters: []css.Filter{{Kind: css.FilterInvert, Amount: 1}}}), NewFonts(), imgs, nil, vecs, image.Rect(0, 0, 100, 100)); st.Vectors != 0 || st.Images != 1 {
		t.Errorf("filter: %+v", st)
	}
	// a background tile at 50,50
	bg := urlStyle("u", css.BgSize{Kind: css.SizeExplicit, W: css.Length{Px: 40}, H: css.Length{Px: 40}}, css.BgPosition{}, css.NoRepeat)
	box := &layout.Box{Node: div(), X: 50, Y: 50, W: 40, H: 40, Style: bg}
	c = newRefCanvas(100, 100)
	if st := PaintVectorWith(c, box, NewFonts(), nil, map[string]image.Image{"u": bitmap}, vecs, image.Rect(0, 0, 100, 100)); st.Vectors != 1 || c.images != 0 {
		t.Errorf("background: %+v", st)
	}
	check(c, 50, 50)
	// A canvas whose VectorImage declines gets the shapes.
	d := &declining{refCanvas: newRefCanvas(100, 100)}
	PaintVectorWith(d, item(&css.Style{}), NewFonts(), imgs, nil, vecs, image.Rect(0, 0, 100, 100))
	if d.asked != 1 || d.fills != 2 {
		t.Errorf("declined: asked %d, fills %d", d.asked, d.fills)
	}
	check(d.refCanvas, 10, 10)
	// A canvas whose VectorImage takes it draws nothing else.
	tk := &taking{refCanvas: newRefCanvas(100, 100)}
	if st := PaintVectorWith(tk, item(&css.Style{}), NewFonts(), imgs, nil, vecs, image.Rect(0, 0, 100, 100)); st.Vectors != 1 || tk.took != 1 || tk.fills != 0 || tk.shades != 0 {
		t.Errorf("taken: %+v, took %d, fills %d", st, tk.took, tk.fills)
	}
	// No vector forms, or none of this bitmap: the bitmap, as PaintVector
	// draws it.
	for _, vs := range []VectorImages{{}, {image.NewRGBA(image.Rect(0, 0, 1, 1)): VectorOf(testDrawing())}} {
		c = newRefCanvas(100, 100)
		if st := PaintVectorWith(c, item(&css.Style{}), NewFonts(), imgs, nil, vs, image.Rect(0, 0, 100, 100)); st.Images != 1 || st.Vectors != 0 {
			t.Errorf("no form: %+v", st)
		}
	}
}

// A canvas that cannot clip gets a clipped drawing, or one its box cuts, as
// its bitmap; one that cannot draw a radial gradient, a radial one; the
// others in a group.
func TestPaintVectorWithLimitedCanvas(t *testing.T) {
	plain := NewVectorImage(Rect{W: 20, H: 20}, []VectorShape{{Path: square(0, 0, 10), Color: rgb(255, 0, 0)}})
	radial := NewVectorImage(Rect{W: 20, H: 20}, []VectorShape{{Path: square(0, 0, 20), Gradient: &Shading{Radial: true, CX: 10, CY: 10, RX: 10, RY: 10, R1: 1, Stops: []GradientStop{{0, rgb(255, 255, 0)}, {1, rgb(255, 0, 0)}}}}})
	bitmap := image.NewRGBA(image.Rect(0, 0, 40, 40))
	n := &dom.Node{Type: dom.Element, Tag: "img"}
	item := &layout.Box{Node: div(), W: 100, H: 100, Style: &css.Style{}, Lines: []*layout.LineBox{{Items: []*layout.InlineItem{{Image: n, Style: &css.Style{}, X: 10, Y: 10, Width: 40, LineHeight: 40}}}}}
	imgs := map[*dom.Node]image.Image{n: bitmap}
	for _, tc := range []struct {
		name    string
		v       *VectorImage
		support VectorSupport
		vector  bool
	}{
		{"plain", plain, VectorSupport{}, true},
		{"clipped", testDrawing(), VectorSupport{}, false},
		{"clipped, clips", testDrawing(), VectorSupport{Clips: true}, true},
		{"radial", radial, VectorSupport{Clips: true}, false},
		{"radial, radials", radial, VectorSupport{RadialGradients: true}, true},
	} {
		c := &limitedCanvas{refCanvas: newRefCanvas(100, 100), support: tc.support}
		st := PaintVectorWith(c, item, NewFonts(), imgs, nil, VectorImages{bitmap: VectorOf(tc.v)}, image.Rect(0, 0, 100, 100))
		if got := st.Vectors == 1; got != tc.vector || (got && c.groups != 1) {
			t.Errorf("%s: stats %+v, groups %d", tc.name, st, c.groups)
		}
	}
	// A tile its box cuts (the box is smaller than the tile) or rounds.
	for name, st := range map[string]*css.Style{
		"cut":     urlStyle("u", css.BgSize{Kind: css.SizeExplicit, W: css.Length{Px: 60}, H: css.Length{Px: 60}}, css.BgPosition{}, css.NoRepeat),
		"rounded": urlStyle("u", css.BgSize{Kind: css.SizeExplicit, W: css.Length{Px: 40}, H: css.Length{Px: 40}}, css.BgPosition{}, css.NoRepeat),
	} {
		if name == "rounded" {
			st.BorderRadius = css.Length{Px: 6}
		}
		c := &limitedCanvas{refCanvas: newRefCanvas(100, 100)}
		box := &layout.Box{Node: div(), W: 40, H: 40, Style: st}
		if vs := PaintVectorWith(c, box, NewFonts(), nil, map[string]image.Image{"u": bitmap}, VectorImages{bitmap: VectorOf(plain)}, image.Rect(0, 0, 100, 100)); vs.Vectors != 0 || c.images != 1 {
			t.Errorf("%s: %+v", name, vs)
		}
	}
}

func TestVectorMap(t *testing.T) {
	v := NewVectorImage(Rect{X: 10, Y: 20, W: 10, H: 5}, []VectorShape{{Path: square(10, 20, 1), Color: rgb(1, 1, 1)}})
	m := v.To(Rect{X: 100, Y: 200, W: 20, H: 20})
	if p := m.Point(Point{10, 20}); p != (Point{100, 200}) {
		t.Errorf("origin %v", p)
	}
	if p := m.Point(Point{20, 25}); p != (Point{120, 220}) {
		t.Errorf("corner %v", p)
	}
	s := m.Shading(Shading{Radial: true, CX: 15, CY: 22.5, RX: 2, RY: 2, X0: 10, Y0: 20, X1: 20, Y1: 20})
	if s.CX != 110 || s.CY != 210 || s.RX != 4 || s.RY != 8 || s.X0 != 100 || s.X1 != 120 {
		t.Errorf("shading %+v", s)
	}
	flip := VectorMap{SX: -1, SY: -2}
	if s := flip.Shading(Shading{RX: 3, RY: 3}); s.RX != 3 || s.RY != 6 {
		t.Errorf("radii under a flip %+v", s)
	}
}
