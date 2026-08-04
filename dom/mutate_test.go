// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package dom

import "testing"

func TestNewElementAndText(t *testing.T) {
	el := NewElement("DIV")
	if el.Type != Element || el.Tag != "div" || el.Attr == nil {
		t.Fatalf("NewElement: %+v", el)
	}
	tx := NewText("hi")
	if tx.Type != Text || tx.Text != "hi" {
		t.Fatalf("NewText: %+v", tx)
	}
}

func TestAppendChildDetaches(t *testing.T) {
	p1 := NewElement("div")
	p2 := NewElement("section")
	c := NewElement("span")
	AppendChild(p1, c)
	if c.Parent != p1 || len(p1.Children) != 1 {
		t.Fatal("append to p1 failed")
	}
	// Re-appending to p2 detaches from p1.
	AppendChild(p2, c)
	if c.Parent != p2 || len(p2.Children) != 1 || len(p1.Children) != 0 {
		t.Fatalf("re-append did not detach: p1=%d p2=%d", len(p1.Children), len(p2.Children))
	}
	// No-ops.
	AppendChild(nil, c)
	AppendChild(p1, nil)
	AppendChild(p1, p1)
}

func TestRemoveChild(t *testing.T) {
	p := NewElement("div")
	a, b := NewElement("a"), NewElement("b")
	AppendChild(p, a)
	AppendChild(p, b)
	RemoveChild(p, a)
	if a.Parent != nil || len(p.Children) != 1 || p.Children[0] != b {
		t.Fatal("remove a failed")
	}
	// Removing a non-child and nils are no-ops.
	RemoveChild(p, a)
	RemoveChild(nil, b)
	RemoveChild(p, nil)
}

func TestInsertBefore(t *testing.T) {
	p := NewElement("div")
	a, b, c := NewElement("a"), NewElement("b"), NewElement("c")
	AppendChild(p, a)
	AppendChild(p, b)
	InsertBefore(p, c, b) // a, c, b
	if len(p.Children) != 3 || p.Children[1] != c {
		t.Fatalf("insertBefore order wrong: %v", tags(p))
	}
	// ref nil -> append.
	d := NewElement("d")
	InsertBefore(p, d, nil)
	if p.Children[len(p.Children)-1] != d {
		t.Fatal("insertBefore nil ref should append")
	}
	// ref not a child -> append.
	e := NewElement("e")
	InsertBefore(p, e, NewElement("x"))
	if p.Children[len(p.Children)-1] != e {
		t.Fatal("insertBefore unknown ref should append")
	}
	// Detaches from old parent.
	other := NewElement("o")
	AppendChild(other, a)
	InsertBefore(p, a, d)
	if a.Parent != p || len(other.Children) != 0 {
		t.Fatal("insertBefore did not detach")
	}
	// No-ops.
	InsertBefore(nil, a, nil)
	InsertBefore(p, nil, nil)
	InsertBefore(p, p, nil)
}

func TestTextContentAndSet(t *testing.T) {
	root, _ := Parse(`<div>a<span>b<em>c</em></span>d</div>`)
	div := Find(root, "div")
	if got := TextContent(div); got != "abcd" {
		t.Fatalf("TextContent=%q", got)
	}
	SetTextContent(div, "zzz")
	if len(div.Children) != 1 || div.Children[0].Type != Text || div.Children[0].Text != "zzz" {
		t.Fatalf("SetTextContent: %v", div.Children)
	}
	SetTextContent(div, "")
	if len(div.Children) != 0 {
		t.Fatal("SetTextContent empty should clear children")
	}
}

func TestInnerAndOuterHTML(t *testing.T) {
	el := NewElement("div")
	el.Attr["id"] = "x"
	el.Attr["class"] = "y"
	AppendChild(el, NewText("a & b"))
	child := NewElement("br")
	AppendChild(el, child)
	if got := InnerHTML(el); got != "a &amp; b<br>" {
		t.Fatalf("InnerHTML=%q", got)
	}
	// Attribute order is sorted (class before id).
	if got := OuterHTML(el); got != `<div class="y" id="x">a &amp; b<br></div>` {
		t.Fatalf("OuterHTML=%q", got)
	}
}

func TestSetInnerHTML(t *testing.T) {
	el := NewElement("div")
	AppendChild(el, NewText("old"))
	if err := SetInnerHTML(el, `<p class="c">hi</p><span>x</span>`); err != nil {
		t.Fatal(err)
	}
	if len(el.Children) != 2 || el.Children[0].Tag != "p" || el.Children[0].Parent != el {
		t.Fatalf("SetInnerHTML children: %v", tags(el))
	}
	if TextContent(el) != "hix" {
		t.Fatalf("text after SetInnerHTML=%q", TextContent(el))
	}
}

func TestParseFragment(t *testing.T) {
	nodes, err := ParseFragment(`<li>1</li><li>2</li>text`)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 3 {
		t.Fatalf("want 3 fragment nodes, got %d", len(nodes))
	}
	for _, n := range nodes {
		if n.Parent != nil {
			t.Fatal("fragment nodes must be detached")
		}
	}
}

func TestSerializeTextNode(t *testing.T) {
	// A bare text node serializes (and escapes) directly.
	if got := OuterHTML(NewText("<b>")); got != "&lt;b&gt;" {
		t.Fatalf("text serialize=%q", got)
	}
}

func tags(n *Node) []string {
	var out []string
	for _, c := range n.Children {
		if c.Type == Element {
			out = append(out, c.Tag)
		} else {
			out = append(out, "#text:"+c.Text)
		}
	}
	return out
}
