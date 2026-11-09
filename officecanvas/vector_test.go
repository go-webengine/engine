// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package officecanvas

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"net/url"
	"testing"

	"github.com/go-webengine/engine/css"
	"github.com/go-webengine/engine/paint"
)

func svgPage(svg string) string {
	return `<!DOCTYPE html><html><head><style>body{margin:0;background:#fff} img{display:block}</style></head><body>` +
		`<img width="100" height="100" src="data:image/svg+xml,` + url.PathEscape(svg) + `"></body></html>`
}

// An SVG becomes one group of filled and gradient-filled shapes; one that
// needs a clip, or a radial gradient, its picture.
func TestSVGIsAGroupOfShapes(t *testing.T) {
	pg, _ := record(t, svgPage(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 20 20"><defs><linearGradient id="g"><stop offset="0" stop-color="#00f"/><stop offset="1" stop-color="#0ff"/></linearGradient></defs><rect width="10" height="10" fill="#f00"/><rect x="10" width="10" height="10" fill="url(#g)"/><path d="M0 15 H20" stroke="#080" stroke-width="2"/></svg>`))
	// the body's background, then the drawing
	if len(pg.Shapes) != 2 || pg.Shapes[1].Kind != KindGroup {
		t.Fatalf("shapes %+v, want the body and one group", pg.Shapes)
	}
	g := pg.Shapes[1]
	if len(g.Group) != 3 || g.Group[0].Kind != KindFill || g.Group[1].Kind != KindGradient || g.Group[2].Kind != KindFill {
		t.Fatalf("members %+v", g.Group)
	}
	if g.Group[0].Color != (css.Color{R: 255, A: 255}) || !g.Group[0].Rect {
		t.Errorf("the red square: %+v", g.Group[0])
	}
	if gr := g.Group[1].Gradient; gr == nil || gr.Radial || gr.Angle != 0 || len(gr.Stops) != 2 {
		t.Errorf("the gradient: %+v", gr)
	}
	// the stroke's outline, 2 units under the 5× map: 10 pixels high
	if b := g.Group[2].Bounds; b.H < 9.5 || b.H > 10.5 || b.W < 99 {
		t.Errorf("the stroke's outline: %+v", b)
	}
	// the squares' top to the stroke's lower edge, 16 units at 5×
	if b := g.Bounds; b.X != 0 || b.Y != 0 || b.W != 100 || b.H < 79.9 || b.H > 80.1 {
		t.Errorf("group bounds %+v", b)
	}

	for name, svg := range map[string]string{
		"clip":   `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 20 20"><clipPath id="c"><circle cx="10" cy="10" r="8"/></clipPath><rect width="20" height="20" fill="#f00" clip-path="url(#c)"/></svg>`,
		"radial": `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 20 20"><defs><radialGradient id="r"><stop offset="0" stop-color="#ff0"/><stop offset="1" stop-color="#f00"/></radialGradient></defs><rect width="20" height="20" fill="url(#r)"/></svg>`,
	} {
		pg, _ := record(t, svgPage(svg))
		if len(pg.Shapes) != 2 || pg.Shapes[1].Kind != KindImage {
			t.Errorf("%s: %+v, want its picture", name, pg.Shapes)
		}
	}
}

// Groups: one member stays as it is, none is nothing, an EndGroup with no
// group is ignored, and a page-covering rectangle inside a group is a shape,
// not the slide's background.
func TestGroupEdges(t *testing.T) {
	c := New(image.Rect(0, 0, 100, 100), nil, nil)
	c.EndGroup()
	c.BeginGroup()
	c.EndGroup()
	if len(c.Page().Shapes) != 0 {
		t.Error("an empty group left a shape")
	}
	var full paint.Path
	full.RoundRect(paint.Rect{W: 100, H: 100}, 0)
	c.BeginGroup()
	c.Fill(&full, css.Color{R: 9, A: 255}, false)
	c.EndGroup()
	if p := c.Page(); p.Background != nil || len(p.Shapes) != 1 || p.Shapes[0].Kind != KindFill {
		t.Errorf("one member: %+v", p)
	}
	if s := c.VectorSupport(); s.Clips || s.RadialGradients {
		t.Errorf("support %+v", s)
	}
}

// A group says which SVG document it was drawn from and, for each member, the
// element it was drawn for, so that an exporter can find the pieces of a
// drawing again (an animated one, each piece on its own).
func TestGroupSaysWhereItsShapesCameFrom(t *testing.T) {
	const svg = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 20 20"><rect width="10" height="10" fill="#f00"/><g id="g"><circle cx="15" cy="15" r="4" fill="#00f"/><path d="M0 18H20" stroke="#080" stroke-width="2"/></g></svg>`
	pg, _ := record(t, svgPage(svg))
	g := pg.Shapes[1]
	sum := sha256.Sum256([]byte(svg))
	if g.Kind != KindGroup || g.Source != hex.EncodeToString(sum[:]) {
		t.Fatalf("group %+v, source %s", g, hex.EncodeToString(sum[:]))
	}
	if g.ViewBox != (paint.Rect{W: 20, H: 20}) || g.Dst.W != 100 || g.Dst.H != 100 {
		t.Errorf("boxes: view %+v, dst %+v", g.ViewBox, g.Dst)
	}
	// svg 0, rect 1, g 2, circle 3, path 4
	want := [][]int{{0, 1}, {0, 2, 3}, {0, 2, 4}}
	if len(g.Group) != 3 {
		t.Fatalf("members %+v", g.Group)
	}
	for i, w := range want {
		if fmt.Sprint(g.Group[i].Keys) != fmt.Sprint(w) {
			t.Errorf("member %d: keys %v, want %v", i, g.Group[i].Keys, w)
		}
	}
	// one shape stays a shape, and carries the same facts
	pg, _ = record(t, svgPage(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 20 20"><rect id="a" width="10" height="10" fill="#f00"/></svg>`))
	if s := pg.Shapes[1]; s.Kind != KindFill || s.Source == "" || fmt.Sprint(s.Keys) != "[0 1]" {
		t.Errorf("single shape %+v", s)
	}
}
