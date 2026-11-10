// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

//go:build !webengine_nojs

package engine

import (
	"image"
	"testing"
)

// TestQuerySelectorReturnsFirstDocumentOrderMatch proves QuerySelector
// behaves exactly like a page script's own document.querySelector: the
// FIRST match in document order, not an arbitrary one, for a selector
// matching several elements.
func TestQuerySelectorReturnsFirstDocumentOrderMatch(t *testing.T) {
	live := openFixture(t, New(), `<html><body>
		<div class="item" id="a">first</div>
		<div class="item" id="b">second</div>
	</body></html>`, image.Rect(0, 0, 200, 200))

	n, ok := live.QuerySelector(".item")
	if !ok {
		t.Fatal("QuerySelector(.item): ok = false, want true")
	}
	if n.ID() != "a" {
		t.Fatalf("QuerySelector(.item).ID() = %q, want %q (document-order first match)", n.ID(), "a")
	}
}

// TestQuerySelectorNoMatch proves a selector matching nothing reports
// ok=false rather than a nil node the caller might dereference.
func TestQuerySelectorNoMatch(t *testing.T) {
	live := openFixture(t, New(), `<html><body><div id="a"></div></body></html>`, image.Rect(0, 0, 200, 200))

	if n, ok := live.QuerySelector("#nope"); ok || n != nil {
		t.Fatalf("QuerySelector(#nope) = (%v, %v), want (nil, false)", n, ok)
	}
}

// TestQuerySelectorEmptySelector proves an empty/unparseable selector is a
// clean no-match, not a panic (ParseSelectorList returns no selectors).
func TestQuerySelectorEmptySelector(t *testing.T) {
	live := openFixture(t, New(), `<html><body><div id="a"></div></body></html>`, image.Rect(0, 0, 200, 200))

	if n, ok := live.QuerySelector(""); ok || n != nil {
		t.Fatalf("QuerySelector(\"\") = (%v, %v), want (nil, false)", n, ok)
	}
}

// TestQuerySelectorAll proves every match is returned, in document order.
func TestQuerySelectorAll(t *testing.T) {
	live := openFixture(t, New(), `<html><body>
		<div class="item" id="a"></div>
		<span></span>
		<div class="item" id="b"></div>
	</body></html>`, image.Rect(0, 0, 200, 200))

	got := live.QuerySelectorAll(".item")
	if len(got) != 2 {
		t.Fatalf("QuerySelectorAll(.item) = %d elements, want 2", len(got))
	}
	if got[0].ID() != "a" || got[1].ID() != "b" {
		t.Fatalf("QuerySelectorAll(.item) order = [%q, %q], want [a, b]", got[0].ID(), got[1].ID())
	}
}
