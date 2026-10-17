// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package dom

import (
	"strings"
	"testing"
)

func TestParseAndAttributes(t *testing.T) {
	root, err := Parse(`<!DOCTYPE html><html><head><title> Hi </title></head>` +
		`<body><p id="x" class="a b" data-k="v">text<!--c--><br></p></body></html>`)
	if err != nil {
		t.Fatal(err)
	}
	if got := Title(root); got != "Hi" {
		t.Fatalf("title = %q", got)
	}
	p := Find(root, "p")
	if p == nil {
		t.Fatal("no <p>")
	}
	if p.ID() != "x" {
		t.Errorf("id = %q", p.ID())
	}
	if cs := p.Classes(); len(cs) != 2 || cs[0] != "a" || cs[1] != "b" {
		t.Errorf("classes = %v", cs)
	}
	if v, ok := p.Attribute("data-k"); !ok || v != "v" {
		t.Errorf("data-k = %q %v", v, ok)
	}
	if _, ok := p.Attribute("missing"); ok {
		t.Error("missing attr reported present")
	}
	if p.Parent == nil || p.Parent.Tag != "body" {
		t.Errorf("parent = %v", p.Parent)
	}
	// A text node exists as the first child of <p>.
	if len(p.Children) == 0 || p.Children[0].Type != Text || p.Children[0].Text != "text" {
		t.Errorf("p children = %v", p.Children)
	}
	// The fixture's own `<!--c-->` is preserved as a real Comment node (not
	// silently dropped) between the text and the <br> — see convertChildren's
	// own doc comment for why this now matters: React's streaming-SSR
	// hydration marks Suspense boundaries with comment nodes it expects to
	// find in the DOM.
	if len(p.Children) != 3 || p.Children[1].Type != Comment || p.Children[1].Text != "c" {
		t.Errorf("p children = %v, want [text, comment(c), br]", p.Children)
	}
}

func TestTemplateContentFromParse(t *testing.T) {
	// A <template>'s children parsed from real HTML source must land in its
	// Content fragment, never in its own Children — see Node.Content's doc
	// comment. Both convertChildren (this test) and NewElement (see
	// mutate_test.go) must agree on this, since a <template> can originate
	// from either path.
	root, err := Parse(`<html><body><template><p>x</p><span>y</span></template></body></html>`)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := Find(root, "template")
	if tmpl == nil {
		t.Fatal("no <template>")
	}
	if len(tmpl.Children) != 0 {
		t.Fatalf("template's own Children = %v, want none", tmpl.Children)
	}
	if tmpl.Content == nil {
		t.Fatal("template has no Content fragment")
	}
	if len(tmpl.Content.Children) != 2 || tmpl.Content.Children[0].Tag != "p" || tmpl.Content.Children[1].Tag != "span" {
		t.Fatalf("template content = %v", tmpl.Content.Children)
	}
	// The content fragment's children are NOT part of the light-DOM parent
	// chain (their Parent is the fragment itself, not the template).
	if tmpl.Content.Children[0].Parent != tmpl.Content {
		t.Error("content child's parent is not the Content fragment")
	}
}

func TestClassesAndIDEmpty(t *testing.T) {
	n := &Node{Type: Element, Tag: "div", Attr: map[string]string{}}
	if n.Classes() != nil {
		t.Error("expected nil classes")
	}
	if n.ID() != "" {
		t.Error("expected empty id")
	}
	if _, ok := n.Attribute("x"); ok {
		t.Error("expected absent")
	}
	// nil Attr map path.
	n2 := &Node{Type: Text}
	if _, ok := n2.Attribute("x"); ok {
		t.Error("nil attr should be absent")
	}
}

func TestClassesCacheTracksAttributeMutation(t *testing.T) {
	// Classes() memoizes its split of Attr["class"] to avoid re-parsing on
	// every selector match; a script rewriting the attribute directly (there
	// is no dedicated setter to hook — see js/style.go) must still be picked
	// up on the very next call, not serve the stale split.
	n := &Node{Type: Element, Tag: "div", Attr: map[string]string{"class": "a b"}}
	if cs := n.Classes(); len(cs) != 2 || cs[0] != "a" || cs[1] != "b" {
		t.Fatalf("initial classes = %v", cs)
	}
	if cs := n.Classes(); len(cs) != 2 || cs[0] != "a" || cs[1] != "b" {
		t.Fatalf("cached classes = %v", cs)
	}
	n.Attr["class"] = "c"
	if cs := n.Classes(); len(cs) != 1 || cs[0] != "c" {
		t.Fatalf("classes after mutation = %v", cs)
	}
	n.Attr["class"] = ""
	if cs := n.Classes(); len(cs) != 0 {
		t.Fatalf("classes after clearing = %v", cs)
	}
}

func TestTitleAndFindMissing(t *testing.T) {
	root, _ := Parse(`<html><body><div></div></body></html>`)
	if Title(root) != "" {
		t.Error("expected empty title")
	}
	if Find(root, "nope") != nil {
		t.Error("expected nil for missing tag")
	}
}

// TestParseQuirksMode covers a real regression: a document with no
// <!DOCTYPE> at all (confirmed live on news.ycombinator.com, which ships
// `<html lang="en" op="news">` directly) must be flagged Quirks — real
// browsers' quirks-mode UA stylesheet resets `table{text-align:initial}`,
// without which a `<center><table>...` page shell (used only to centre the
// table as a block) inherited centered text into every cell. A document
// WITH a doctype (the common case) must not be flagged.
func TestParseQuirksMode(t *testing.T) {
	noDoctype, err := Parse(`<html><body><div></div></body></html>`)
	if err != nil {
		t.Fatal(err)
	}
	if !noDoctype.Quirks {
		t.Error("no <!DOCTYPE> at all: want Quirks=true")
	}

	withDoctype, err := Parse(`<!DOCTYPE html><html><body><div></div></body></html>`)
	if err != nil {
		t.Fatal(err)
	}
	if withDoctype.Quirks {
		t.Error("<!DOCTYPE html> present: want Quirks=false")
	}
}

func TestParseCapsTotalNodeCount(t *testing.T) {
	// See TestParseFragmentCapsTotalNodeCount (mutate_test.go) for the full
	// rationale: golang.org/x/net/html's own depth guard (512 levels) does
	// nothing against a WIDE document, and this is the top-level Parse
	// (the main document fetch) rather than ParseFragment, sharing the same
	// convertChildren budget.
	root, err := Parse(`<html><body>` + strings.Repeat("<div>x</div>", maxParsedNodes+1000) + `</body></html>`)
	if err != nil {
		t.Fatal(err)
	}
	// +1: root itself is the synthetic Document wrapper Parse creates BEFORE
	// calling convertChildren, so it is one node outside the budget
	// convertChildren's own recursion tracks (confirmed: without the +1,
	// this reported exactly maxParsedNodes+1, not a looser overshoot).
	if got := countTreeNodes([]*Node{root}); got > maxParsedNodes+1 {
		t.Errorf("Parse produced %d nodes, want <= %d (maxParsedNodes+1)", got, maxParsedNodes+1)
	}
}
