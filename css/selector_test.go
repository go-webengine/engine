// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package css

import (
	"testing"

	"github.com/go-webengine/engine/dom"
)

func el(tag, id, class string) *dom.Node {
	attr := map[string]string{}
	if id != "" {
		attr["id"] = id
	}
	if class != "" {
		attr["class"] = class
	}
	return &dom.Node{Type: dom.Element, Tag: tag, Attr: attr}
}

// attach links children to a parent (with Parent back-pointers) for combinator
// tests and returns the parent.
func attach(parent *dom.Node, children ...*dom.Node) *dom.Node {
	for _, c := range children {
		c.Parent = parent
		parent.Children = append(parent.Children, c)
	}
	return parent
}

func TestParseSelectorList(t *testing.T) {
	sels := ParseSelectorList("a.foo#bar, p , , div span, .x:hover, > , *")
	// Expect: a.foo#bar, p, "div span" (2 parts), .x, * → 5 (empties dropped).
	if len(sels) != 5 {
		t.Fatalf("got %d selectors: %+v", len(sels), sels)
	}
	first := sels[0]
	key := first.parts[len(first.parts)-1]
	if key.Tag != "a" || key.ID != "bar" || len(key.Classes) != 1 || key.Classes[0] != "foo" {
		t.Errorf("first key = %+v", key)
	}
	dsp := sels[2]
	if len(dsp.parts) != 2 || dsp.parts[0].Tag != "div" || dsp.parts[1].Tag != "span" {
		t.Errorf("descendant parts = %+v", dsp.parts)
	}
	if len(dsp.combs) != 1 || dsp.combs[0] != combDescendant {
		t.Errorf("descendant comb = %+v", dsp.combs)
	}
	x := sels[3].parts[0]
	if x.Tag != "" || len(x.Classes) != 1 || x.Classes[0] != "x" {
		t.Errorf("pseudo = %+v", x)
	}
}

func TestSpecificity(t *testing.T) {
	id, _ := parseComplex("#a")
	cls, _ := parseComplex(".a")
	tag, _ := parseComplex("p")
	comp, _ := parseComplex("p.a.b#c")
	if !(id.Specificity() > cls.Specificity() && cls.Specificity() > tag.Specificity()) {
		t.Errorf("ordering id=%d cls=%d tag=%d", id.Specificity(), cls.Specificity(), tag.Specificity())
	}
	if comp.Specificity() != 1*10000+2*100+1 {
		t.Errorf("compound specificity = %d", comp.Specificity())
	}
	chain, _ := parseComplex("div.box p")
	if chain.Specificity() != 0*10000+1*100+2 {
		t.Errorf("chain specificity = %d", chain.Specificity())
	}
}

func TestMatchesSimple(t *testing.T) {
	n := el("a", "bar", "foo baz")
	s, _ := parseComplex("a.foo#bar")
	if !s.Matches(n) {
		t.Error("should match")
	}
	if s2, _ := parseComplex("a.missing"); s2.Matches(n) {
		t.Error("missing class should not match")
	}
	if s3, _ := parseComplex("#other"); s3.Matches(n) {
		t.Error("wrong id should not match")
	}
	if s4, _ := parseComplex("p"); s4.Matches(n) {
		t.Error("wrong tag should not match")
	}
	if u, _ := parseComplex("*"); !u.Matches(n) {
		t.Error("universal should match")
	}
	if s.Matches(&dom.Node{Type: dom.Text}) {
		t.Error("text node should not match")
	}
	if (Selector{}).Matches(n) {
		t.Error("empty selector should not match")
	}
}

func TestDescendantAndChild(t *testing.T) {
	// div > section > p.deep: p is a grandchild of div, a child of section.
	p := el("p", "", "deep")
	sec := attach(el("section", "", ""), p)
	attach(el("div", "", ""), sec)

	if desc, _ := parseComplex("div p"); !desc.Matches(p) {
		t.Error("descendant div p should match")
	}
	if child, _ := parseComplex("section > p"); !child.Matches(p) {
		t.Error("child section > p should match")
	}
	if notChild, _ := parseComplex("div > p"); notChild.Matches(p) {
		t.Error("div > p should NOT match (p is a grandchild)")
	}
	if classDesc, _ := parseComplex("div .deep"); !classDesc.Matches(p) {
		t.Error("div .deep should match")
	}
	if wrong, _ := parseComplex("article p"); wrong.Matches(p) {
		t.Error("article p should not match")
	}
}

func TestSiblingCombinators(t *testing.T) {
	h2 := el("h2", "", "")
	p1 := el("p", "", "")
	p2 := el("p", "", "")
	attach(el("div", "", ""), h2, p1, p2)

	adj, _ := parseComplex("h2 + p")
	if !adj.Matches(p1) {
		t.Error("h2 + p should match the immediately-following p")
	}
	if adj.Matches(p2) {
		t.Error("h2 + p should NOT match the second p")
	}
	gen, _ := parseComplex("h2 ~ p")
	if !gen.Matches(p1) || !gen.Matches(p2) {
		t.Error("h2 ~ p should match all following p siblings")
	}
	if prevElementSibling(h2) != nil {
		t.Error("first child has no previous element sibling")
	}
	// A node with no parent has no previous sibling.
	if prevElementSibling(el("p", "", "")) != nil {
		t.Error("orphan node has no previous sibling")
	}
}

func TestChainedCombinators(t *testing.T) {
	a := el("a", "", "")
	li := attach(el("li", "", ""), a)
	ul := attach(el("ul", "", ""), li)
	attach(el("nav", "", ""), ul)

	if sel, _ := parseComplex("nav > ul li a"); !sel.Matches(a) {
		t.Error("nav > ul li a should match")
	}
	if bad, _ := parseComplex("ul > a"); bad.Matches(a) {
		t.Error("ul > a should not match (parent is li)")
	}
	// Adjacent-with-no-previous: p + a where a is first child fails.
	first := el("a", "", "")
	attach(el("li", "", ""), first)
	if adj, _ := parseComplex("p + a"); adj.Matches(first) {
		t.Error("p + a should not match a first child")
	}
	if sib, _ := parseComplex("p ~ a"); sib.Matches(first) {
		t.Error("p ~ a should not match with no matching previous sibling")
	}
}

// checkbox builds an <input type=type> element, marked checked when checked.
func checkbox(id, typ string, checked bool) *dom.Node {
	attr := map[string]string{"type": typ}
	if id != "" {
		attr["id"] = id
	}
	if checked {
		attr["checked"] = ""
	}
	return &dom.Node{Type: dom.Element, Tag: "input", Attr: attr}
}

func TestCheckedPseudo(t *testing.T) {
	on := checkbox("t", "checkbox", true)
	off := checkbox("t", "checkbox", false)

	sel, ok := parseComplex("input:checked")
	if !ok {
		t.Fatal("input:checked should parse")
	}
	if !sel.Matches(on) {
		t.Error(":checked should match a checked input")
	}
	if sel.Matches(off) {
		t.Error(":checked should NOT match an unchecked input")
	}

	// A bare ":checked" is a valid, real constraint on its own.
	bare, ok := parseComplex(":checked")
	if !ok || bare.parts[0].Checked != true {
		t.Fatalf("bare :checked = %+v ok=%v", bare, ok)
	}
	if !bare.Matches(on) || bare.Matches(off) {
		t.Error("bare :checked match wrong")
	}

	// <option selected> counts as checked; a plain element never does.
	opt := &dom.Node{Type: dom.Element, Tag: "option", Attr: map[string]string{"selected": ""}}
	if !bare.Matches(opt) {
		t.Error(":checked should match <option selected>")
	}
	if bare.Matches(el("div", "", "")) {
		t.Error(":checked should not match a plain div")
	}
	// ":checked" contributes class-level specificity.
	if got := bare.Specificity(); got != 100 {
		t.Errorf(":checked specificity = %d, want 100", got)
	}
}

func TestNotPseudo(t *testing.T) {
	box := el("div", "", "box")
	other := el("div", "", "other")

	// :not(.other) matches .box but not .other.
	sel, ok := parseComplex("div:not(.other)")
	if !ok {
		t.Fatal("div:not(.other) should parse")
	}
	if !sel.Matches(box) {
		t.Error("div:not(.other) should match .box")
	}
	if sel.Matches(other) {
		t.Error("div:not(.other) should NOT match .other")
	}

	// :not(:checked) — matches an unchecked input, not a checked one.
	on := checkbox("t", "checkbox", true)
	off := checkbox("t", "checkbox", false)
	nc, ok := parseComplex("input:not(:checked)")
	if !ok {
		t.Fatal("input:not(:checked) should parse")
	}
	if !nc.Matches(off) {
		t.Error(":not(:checked) should match an unchecked input")
	}
	if nc.Matches(on) {
		t.Error(":not(:checked) should NOT match a checked input")
	}

	// :not() over a selector list: fails if ANY alternative matches.
	list, ok := parseComplex("div:not(.a, .other)")
	if !ok {
		t.Fatal("div:not(.a, .other) should parse")
	}
	if list.Matches(other) {
		t.Error("div:not(.a, .other) should exclude .other")
	}
	if !list.Matches(box) {
		t.Error("div:not(.a, .other) should keep .box")
	}

	// :not(:hover) is always true statically → no constraint; .box still matches.
	nh, ok := parseComplex(".box:not(:hover)")
	if !ok {
		t.Fatal(".box:not(:hover) should parse")
	}
	if len(nh.parts[0].Not) != 0 {
		t.Errorf(":not(:hover) should impose no constraint, got %+v", nh.parts[0].Not)
	}
	if !nh.Matches(box) {
		t.Error(".box:not(:hover) should match .box statically")
	}

	// :not() specificity picks up its argument (id here).
	spec, ok := parseComplex("div:not(#x)")
	if !ok {
		t.Fatal("div:not(#x) should parse")
	}
	if got := spec.Specificity(); got != 10000+1 { // one id + one tag
		t.Errorf("div:not(#x) specificity = %d, want %d", got, 10001)
	}
}

func TestNotUnmodelledDoesNotDropRule(t *testing.T) {
	// A :not() whose argument is empty or UNMODELLED (attribute-only, a
	// pseudo-element) must NOT drop the rule — it degrades to "no constraint" so
	// the compound keeps matching its base. This is the regression that would
	// otherwise disable dark themes gated on `:root:not([data-theme])`.
	div := el("div", "", "")
	for _, s := range []string{"div:not()", "div:not(   )", `div:not([data-x])`, "div:not(::before)"} {
		sel, ok := parseComplex(s)
		if !ok {
			t.Errorf("parseComplex(%q) should parse (unmodelled :not = no constraint)", s)
			continue
		}
		if len(sel.parts[0].Not) != 0 {
			t.Errorf("%q: unmodelled :not should impose no constraint, got %+v", s, sel.parts[0].Not)
		}
		if !sel.Matches(div) {
			t.Errorf("%q should still match a plain div", s)
		}
	}

	// The dark-theme shape that regressed go.dev/pkg.go.dev: `:root:not([attr])`
	// must still select the document root (so its dark custom properties apply).
	root := el("html", "", "")
	sel, ok := parseComplex(`:root:not([data-theme])`)
	if !ok || !sel.Matches(root) {
		t.Errorf(":root:not([data-theme]) should match the root element; ok=%v", ok)
	}

	// A comma-separated list is always fully preserved (nothing dropped).
	sels := ParseSelectorList("div:not(), .ok")
	if len(sels) != 2 {
		t.Fatalf("got %d selectors, want 2: %+v", len(sels), sels)
	}
	if !sels[1].Matches(el("span", "", "ok")) {
		t.Error(".ok should match")
	}
}

func TestSplitPseudos(t *testing.T) {
	// A colon inside an attribute value must not split the pseudo list.
	base, ps := splitPseudos(`a[href="ht:tp"]:hover`)
	if base != `a[href="ht:tp"]` || len(ps) != 1 || ps[0] != "hover" {
		t.Errorf("splitPseudos attr-colon = %q %v", base, ps)
	}
	// Nested pseudo argument stays a single token.
	base, ps = splitPseudos(":not(:checked)")
	if base != "" || len(ps) != 1 || ps[0] != "not(:checked)" {
		t.Errorf("splitPseudos nested = %q %v", base, ps)
	}
	// Multiple chained pseudos.
	_, ps = splitPseudos("input:focus:checked")
	if len(ps) != 2 || ps[0] != "focus" || ps[1] != "checked" {
		t.Errorf("splitPseudos chained = %v", ps)
	}
	// No pseudo.
	base, ps = splitPseudos("div.box")
	if base != "div.box" || ps != nil {
		t.Errorf("splitPseudos none = %q %v", base, ps)
	}
	// An escaped colon in the base is not a pseudo boundary.
	base, ps = splitPseudos(`a\:b:checked`)
	if base != `a\:b` || len(ps) != 1 || ps[0] != "checked" {
		t.Errorf("splitPseudos escaped-colon = %q %v", base, ps)
	}
	// A bracketed argument nested inside :not() tokenizes as one compound
	// (exercises the depth-tracking of nested [] and () in tokenizeSelector); the
	// modelled tag prefix survives while the unmodelled attribute is dropped.
	if sel, ok := parseComplex("a:not(b[c])"); !ok || len(sel.parts) != 1 ||
		len(sel.parts[0].Not) != 1 || sel.parts[0].Not[0].Tag != "b" {
		t.Errorf("a:not(b[c]) = %+v ok=%v", sel, ok)
	}
	// An attribute-ONLY :not() argument is unmodelled, so it imposes no
	// constraint — the compound keeps its modelled base ("input") and still
	// parses (the rule is NOT dropped). See TestNotUnmodelledDoesNotDropRule.
	if sel, ok := parseComplex(`input:not([type="x"])`); !ok ||
		len(sel.parts[0].Not) != 0 || sel.parts[0].Tag != "input" {
		t.Errorf(`input:not([type="x"]) = %+v ok=%v`, sel, ok)
	}
	// pseudoNameArg forms.
	if n, a := pseudoNameArg("not(:checked)"); n != "not" || a != ":checked" {
		t.Errorf("pseudoNameArg func = %q %q", n, a)
	}
	if n, a := pseudoNameArg("checked"); n != "checked" || a != "" {
		t.Errorf("pseudoNameArg plain = %q %q", n, a)
	}
}

// TestCheckboxHack is the crux: a hidden-by-default menu revealed only when the
// toggle is :checked. With no user interaction the toggle is unchecked, so the
// reveal rule must NOT apply and the menu stays hidden — Chrome's static state.
func TestCheckboxHack(t *testing.T) {
	hide, _ := parseComplex(".menu")                      // display:none base rule
	reveal, _ := parseComplex("#toggle:checked ~ .menu")  // reveal when checked

	toggle := checkbox("toggle", "checkbox", false)
	menu := el("div", "", "menu")
	attach(el("div", "", ""), toggle, menu)

	if !hide.Matches(menu) {
		t.Fatal(".menu base rule should match the menu")
	}
	if reveal.Matches(menu) {
		t.Error("with an UNCHECKED toggle, the reveal rule must NOT apply (menu stays hidden)")
	}

	// Now mark the toggle checked: the reveal rule applies (menu shown).
	toggleOn := checkbox("toggle", "checkbox", true)
	menu2 := el("div", "", "menu")
	attach(el("div", "", ""), toggleOn, menu2)
	if !reveal.Matches(menu2) {
		t.Error("with a CHECKED toggle, the reveal rule should apply")
	}

	// The inverse MediaWiki form: hide the container while the checkbox is not
	// checked. Unchecked → hidden; checked → the hide rule stops matching.
	hideWhileUnchecked, _ := parseComplex("#toggle:not(:checked) ~ .menu")
	if !hideWhileUnchecked.Matches(menu) {
		t.Error(":not(:checked) ~ .menu should hide the menu of an unchecked toggle")
	}
	if hideWhileUnchecked.Matches(menu2) {
		t.Error(":not(:checked) ~ .menu should stop matching once the toggle is checked")
	}
}

func TestParseComplexEdgeCases(t *testing.T) {
	for _, bad := range []string{"", "   ", "> p", "p >", "p > > a", "::before"} {
		if _, ok := parseComplex(bad); ok {
			t.Errorf("parseComplex(%q) should fail", bad)
		}
	}
	// A bare dynamic pseudo now parses to a never-matching compound (equivalent
	// net effect to being dropped: it applies to nothing in a static render),
	// which is what lets ":not(:hover)" resolve to "no constraint".
	if sel, ok := parseComplex(":hover"); !ok {
		t.Error("parseComplex(:hover) should parse")
	} else if sel.Matches(el("div", "", "")) {
		t.Error(":hover must never match statically")
	}
	c, ok := parseSimple("p.")
	if !ok || c.Tag != "p" || len(c.Classes) != 0 {
		t.Errorf("p. = %+v %v", c, ok)
	}
	attr, ok := parseComplex("input[type=text]")
	if !ok || attr.parts[0].Tag != "input" {
		t.Errorf("attr selector = %+v %v", attr, ok)
	}
	if _, ok := parseSimple(""); ok {
		t.Error("empty simple should fail")
	}
	if _, ok := parseSimple("[x]"); ok {
		t.Error("bare attribute compound should fail")
	}
}

// TestPseudoElementMatchesNothing verifies that a compound carrying a pseudo-
// ELEMENT (::before/::after/… and their CSS2 single-colon spellings) matches no
// real element: the rule targets a generated box the engine does not synthesise,
// so its declarations must NOT be applied to the originating element. This is the
// fix for clearfix idioms like `.wrap::after{height:0;overflow:hidden}` wrongly
// collapsing the real `.wrap` element.
func TestPseudoElementMatchesNothing(t *testing.T) {
	wrap := el("div", "", "wrap")
	// Both the double-colon and legacy single-colon spellings, and a bare form.
	for _, sel := range []string{".wrap::after", ".wrap:after", ".wrap::before", "div::first-line", "p::marker"} {
		s, ok := parseComplex(sel)
		if !ok {
			t.Fatalf("parseComplex(%q) should parse (keeping the rule, matching nothing)", sel)
		}
		if !s.parts[len(s.parts)-1].PseudoElement {
			t.Errorf("%q: key compound should be flagged PseudoElement", sel)
		}
		if s.Matches(wrap) && sel == ".wrap::after" {
			t.Errorf("%q must not match the real .wrap element", sel)
		}
	}
	// A pseudo-CLASS on the same base still matches (only pseudo-ELEMENTS are
	// suppressed): guards against over-broadening the suppression.
	if s, _ := parseComplex(".wrap:first-child"); !s.Matches(wrap) {
		t.Error(".wrap:first-child (pseudo-class, unmodelled) should still match its base")
	}
	// isPseudoElement direct: a name that is not a pseudo-element returns false.
	if isPseudoElement("hover") || isPseudoElement("nth-child") {
		t.Error("pseudo-classes must not be classified as pseudo-elements")
	}
	if !isPseudoElement("after") || !isPseudoElement("-webkit-scrollbar") {
		t.Error("pseudo-elements must be classified as such")
	}
}
