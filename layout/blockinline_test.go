// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package layout

import (
	"testing"

	"github.com/go-webengine/engine/css"
	"github.com/go-webengine/engine/dom"
)

// TestBlockLevelChildUnderInlineAncestorGetsRealBox covers CSS 2.1 §9.2.1.1's
// anonymous-block-generation-and-promotion, using news.ycombinator.com's own
// real markup as the fixture: its upvote-triangle icon is a block-level
// <div> (a background-image icon) nested directly inside an inline <a> —
// `<a href="vote"><div class="votearrow" style="...background:url(...)...">
// </div></a>`. Before this fix, the div's own box (and therefore its
// background paint step) never existed at all: appendElementInline
// unconditionally recursed into an inline-context child's own children,
// flattening a genuinely block-level descendant into the surrounding
// text run instead of promoting it to a real sibling box.
func TestBlockLevelChildUnderInlineAncestorGetsRealBox(t *testing.T) {
	src := `<html><body style="margin:0"><a href="vote">` +
		`<div style="display:block;width:10px;height:10px;background:red"></div>` +
		`</a></body></html>`
	div := findBox(layoutHTML(t, src, 300), "div")
	if div == nil {
		t.Fatal("a block-level div nested under an inline <a> must get a real Box")
	}
	assertF(t, "div.W", div.W, 10)
	assertF(t, "div.H", div.H, 10)
}

// TestFlexContainerUnderUnstyledInlineElementGetsRealLayout covers the same
// rule for a flex container, using github.com's own real shape: an unstyled
// custom element (`<react-partial>` there — default display:inline, nothing
// styles it) wraps a real `display:flex` row. Before this fix, the flex
// container's own box never existed, so its children never went through
// flex layout at all — two adjacent links rendered glued together with no
// gap, because a margin never applies to content flattened into plain
// inline text.
func TestFlexContainerUnderUnstyledInlineElementGetsRealLayout(t *testing.T) {
	src := `<html><body style="margin:0"><my-widget>` +
		`<div style="display:flex"><a>Sign in</a><a style="margin-left:8px">Sign up</a></div>` +
		`</my-widget></body></html>`
	flexDiv := findBox(layoutHTML(t, src, 300), "div")
	if flexDiv == nil {
		t.Fatal("the flex div nested under an unstyled inline element must get a real Box")
	}
	if flexDiv.Style.Display != css.DisplayFlex {
		t.Errorf("flexDiv.Style.Display = %v, want DisplayFlex", flexDiv.Style.Display)
	}
	if len(flexDiv.Children) != 2 {
		t.Fatalf("flex container children = %d, want 2", len(flexDiv.Children))
	}
	// "Sign in" = 7 runes * 10px (fakeMeasurer) = 70; the second item starts
	// at 70 + the 8px margin-left the FIRST fix (round 24) already proved
	// gets dropped when this content is wrongly flattened into inline text.
	assertF(t, "second flex item X", flexDiv.Children[1].X, 78)
}

// TestDisplayBlockSpansUnderCodeGetSeparateLines covers the same rule for
// MULTIPLE promoted siblings in a row, using tailwindcss.com's own real
// shape: a syntax-highlighted <code> block gives each line's <span> a real
// `display:block` (via a compiled Tailwind arbitrary-variant utility) so it
// occupies its own line — <code> itself is inline by default. Before this
// fix, every line's span was flattened into ONE inline run on a single
// line instead of each getting its own line.
func TestDisplayBlockSpansUnderCodeGetSeparateLines(t *testing.T) {
	src := `<html><body style="margin:0"><code>` +
		`<span style="display:block">Line1</span>` +
		`<span style="display:block">Line2</span>` +
		`</code></body></html>`
	root := layoutHTML(t, src, 300)
	var spans []*Box
	var walk func(b *Box)
	walk = func(b *Box) {
		if b.Node != nil && b.Node.Type == dom.Element && b.Node.Tag == "span" {
			spans = append(spans, b)
		}
		for _, c := range b.Children {
			walk(c)
		}
	}
	walk(root)
	if len(spans) != 2 {
		t.Fatalf("found %d <span> boxes, want 2", len(spans))
	}
	if spans[0].Y >= spans[1].Y {
		t.Errorf("span0.Y=%v span1.Y=%v — want span1 strictly BELOW span0 (separate lines, not squashed onto one)", spans[0].Y, spans[1].Y)
	}
}

// TestBlockBreakResetsPendingWhitespace covers a collapsible-whitespace edge
// case in the fix above: text ending in a trailing space, then a promoted
// block-level element, then more text in the SAME inline wrapper. The
// resumed run after the block starts its own fresh anonymous box/line, so
// its first word must NOT carry a phantom leading space from whitespace
// pending before the block — the same reset the pre-existing "br" case
// already applies, for the identical reason (a forced break also starts a
// new line).
func TestBlockBreakResetsPendingWhitespace(t *testing.T) {
	src := `<html><body style="margin:0"><a>text ` +
		`<div style="display:block">x</div>more</a></body></html>`
	body := findBox(layoutHTML(t, src, 300), "body")
	if body == nil || len(body.Children) < 3 {
		n := -1
		if body != nil {
			n = len(body.Children)
		}
		t.Fatalf("body.Children = %d, want >= 3 (anon run, div, anon run)", n)
	}
	lastAnon := body.Children[len(body.Children)-1]
	if len(lastAnon.Lines) == 0 || len(lastAnon.Lines[0].Items) == 0 {
		t.Fatal("expected an anonymous box with items after the promoted div")
	}
	first := lastAnon.Lines[0].Items[0]
	if first.Text != "more" {
		t.Fatalf("first item after the block break = %q, want \"more\"", first.Text)
	}
	if first.SpaceBefore != 0 {
		t.Errorf("SpaceBefore = %v, want 0 (no phantom leading space carried across a promoted block)", first.SpaceBefore)
	}
}
