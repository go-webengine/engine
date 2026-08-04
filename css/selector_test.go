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

func TestParseSelectorList(t *testing.T) {
	sels := ParseSelectorList("a.foo#bar, p , , div span, .x:hover, > , *")
	// Expect: a.foo#bar, p, span (key of "div span"), .x, * → 5 (empty entries dropped).
	if len(sels) != 5 {
		t.Fatalf("got %d selectors: %+v", len(sels), sels)
	}
	first := sels[0]
	if first.Tag != "a" || first.ID != "bar" || len(first.Classes) != 1 || first.Classes[0] != "foo" {
		t.Errorf("first = %+v", first)
	}
	// Descendant combinator reduced to key selector "span".
	if sels[2].Tag != "span" {
		t.Errorf("combinator key = %+v", sels[2])
	}
	// Pseudo dropped, class kept.
	if sels[3].Tag != "" || len(sels[3].Classes) != 1 || sels[3].Classes[0] != "x" {
		t.Errorf("pseudo = %+v", sels[3])
	}
}

func TestSpecificity(t *testing.T) {
	id, _ := parseSimple("#a")
	cls, _ := parseSimple(".a")
	tag, _ := parseSimple("p")
	comp, _ := parseSimple("p.a.b#c")
	if !(id.Specificity() > cls.Specificity() && cls.Specificity() > tag.Specificity()) {
		t.Errorf("ordering id=%d cls=%d tag=%d", id.Specificity(), cls.Specificity(), tag.Specificity())
	}
	if comp.Specificity() != 1*10000+2*100+1 {
		t.Errorf("compound specificity = %d", comp.Specificity())
	}
}

func TestMatches(t *testing.T) {
	n := el("a", "bar", "foo baz")
	s, _ := parseSimple("a.foo#bar")
	if !s.Matches(n) {
		t.Error("should match")
	}
	if s2, _ := parseSimple("a.missing"); s2.Matches(n) {
		t.Error("missing class should not match")
	}
	if s3, _ := parseSimple("#other"); s3.Matches(n) {
		t.Error("wrong id should not match")
	}
	if s4, _ := parseSimple("p"); s4.Matches(n) {
		t.Error("wrong tag should not match")
	}
	// Universal matches any element.
	if u, _ := parseSimple("*"); !u.Matches(n) {
		t.Error("universal should match")
	}
	// Non-element never matches.
	if s.Matches(&dom.Node{Type: dom.Text}) {
		t.Error("text node should not match")
	}
}

func TestParseSimpleEmpty(t *testing.T) {
	if _, ok := parseSimple(""); ok {
		t.Error("empty should fail")
	}
	if _, ok := parseSimple(":hover"); ok {
		t.Error("bare pseudo should fail")
	}
	if _, ok := parseCompound("   "); ok {
		t.Error("blank compound should fail")
	}
	// A trailing separator token like ".": name empty is skipped.
	s, ok := parseSimple("p.")
	if !ok || s.Tag != "p" || len(s.Classes) != 0 {
		t.Errorf("p. = %+v %v", s, ok)
	}
}
