// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package dom

import "testing"

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

func TestTitleAndFindMissing(t *testing.T) {
	root, _ := Parse(`<html><body><div></div></body></html>`)
	if Title(root) != "" {
		t.Error("expected empty title")
	}
	if Find(root, "nope") != nil {
		t.Error("expected nil for missing tag")
	}
}
