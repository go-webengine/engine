// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package layout

import "testing"

// TestAfterContentAppearsAsATrailingInlineItem covers the confirmed real
// trigger this feature closes: pkg.go.dev's own breadcrumb separator,
// `li:not(:last-child)::after{content:">"}`. The cascade layer (css package)
// already proves Content/ContentSet resolve correctly; this is the first
// layout-level proof that the synthesised node renderedChildren injects
// (see layout/shadow.go's pseudoElementNode) actually reaches the inline
// item stream, not just that cascade computed a Style for it.
func TestAfterContentAppearsAsATrailingInlineItem(t *testing.T) {
	src := `<html><head><style>li.sep::after{content:">"}</style></head>` +
		`<body style="margin:0"><ul><li class="sep">Docs</li></ul></body></html>`
	root := layoutHTML(t, src, 300)
	li := findBox(root, "li")
	if li == nil {
		t.Fatal("no <li> box")
	}
	if findBoxWithText(li, ">") == nil {
		t.Error("the ::after separator \">\" did not reach the inline item stream")
	}
	if findBoxWithText(li, "Docs") == nil {
		t.Error("the <li>'s own real text should still be there alongside the generated content")
	}
}

// TestBeforeContentAppearsAsALeadingInlineItem is the mirror case: ::before
// prepends rather than appends, confirming renderedChildren's ordering (not
// just that SOME pseudo-element content reaches layout). Uses single
// non-space markers rather than a trailing space in the content string:
// appendWords applies the SAME whitespace-collapsing to generated content as
// to ordinary source text (a real browser would preserve a literal
// content:"..." string's whitespace exactly — a narrow, documented
// simplification here, not the thing this test is pinning).
func TestBeforeContentAppearsAsALeadingInlineItem(t *testing.T) {
	src := `<html><head><style>p::before{content:"*"}</style></head>` +
		`<body style="margin:0"><p>note</p></body></html>`
	root := layoutHTML(t, src, 300)
	p := findBox(root, "p")
	if p == nil {
		t.Fatal("no <p> box")
	}
	if len(p.Lines) == 0 || len(p.Lines[0].Items) == 0 {
		t.Fatal("expected at least one inline item on <p>'s first line")
	}
	first := p.Lines[0].Items[0]
	if first.Text != "*" {
		t.Errorf("first inline item = %q, want \"*\" (::before must come BEFORE the real text)", first.Text)
	}
}

// TestNoContentDeclarationAddsNoExtraBox is the common-case guard: an
// ordinary page with no ::before/::after content anywhere must produce the
// EXACT same inline items as before this feature existed —
// renderedChildren's own doc comment promises this; this pins it as a real
// assertion, not just prose. "plain text" is two space-separated words, so
// two items, not one — pinning THAT shape too, not assumed.
func TestNoContentDeclarationAddsNoExtraBox(t *testing.T) {
	src := `<html><body style="margin:0"><p>plain text</p></body></html>`
	root := layoutHTML(t, src, 300)
	p := findBox(root, "p")
	if p == nil {
		t.Fatal("no <p> box")
	}
	if len(p.Lines) != 1 || len(p.Lines[0].Items) != 2 {
		t.Fatalf("expected exactly one line with 2 inline items (the real text's two words), got %d lines", len(p.Lines))
	}
	if p.Lines[0].Items[0].Text != "plain" || p.Lines[0].Items[1].Text != "text" {
		t.Errorf("items = %q, %q, want \"plain\", \"text\"", p.Lines[0].Items[0].Text, p.Lines[0].Items[1].Text)
	}
}
