// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package layout

import "testing"

// TestCanvasIsReplacedWithOwnWidthHeightAttributes checks a <canvas> gets a
// real inline item sized from its own width/height content attributes —
// found missing while implementing canvas.getContext: appendElementInline
// (the actual inline-item builder most real pages reach, as opposed to
// contents()'s own earlier replaced-element check, which only fires when the
// REPLACED element is itself the box being laid out, not an ordinary inline
// child of one) switched on a hardcoded "img","svg" tag list instead of
// calling the shared isReplacedTag — an ordinary inline-level <canvas>, the
// overwhelming common case, never went through either check at all.
func TestCanvasIsReplacedWithOwnWidthHeightAttributes(t *testing.T) {
	box := layoutHTML(t, `<html><body><canvas width="100" height="50"></canvas></body></html>`, 1024)
	items := firstLineItems(box)
	if len(items) != 1 {
		t.Fatalf("got %d inline items, want 1", len(items))
	}
	it := items[0]
	if it.Image == nil || it.Image.Tag != "canvas" {
		t.Fatalf("item.Image = %v, want the canvas node", it.Image)
	}
	assertF(t, "item.Width", it.Width, 100)
	assertF(t, "item.Ascent", it.Ascent, 50)
}

// TestCanvasDefaultsTo300x150 is the HTML Standard's own §4.12.5 default,
// for a <canvas> with no width/height attribute at all.
func TestCanvasDefaultsTo300x150(t *testing.T) {
	box := layoutHTML(t, `<html><body><canvas></canvas></body></html>`, 1024)
	items := firstLineItems(box)
	if len(items) != 1 {
		t.Fatalf("got %d inline items, want 1", len(items))
	}
	assertF(t, "item.Width", items[0].Width, 300)
	assertF(t, "item.Ascent", items[0].Ascent, 150)
}

// TestCanvasBlockDisplayStillReplaced mirrors isReplacedTag's own doc
// comment ("whatever its display value") for a <canvas style="display:block">
// — it must still be sized as a replaced element, not laid out as an empty
// block container.
func TestCanvasBlockDisplayStillReplaced(t *testing.T) {
	box := layoutHTML(t, `<html><body><canvas width="40" height="40" style="display:block"></canvas></body></html>`, 1024)
	c := findBox(box, "canvas")
	if c == nil {
		t.Fatal("no box found for the canvas element")
	}
	if len(c.Lines) != 1 || len(c.Lines[0].Items) != 1 {
		t.Fatalf("canvas box has %d lines, want 1 line with 1 item", len(c.Lines))
	}
	assertF(t, "item.Width", c.Lines[0].Items[0].Width, 40)
}
