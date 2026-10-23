// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package layout

import "testing"

// The cases of CSS 2.1 §10.8: "The baseline of an 'inline-block' is the
// baseline of its last line box in the normal flow, unless it has either no
// in-flow line boxes or if its 'overflow' property has a computed value other
// than 'visible', in which case the baseline is the bottom margin edge."
//
// The engine used the bottom edge in every case, which raised an
// inline-block's text above its line's by its descent and half its leading
// (§10.8.1). Found by go-slides' line numbers: drawn as inline-blocks in a
// code block at line-height 1.45, they sat 4.7 px above the code at 1280x720.
func TestInlineBlockBaseline(t *testing.T) {
	// The inline-block's text, laid out in its own box, sits on the line's
	// baseline: its distance from the box's top is the item's Ascent.
	textBaseline := func(t *testing.T, src string) (ascent, inner, h float64) {
		t.Helper()
		items := firstLineItems(layoutHTML(t, src, 400))
		var ib *InlineItem
		for _, it := range items {
			if it.NestedBox != nil {
				ib = it
			}
		}
		if ib == nil {
			t.Fatalf("no inline-block among %v", texts(items))
		}
		y, ok := lastLineBaseline(ib.NestedBox)
		if !ok {
			return ib.Ascent, -1, ib.NestedBox.H
		}
		return ib.Ascent, y - ib.NestedBox.Y, ib.NestedBox.H
	}

	t.Run("last line box", func(t *testing.T) {
		// line-height 2: a leading as large as the font, so the bottom edge
		// is far below the baseline.
		a, inner, h := textBaseline(t, `<html><body style="margin:0;font-size:10px;line-height:2">x <span style="display:inline-block">y</span> z</body></html>`)
		assertF(t, "Ascent", a, inner)
		if a >= h {
			t.Errorf("Ascent %v is the bottom edge %v: the baseline is the text's", a, h)
		}
	})
	t.Run("last of several lines", func(t *testing.T) {
		a, inner, h := textBaseline(t, `<html><body style="margin:0;font-size:10px;line-height:2">x <span style="display:inline-block;width:30px">aa bb cc</span> z</body></html>`)
		assertF(t, "Ascent", a, inner)
		if inner < h/2 {
			t.Errorf("baseline %v of %v: not the LAST line's", inner, h)
		}
	})
	t.Run("in a block child", func(t *testing.T) {
		a, inner, _ := textBaseline(t, `<html><body style="margin:0;font-size:10px;line-height:2">x <span style="display:inline-block"><div>one</div><div>two</div></span></body></html>`)
		assertF(t, "Ascent", a, inner)
	})
	t.Run("a float after it is not in the flow", func(t *testing.T) {
		a, inner, h := textBaseline(t, `<html><body style="margin:0;font-size:10px;line-height:2">x <span style="display:inline-block"><div>one</div><div style="float:left;width:20px;height:50px">f</div></span></body></html>`)
		assertF(t, "Ascent", a, inner)
		if inner > 20 {
			t.Errorf("baseline %v of %v: the float's line, not the first div's", inner, h)
		}
	})
	t.Run("no line box", func(t *testing.T) {
		a, _, h := textBaseline(t, `<html><body style="margin:0;font-size:10px;line-height:2">x <span style="display:inline-block;width:20px;height:20px"></span></body></html>`)
		assertF(t, "Ascent (bottom edge)", a, h)
	})
	t.Run("overflow hidden", func(t *testing.T) {
		a, _, h := textBaseline(t, `<html><body style="margin:0;font-size:10px;line-height:2">x <span style="display:inline-block;overflow:hidden">y</span></body></html>`)
		assertF(t, "Ascent (bottom edge)", a, h)
	})
}
