// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package paint

import (
	"testing"

	"github.com/go-webengine/engine/css"
	"github.com/go-webengine/engine/dom"
	"github.com/go-webengine/engine/layout"
)

// TestPaintInlineBackground: an inline-level element with a solid background and
// light text — the pill/label pattern (e.g. Ghost's .boat-name span, or any
// display:inline-block chip) — owns no block box, so its background must be
// painted behind its inline items. Without it, the white-on-coloured text lands
// as white-on-white and the words vanish entirely. Two words of the SAME inline
// element also paint as one continuous band (the internal space is covered).
func TestPaintInlineBackground(t *testing.T) {
	dst := white(120, 30)
	p := &dom.Node{Type: dom.Element, Tag: "p"}
	span := &dom.Node{Type: dom.Element, Tag: "span", Parent: p}
	blue := css.Color{R: 0x15, G: 0x65, B: 0xc0, A: 0xff}
	white := css.Color{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
	pStyle := &css.Style{}
	spanStyle := &css.Style{Background: blue, Color: white}
	// Two words of the same span: it1 [10,30], it2 [36,56] with a 6px internal
	// leading space, so the merged band is [10,56].
	it1 := &layout.InlineItem{Text: "AA", Style: spanStyle, Node: span, X: 10, Y: 5, Width: 20, Ascent: 12, LineHeight: 20}
	it2 := &layout.InlineItem{Text: "BB", Style: spanStyle, Node: span, X: 36, Y: 5, Width: 20, SpaceBefore: 6, Ascent: 12, LineHeight: 20}
	line := &layout.LineBox{X: 0, Y: 5, W: 120, H: 20, Items: []*layout.InlineItem{it1, it2}}
	box := &layout.Box{Node: p, Style: pStyle, X: 0, Y: 0, W: 120, H: 30, Lines: []*layout.LineBox{line}}
	PaintFull(dst, box, NewFonts(), nil, nil)

	// Background painted behind the first word's box.
	if c := dst.RGBAAt(11, 6); c.B < 150 || c.R > 80 {
		t.Errorf("inline bg behind word1 = %+v, want blue", c)
	}
	// The internal space between the two words is covered (continuous band).
	if c := dst.RGBAAt(33, 12); c.B < 150 || c.R > 80 {
		t.Errorf("inline bg over internal space = %+v, want blue", c)
	}
	// Just inside the far end of the band is blue.
	if c := dst.RGBAAt(54, 12); c.B < 150 || c.R > 80 {
		t.Errorf("inline bg at band end = %+v, want blue", c)
	}
	// Before the band (x<10) stays white — the fill is bounded to the items.
	if c := dst.RGBAAt(2, 12); c.R != 255 || c.G != 255 || c.B != 255 {
		t.Errorf("left of inline bg = %+v, want white", c)
	}
	// Above the band (y<5) stays white — the fill is bounded vertically.
	if c := dst.RGBAAt(20, 2); c.R != 255 || c.G != 255 || c.B != 255 {
		t.Errorf("above inline bg = %+v, want white", c)
	}
}

// TestPaintInlineBackgroundSkips: paintInlineBackground must NOT paint for a
// block's own direct text (which carries the block's Style pointer — the block
// already painted that background, so a second per-item fill would double-paint
// a translucent colour), nor for line-break or image items, nor for an
// inline element with no background.
func TestPaintInlineBackgroundSkips(t *testing.T) {
	dst := white(60, 20)
	p := &dom.Node{Type: dom.Element, Tag: "p"}
	pStyle := &css.Style{} // no background
	// Direct text of the block: Style == box.Style, so the inline-bg pass skips it
	// (and here the block itself has no background, so the row stays white).
	direct := &layout.InlineItem{Text: "x", Style: pStyle, Node: p, X: 2, Y: 2, Width: 10, Ascent: 10, LineHeight: 14}
	// A <br> item and an image item are skipped even if they carried a background.
	bg := &css.Style{Background: css.Color{R: 0, G: 0x80, B: 0, A: 0xff}}
	br := &layout.InlineItem{LineBreak: true, Style: bg, Node: p}
	img := &layout.InlineItem{Image: &dom.Node{Type: dom.Element, Tag: "img"}, Style: bg, Node: p, X: 20, Y: 2, Width: 10, ImgW: 10, ImgH: 10, Ascent: 10, LineHeight: 14}
	// An inline span with a transparent background contributes no fill either.
	span := &dom.Node{Type: dom.Element, Tag: "span", Parent: p}
	nobg := &layout.InlineItem{Text: "y", Style: &css.Style{}, Node: span, X: 40, Y: 2, Width: 10, Ascent: 10, LineHeight: 14}
	line := &layout.LineBox{X: 0, Y: 2, W: 60, H: 14, Items: []*layout.InlineItem{direct, br, img, nobg}}
	box := &layout.Box{Node: p, Style: pStyle, X: 0, Y: 0, W: 60, H: 20, Lines: []*layout.LineBox{line}}
	PaintFull(dst, box, NewFonts(), nil, nil)

	// No green (the skipped items' background) painted anywhere on the row.
	for _, x := range []int{5, 25, 45} {
		if c := dst.RGBAAt(x, 8); c.G > 200 && c.R < 120 && c.B < 120 {
			t.Errorf("unexpected inline bg at x=%d: %+v", x, c)
		}
	}
}

// TestPaintInlineBackgroundBlockOwnBackground: a block that DOES have a
// background — its direct text carries the block's own Style pointer. The block
// paints that background once (step 2); the inline-bg pass must skip those items
// (it.Style == box.Style) so a translucent block background is never doubled.
func TestPaintInlineBackgroundBlockOwnBackground(t *testing.T) {
	dst := white(30, 20)
	p := &dom.Node{Type: dom.Element, Tag: "p"}
	green := css.Color{R: 0, G: 0x80, B: 0, A: 0xff}
	pStyle := &css.Style{Background: green, Color: css.Color{A: 0xff}}
	direct := &layout.InlineItem{Text: "x", Style: pStyle, Node: p, X: 2, Y: 2, Width: 10, Ascent: 10, LineHeight: 14}
	line := &layout.LineBox{X: 0, Y: 2, W: 30, H: 14, Items: []*layout.InlineItem{direct}}
	box := &layout.Box{Node: p, Style: pStyle, X: 0, Y: 0, W: 30, H: 20, Lines: []*layout.LineBox{line}}
	PaintFull(dst, box, NewFonts(), nil, nil)
	// The block's own background paints (green); the inline-bg pass added nothing.
	if c := dst.RGBAAt(15, 10); c.G < 100 || c.R > 80 {
		t.Errorf("block own background = %+v, want green", c)
	}
}
