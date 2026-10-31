// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package engine

import (
	"context"
	"image"
	"net/url"
	"strings"
	"testing"

	"github.com/go-webengine/engine/css"
	"github.com/go-webengine/engine/paint"
)

// opCanvas records the operations PaintVector draws with.
type opCanvas struct {
	ops     []string
	fills   []css.Color
	shades  []paint.Shading
	support *paint.VectorSupport
}

func (c *opCanvas) Save()                  { c.ops = append(c.ops, "q") }
func (c *opCanvas) Restore()               { c.ops = append(c.ops, "Q") }
func (c *opCanvas) Clip(*paint.Path, bool) { c.ops = append(c.ops, "clip") }
func (c *opCanvas) Fill(_ *paint.Path, col css.Color, _ bool) {
	c.ops = append(c.ops, "fill")
	c.fills = append(c.fills, col)
}
func (c *opCanvas) Shade(s paint.Shading) {
	c.ops = append(c.ops, "shade")
	c.shades = append(c.shades, s)
}
func (c *opCanvas) Image(image.Image, paint.Rect) { c.ops = append(c.ops, "image") }
func (c *opCanvas) Glyphs(*paint.GlyphRun)        { c.ops = append(c.ops, "glyphs") }

// groupCanvas is an opCanvas that groups and says what it supports.
type groupCanvas struct{ opCanvas }

func (c *groupCanvas) BeginGroup()                        { c.ops = append(c.ops, "group") }
func (c *groupCanvas) EndGroup()                          { c.ops = append(c.ops, "end") }
func (c *groupCanvas) VectorSupport() paint.VectorSupport { return *c.support }

// formCanvas is an opCanvas that places vector images itself.
type formCanvas struct {
	opCanvas
	placed []*paint.VectorImage
}

func (c *formCanvas) VectorImage(v *paint.VectorImage, _ paint.Rect) bool {
	c.placed = append(c.placed, v)
	return true
}

func (c *opCanvas) count(op string) int {
	n := 0
	for _, o := range c.ops {
		if o == op {
			n++
		}
	}
	return n
}

const (
	// a red square and a blue linear gradient, a stroked circle
	plainSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 20 20"><defs><linearGradient id="g"><stop offset="0" stop-color="#00f"/><stop offset="1" stop-color="#0ff"/></linearGradient></defs><rect width="10" height="10" fill="#f00"/><rect x="10" width="10" height="10" fill="url(#g)"/><circle cx="10" cy="15" r="4" fill="none" stroke="#080" stroke-width="2"/></svg>`
	clipSVG  = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 20 20"><clipPath id="c"><circle cx="10" cy="10" r="8"/></clipPath><rect width="20" height="20" fill="#f00" clip-path="url(#c)"/></svg>`
	radSVG   = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 20 20"><defs><radialGradient id="r"><stop offset="0" stop-color="#ff0"/><stop offset="1" stop-color="#f00"/></radialGradient></defs><rect width="20" height="20" fill="url(#r)"/></svg>`
	// a focal radial gradient: no exact vector form
	focalSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 20 20"><defs><radialGradient id="r" fx="0.2" fy="0.2"><stop offset="0" stop-color="#ff0"/><stop offset="1" stop-color="#f00"/></radialGradient></defs><rect width="20" height="20" fill="url(#r)"/></svg>`
)

func svgURI(svg string) string { return "data:image/svg+xml," + url.PathEscape(svg) }

// vectorLayout lays out body, every image an SVG.
func vectorLayout(t *testing.T, body string) *PageLayout {
	t.Helper()
	e := New()
	e.DisableJS = true
	html := `<!DOCTYPE html><html><head><style>body{margin:0;background:#fff} img,svg,div{display:block}</style></head><body>` + body + `</body></html>`
	l, err := e.LayoutHTML(context.Background(), html, "https://example.com/", image.Rect(0, 0, 400, 400))
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// An inline <svg>, an <img src=svg> and an SVG background are drawn as
// vector shapes: fills, the gradient as a shading through its shape, the
// stroke as a filled outline; no bitmap.
func TestSVGIsDrawnAsVectors(t *testing.T) {
	for name, body := range map[string]string{
		"inline":     strings.Replace(plainSVG, "<svg ", `<svg width="100" height="100" `, 1),
		"img":        `<img width="100" height="100" src="` + svgURI(plainSVG) + `">`,
		"background": `<div style="width:100px;height:100px;background:url('` + svgURI(plainSVG) + `') 0 0/100px 100px no-repeat"></div>`,
	} {
		var c opCanvas
		st := vectorLayout(t, body).PaintVector(&c)
		if st.Vectors != 1 || st.Images != 0 || c.count("image") != 0 {
			t.Errorf("%s: stats %+v, ops %v", name, st, c.ops)
			continue
		}
		red, green := 0, 0
		for _, f := range c.fills {
			switch f {
			case css.Color{R: 255, A: 255}:
				red++
			case css.Color{G: 0x88, A: 255}:
				green++
			}
		}
		if red != 1 || green != 1 || len(c.shades) != 1 {
			t.Errorf("%s: %d red fills, %d green strokes, %d shadings; ops %v", name, red, green, len(c.shades), c.ops)
			continue
		}
		// the gradient runs across the right half of the 100px box: x 50 to 100
		if s := c.shades[0]; s.Radial || s.X0 != 50 || s.X1 != 100 || !s.ExtendStart || !s.ExtendEnd {
			t.Errorf("%s: shading %+v", name, s)
		}
	}
}

// What has no exact vector form, or is filtered, is placed as its bitmap.
func TestSVGFallsBackToItsBitmap(t *testing.T) {
	for name, body := range map[string]string{
		"focal gradient": `<img width="100" height="100" src="` + svgURI(focalSVG) + `">`,
		"filter":         `<img style="filter:grayscale(1)" width="100" height="100" src="` + svgURI(plainSVG) + `">`,
	} {
		var c opCanvas
		st := vectorLayout(t, body).PaintVector(&c)
		if st.Vectors != 0 || c.count("image") != 1 {
			t.Errorf("%s: stats %+v, ops %v", name, st, c.ops)
		}
	}
}

// A canvas that cannot clip, nor draw an SVG radial gradient, gets those
// images as bitmaps, and the others as vector shapes inside a group.
func TestSVGOnACanvasThatCannotClip(t *testing.T) {
	none := paint.VectorSupport{}
	cases := []struct {
		body   string
		vector bool
	}{
		{`<img width="100" height="100" src="` + svgURI(plainSVG) + `">`, true},
		{`<img width="100" height="100" src="` + svgURI(clipSVG) + `">`, false},
		{`<img width="100" height="100" src="` + svgURI(radSVG) + `">`, false},
		// object-fit: an SVG's bitmap is made at its box's size (stretched,
		// as oksvg maps the viewBox), so neither fit cuts it
		{`<img style="object-fit:cover" width="100" height="50" src="` + svgURI(plainSVG) + `">`, true},
		{`<img style="object-fit:contain" width="100" height="50" src="` + svgURI(plainSVG) + `">`, true},
		// a tile its box cuts
		{`<div style="width:50px;height:50px;background:url('` + svgURI(plainSVG) + `') 0 0/100px 100px no-repeat"></div>`, false},
	}
	for i, tc := range cases {
		c := groupCanvas{opCanvas{support: &none}}
		st := vectorLayout(t, tc.body).PaintVector(&c)
		if got := st.Vectors == 1; got != tc.vector {
			t.Errorf("case %d: vector %v, want %v (ops %v)", i, got, tc.vector, c.ops)
		}
		if tc.vector && (c.count("group") != 1 || c.count("end") != 1) {
			t.Errorf("case %d: not one group: %v", i, c.ops)
		}
	}
	// With full support the clipped and the radial ones are vectors too.
	all := paint.VectorSupport{Clips: true, RadialGradients: true}
	for _, svg := range []string{clipSVG, radSVG} {
		c := groupCanvas{opCanvas{support: &all}}
		if st := vectorLayout(t, `<img width="100" height="100" src="`+svgURI(svg)+`">`).PaintVector(&c); st.Vectors != 1 {
			t.Errorf("full support: %+v %v", st, c.ops)
		}
	}
}

// A hostile dash array (ten million dashes) neither hangs the page nor
// reaches the canvas as ten million shapes: it has no vector form, and its
// bitmap strokes it solid.
func TestHostileDashesArePlacedAsTheBitmap(t *testing.T) {
	svg := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100"><path d="M0 50 H10000" fill="none" stroke="#000" stroke-width="4" stroke-dasharray="0.001"/></svg>`
	var c opCanvas
	st := vectorLayout(t, `<img width="100" height="100" src="`+svgURI(svg)+`">`).PaintVector(&c)
	if st.Vectors != 0 || c.count("image") != 1 {
		t.Errorf("stats %+v, ops %v", st, c.ops)
	}
}

// A VectorCanvas places the image itself: one call, the same form for the
// same drawing, nothing drawn shape by shape.
func TestVectorCanvasPlacesTheImage(t *testing.T) {
	img := `<img width="40" height="40" src="` + svgURI(plainSVG) + `">`
	var c formCanvas
	st := vectorLayout(t, img+img+`<img width="40" height="40" src="`+svgURI(clipSVG)+`">`).PaintVector(&c)
	if st.Vectors != 3 || len(c.placed) != 3 || len(c.shades) != 0 {
		t.Fatalf("stats %+v, placed %d, ops %v", st, len(c.placed), c.ops)
	}
	if c.placed[0].Key != c.placed[1].Key || c.placed[0].Key == c.placed[2].Key || c.placed[0].Key == "" {
		t.Errorf("keys %q %q %q", c.placed[0].Key, c.placed[1].Key, c.placed[2].Key)
	}
	if !c.placed[2].Clipped() || c.placed[0].Clipped() {
		t.Error("Clipped")
	}
}

// The bitmap render does not change: a page of SVGs paints as it did, and
// never asks for a vector form.
func TestSVGBitmapRenderIsUnchanged(t *testing.T) {
	l := vectorLayout(t, `<img width="100" height="100" src="`+svgURI(plainSVG)+`">`)
	for _, src := range l.rp.vecs {
		if src.Image() == nil {
			t.Fatal("no vector form")
		}
	}
	l = vectorLayout(t, `<img width="100" height="100" src="`+svgURI(plainSVG)+`">`)
	img := l.Paint()
	if c := img.RGBAAt(20, 20); c.R < 240 || c.G > 20 {
		t.Errorf("the red square paints %v", c)
	}
}
