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

func TestParseComplexEdgeCases(t *testing.T) {
	for _, bad := range []string{"", "   ", "> p", "p >", "p > > a", ":hover"} {
		if _, ok := parseComplex(bad); ok {
			t.Errorf("parseComplex(%q) should fail", bad)
		}
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
