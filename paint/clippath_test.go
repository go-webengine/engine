// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package paint

import (
	"testing"

	"github.com/go-webengine/engine/css"
	"github.com/go-webengine/engine/dom"
	"github.com/go-webengine/engine/layout"
)

func clipPathStyle(bg css.Color, clipPath string) *css.Style {
	return &css.Style{Background: bg, ClipPath: clipPath}
}

// TestPaintClipPathPolygonTriangle covers the real regression: `clip-path`
// was entirely unimplemented for ordinary HTML elements, so a decorative
// shape cut from an element's box (confirmed live: vercel.com's own
// homepage, a "paper-peel" corner cut on an always-present background
// <div> — round 194, engine#330) rendered as a plain, uncut rectangle. A
// right triangle (the top-left half of the box) is a clean shape to probe:
// a point near the top-left corner is inside it, a point near the
// bottom-right corner is not.
func TestPaintClipPathPolygonTriangle(t *testing.T) {
	dst := white(20, 20)
	box := &layout.Box{
		Node:  &dom.Node{Type: dom.Element, Tag: "div"},
		Style: clipPathStyle(css.Color{R: 0, G: 100, B: 200, A: 255}, "polygon(0 0, 100% 0, 0 100%)"),
		X:     0, Y: 0, W: 20, H: 20,
	}
	PaintFull(dst, box, NewFonts(), nil, nil)
	if c := dst.RGBAAt(2, 2); c.B < 150 {
		t.Errorf("top-left (inside the triangle) = %+v, want the background colour", c)
	}
	if c := dst.RGBAAt(17, 17); c.R != 255 || c.G != 255 || c.B != 255 {
		t.Errorf("bottom-right (outside the triangle) = %+v, want white (clipped away)", c)
	}
}

// TestPaintClipPathPolygonVercelTrigger paints the exact confirmed trigger
// (the calc()-mixing "paper-peel" corner cut) at a size where the cut
// corner's effect is unambiguous: a point just inside the cut corner must
// be clipped away, while the opposite corner (far from the cut) must not.
func TestPaintClipPathPolygonVercelTrigger(t *testing.T) {
	dst := white(100, 50)
	box := &layout.Box{
		Node: &dom.Node{Type: dom.Element, Tag: "div"},
		Style: clipPathStyle(css.Color{R: 0, G: 200, B: 0, A: 255},
			"polygon(0 0, 100% 0, 100% calc(100% - 20px), calc(100% - 20px) 100%, 0 100%)"),
		X: 0, Y: 0, W: 100, H: 50,
	}
	PaintFull(dst, box, NewFonts(), nil, nil)
	if c := dst.RGBAAt(5, 5); c.G < 150 {
		t.Errorf("top-left (far from the cut corner) = %+v, want the background colour", c)
	}
	// The cut runs from (100,30) to (80,50): a point at (97,47) is beyond
	// that diagonal, in the cut-away corner.
	if c := dst.RGBAAt(97, 47); c.R != 255 || c.G != 255 || c.B != 255 {
		t.Errorf("bottom-right cut corner = %+v, want white (clipped away)", c)
	}
	// A point just inside the same corner, before the diagonal, must still
	// show the background.
	if c := dst.RGBAAt(85, 35); c.G < 150 {
		t.Errorf("just inside the cut corner = %+v, want the background colour", c)
	}
}

// TestPaintClipPathOutsideBorderBoxFullyMasked mirrors
// TestPaintMaskImageOutsideBorderBoxFullyMasked: clip-path affects the
// WHOLE element, including anything a child paints outside the clipped
// element's own border box.
func TestPaintClipPathOutsideBorderBoxFullyMasked(t *testing.T) {
	dst := white(20, 20)
	parent := &layout.Box{
		Node:  &dom.Node{Type: dom.Element, Tag: "span"},
		Style: clipPathStyle(css.Color{A: 0}, "polygon(0 0, 100% 0, 100% 100%, 0 100%)"), // the whole box; transparent itself
		X:     5, Y: 5, W: 10, H: 10,
		Children: []*layout.Box{{
			Node:  &dom.Node{Type: dom.Element, Tag: "div"},
			Style: &css.Style{Background: css.Color{R: 0, G: 200, B: 0, A: 255}},
			X:     0, Y: 7, W: 4, H: 4, // outside the parent's border box (parent starts at X=5)
		}},
	}
	PaintFull(dst, parent, NewFonts(), nil, nil)
	if c := dst.RGBAAt(2, 9); c.R != 255 || c.G != 255 || c.B != 255 {
		t.Errorf("content painted outside the clipped element's border box should be fully masked out, got %+v", c)
	}
}

// TestPaintClipPathZeroSizedBox covers applyClipPathMask's degenerate-box
// guard, mirroring TestPaintMaskImageZeroSizedBox.
func TestPaintClipPathZeroSizedBox(t *testing.T) {
	dst := white(10, 10)
	box := &layout.Box{
		Node:  &dom.Node{Type: dom.Element, Tag: "span"},
		Style: clipPathStyle(css.Color{R: 255, A: 255}, "polygon(0 0, 100% 0, 0 100%)"),
		X:     2, Y: 2, W: 0, H: 0,
	}
	PaintFull(dst, box, NewFonts(), nil, nil)
	if c := dst.RGBAAt(2, 2); c.R != 255 || c.G != 255 || c.B != 255 {
		t.Errorf("zero-sized clipped box should paint nothing, got %+v", c)
	}
}

// TestPaintClipPathEvenOddKeywordParses covers the leading "evenodd"
// fill-rule keyword reaching paint at all (not just css.
// ResolveClipPathPolygon in isolation — see TestResolveClipPathPolygonFillRule):
// on a simple, non-self-intersecting polygon the two fill rules agree
// everywhere (see pointInPolygon's own doc comment), so this exercises the
// keyword-stripping path end to end with the same assertions as the plain
// triangle test above.
func TestPaintClipPathEvenOddKeywordParses(t *testing.T) {
	dst := white(20, 20)
	box := &layout.Box{
		Node:  &dom.Node{Type: dom.Element, Tag: "div"},
		Style: clipPathStyle(css.Color{R: 0, G: 100, B: 200, A: 255}, "evenodd polygon(0 0, 100% 0, 0 100%)"),
		X:     0, Y: 0, W: 20, H: 20,
	}
	PaintFull(dst, box, NewFonts(), nil, nil)
	if c := dst.RGBAAt(2, 2); c.B < 150 {
		t.Errorf("top-left (inside the triangle) = %+v, want the background colour", c)
	}
	if c := dst.RGBAAt(17, 17); c.R != 255 || c.G != 255 || c.B != 255 {
		t.Errorf("bottom-right (outside the triangle) = %+v, want white (clipped away)", c)
	}
}
