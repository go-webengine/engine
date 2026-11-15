// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package layout

import "testing"

// TestIframeIsReplacedWithOwnWidthHeightAttributes mirrors <canvas>'s own
// test: an <iframe> is sized from its own width/height content attributes,
// via the SAME isReplacedTag/appendElementInline path.
func TestIframeIsReplacedWithOwnWidthHeightAttributes(t *testing.T) {
	box := layoutHTML(t, `<html><body><iframe width="200" height="100"></iframe></body></html>`, 1024)
	items := firstLineItems(box)
	if len(items) != 1 {
		t.Fatalf("got %d inline items, want 1", len(items))
	}
	it := items[0]
	if it.Image == nil || it.Image.Tag != "iframe" {
		t.Fatalf("item.Image = %v, want the iframe node", it.Image)
	}
	assertF(t, "item.Width", it.Width, 200)
	assertF(t, "item.Ascent", it.Ascent, 100)
}

// TestIframeDefaultsTo300x150 is the HTML Standard's own §4.8.5 default,
// for an <iframe> with no width/height attribute at all — the same default
// <canvas> has.
func TestIframeDefaultsTo300x150(t *testing.T) {
	box := layoutHTML(t, `<html><body><iframe></iframe></body></html>`, 1024)
	items := firstLineItems(box)
	if len(items) != 1 {
		t.Fatalf("got %d inline items, want 1", len(items))
	}
	assertF(t, "item.Width", items[0].Width, 300)
	assertF(t, "item.Ascent", items[0].Ascent, 150)
}
