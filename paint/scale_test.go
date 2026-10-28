// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package paint

import (
	"testing"

	"github.com/go-webengine/engine/css"
	"github.com/go-webengine/engine/dom"
	"github.com/go-webengine/engine/layout"
)

// TestScaleShrinksTowardsCentre covers the real confirmed trigger (round
// 188): vuejs.org's own compiled stylesheet sets a plain, static
// `transform:scale(.8)` on its theme-toggle switch. A 100×60 box split
// left-half red / right-half blue, scaled by 0.5 (exaggerated from the real
// 0.8 for an unambiguous pixel test), must come out 50×30, still centred on
// the original box's own centre (70,50) — the default, and only modelled,
// transform-origin.
func TestScaleShrinksTowardsCentre(t *testing.T) {
	dst := white(140, 140)
	root := &layout.Box{
		Node: &dom.Node{Type: dom.Element, Tag: "div"}, Style: &css.Style{},
		X: 0, Y: 0, W: 140, H: 140,
		Children: []*layout.Box{{
			Node: &dom.Node{Type: dom.Element, Tag: "div"}, Style: &css.Style{HasScale: true, ScaleX: 0.5, ScaleY: 0.5},
			X: 20, Y: 20, W: 100, H: 60,
			Children: []*layout.Box{
				{Node: &dom.Node{Type: dom.Element, Tag: "div"}, Style: &css.Style{Background: css.Color{R: 255, A: 255}}, X: 20, Y: 20, W: 50, H: 60},
				{Node: &dom.Node{Type: dom.Element, Tag: "div"}, Style: &css.Style{Background: css.Color{B: 255, A: 255}}, X: 70, Y: 20, W: 50, H: 60},
			},
		}},
	}
	PaintFull(dst, root, NewFonts(), nil, nil)
	// Scaled bounds: 50 wide x 30 tall, centred on (70,50) -> spans
	// X:[45,95) Y:[35,65). Sampled well inside the left (red) and right
	// (blue) halves.
	left := dst.RGBAAt(55, 50)
	right := dst.RGBAAt(85, 50)
	if left.R < 200 || left.B > 50 {
		t.Errorf("left half of scaled box = %+v, want red", left)
	}
	if right.B < 200 || right.R > 50 {
		t.Errorf("right half of scaled box = %+v, want blue", right)
	}
	// A point well inside the ORIGINAL (unscaled) box's own red half, but
	// OUTSIDE the scaled box's new, smaller bounds — proving a real pixel
	// shrink happened, not merely a no-op leaving the full-size box in place.
	outside := dst.RGBAAt(25, 50)
	if outside.R < 250 || outside.G < 250 || outside.B < 250 {
		t.Errorf("outside the scaled box (still inside the ORIGINAL box) = %+v, want the white background, proving the box actually shrank", outside)
	}
}

// TestScaleGrowsFromCentre covers the opposite direction: a factor above 1
// enlarges the box, still centred on the same point.
func TestScaleGrowsFromCentre(t *testing.T) {
	dst := white(100, 100)
	root := &layout.Box{
		Node: &dom.Node{Type: dom.Element, Tag: "div"}, Style: &css.Style{},
		X: 0, Y: 0, W: 100, H: 100,
		Children: []*layout.Box{{
			Node:  &dom.Node{Type: dom.Element, Tag: "div"},
			Style: &css.Style{HasScale: true, ScaleX: 2, ScaleY: 2, Background: css.Color{R: 255, A: 255}},
			X:     40, Y: 40, W: 20, H: 20,
		}},
	}
	PaintFull(dst, root, NewFonts(), nil, nil)
	// Original box: X:[40,60) Y:[40,60), centre (50,50). Scaled 2x: 40x40,
	// centred on (50,50) -> spans X:[30,70) Y:[30,70). A point just inside
	// that grown edge (e.g. 32,50), OUTSIDE the original box, must now be
	// covered.
	grown := dst.RGBAAt(32, 50)
	if grown.R < 200 || grown.G > 50 {
		t.Errorf("grown edge = %+v, want red (proving the box actually grew beyond its original bounds)", grown)
	}
}

// TestScaleOneIsUnaffected confirms HasScale's own "not set" disambiguation
// (ScaleX=ScaleY=1, or HasScale=false) takes the ordinary, non-scaled paint
// path — no offscreen buffer, no visible difference from a plain box.
func TestScaleOneIsUnaffected(t *testing.T) {
	dst := white(60, 60)
	root := &layout.Box{
		Node: &dom.Node{Type: dom.Element, Tag: "div"}, Style: &css.Style{},
		X: 0, Y: 0, W: 60, H: 60,
		Children: []*layout.Box{
			{Node: &dom.Node{Type: dom.Element, Tag: "div"}, Style: &css.Style{HasScale: true, ScaleX: 1, ScaleY: 1, Background: css.Color{R: 255, A: 255}}, X: 10, Y: 10, W: 40, H: 40},
		},
	}
	PaintFull(dst, root, NewFonts(), nil, nil)
	if c := dst.RGBAAt(30, 30); c.R < 250 || c.G > 5 || c.B > 5 {
		t.Errorf("centre of a scale(1) red box = %+v, want plain solid red", c)
	}
}

// TestScaleOutsideClipIsANoOp mirrors TestRotateOutsideClipIsANoOp's
// identical guard for this offscreen-buffer feature.
func TestScaleOutsideClipIsANoOp(t *testing.T) {
	dst := solid(20, 20, css.Color{R: 255, A: 255})
	box := &layout.Box{
		Node:  &dom.Node{Type: dom.Element, Tag: "div"},
		Style: &css.Style{HasScale: true, ScaleX: 0.5, ScaleY: 0.5, Background: css.Color{B: 255, A: 255}},
		X:     100, Y: 100, W: 50, H: 50,
	}
	paintBox(dst, newTestPainter(dst), box, NewFonts(), nil, nil, dst.Rect)
	if c := dst.RGBAAt(10, 10); c.R != 255 || c.G != 0 || c.B != 0 {
		t.Errorf("dst = %+v, want untouched solid red (box entirely outside the canvas)", c)
	}
}

// TestScaleDoesNotCombineWithOpacity mirrors
// TestRotateDoesNotCombineWithOpacity's identical disclosed scope limit.
func TestScaleDoesNotCombineWithOpacity(t *testing.T) {
	dst := white(100, 100)
	root := &layout.Box{
		Node: &dom.Node{Type: dom.Element, Tag: "div"}, Style: &css.Style{},
		X: 0, Y: 0, W: 100, H: 100,
		Children: []*layout.Box{{
			Node:  &dom.Node{Type: dom.Element, Tag: "div"},
			Style: &css.Style{HasScale: true, ScaleX: 0.5, ScaleY: 0.5, HasOpacity: true, Opacity: 0.5, Background: css.Color{R: 255, A: 255}},
			X:     20, Y: 20, W: 60, H: 60,
		}},
	}
	PaintFull(dst, root, NewFonts(), nil, nil)
	c := dst.RGBAAt(50, 50)
	if c.G < 90 || c.G > 165 || c.B < 90 || c.B > 165 {
		t.Errorf("centre = %+v, want G/B around 127 (a real ~50%% opacity blend, proving the opacity group pass ran)", c)
	}
	// A point near the box's own original edge, which a real scale-down
	// would have left uncovered, stays covered — proving no scaling was
	// applied alongside opacity.
	edge := dst.RGBAAt(22, 22)
	if edge.R < 90 {
		t.Errorf("box edge = %+v, want still covered by the (unscaled) box's own full size", edge)
	}
}

// TestScaleZeroCollapsesWithoutCrashing covers the degenerate `scale(0)`
// case: go-images' Resize is never asked for a non-positive dimension
// (clamped to a minimum of 1px), so the render completes rather than
// erroring or crashing — a real browser's own `scale(0)` makes an element
// effectively invisible, which a 1x1 pixel is visually indistinguishable
// from at any normal viewport.
func TestScaleZeroCollapsesWithoutCrashing(t *testing.T) {
	dst := white(60, 60)
	root := &layout.Box{
		Node: &dom.Node{Type: dom.Element, Tag: "div"}, Style: &css.Style{},
		X: 0, Y: 0, W: 60, H: 60,
		Children: []*layout.Box{
			{Node: &dom.Node{Type: dom.Element, Tag: "div"}, Style: &css.Style{HasScale: true, ScaleX: 0, ScaleY: 0, Background: css.Color{R: 255, A: 255}}, X: 10, Y: 10, W: 40, H: 40},
		},
	}
	PaintFull(dst, root, NewFonts(), nil, nil) // must not panic
	if c := dst.RGBAAt(5, 5); c.R < 250 || c.G < 250 || c.B < 250 {
		t.Errorf("corner far from the collapsed box = %+v, want the white background untouched", c)
	}
}

// TestScaleNonUniformAxes covers sx != sy (scale(2, 0.5)-shaped): the two
// axes are resized independently, not forced to a single uniform factor.
func TestScaleNonUniformAxes(t *testing.T) {
	dst := white(140, 140)
	root := &layout.Box{
		Node: &dom.Node{Type: dom.Element, Tag: "div"}, Style: &css.Style{},
		X: 0, Y: 0, W: 140, H: 140,
		Children: []*layout.Box{{
			Node:  &dom.Node{Type: dom.Element, Tag: "div"},
			Style: &css.Style{HasScale: true, ScaleX: 2, ScaleY: 0.5, Background: css.Color{R: 255, A: 255}},
			X:     60, Y: 55, W: 20, H: 30,
		}},
	}
	PaintFull(dst, root, NewFonts(), nil, nil)
	// Original box: X:[60,80) Y:[55,85), centre (70,70). Scaled (2x, 0.5x):
	// 40 wide x 15 tall, centred on (70,70) -> spans X:[50,90) Y:[62,78).
	// Wide axis: a point beyond the ORIGINAL box's own width but within the
	// doubled width must be covered by red (G/B near 0 — R alone can't
	// discriminate red from white, both have R=255).
	wide := dst.RGBAAt(55, 70)
	if wide.G > 50 {
		t.Errorf("widened axis at (55,70) = %+v, want red (X doubled beyond the original 60..80 span)", wide)
	}
	// Narrow axis: a point still within the ORIGINAL box's own height but
	// beyond the halved height must now be the (white, G=255) background.
	narrow := dst.RGBAAt(70, 60)
	if narrow.G < 200 {
		t.Errorf("narrowed axis at (70,60) = %+v, want white background (Y halved below the original 55..85 span)", narrow)
	}
}
