// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package dom

import (
	"sort"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// NewElement creates a detached element node with the given (lowercased) tag and
// an empty attribute map. It is used by the JS binding's document.createElement.
func NewElement(tag string) *Node {
	return &Node{Type: Element, Tag: strings.ToLower(tag), Attr: map[string]string{}}
}

// NewText creates a detached text node with the given character data.
func NewText(text string) *Node {
	return &Node{Type: Text, Text: text}
}

// AppendChild appends child to parent's child list, first detaching child from
// any current parent. Nil operands and self-appends are no-ops.
func AppendChild(parent, child *Node) {
	if parent == nil || child == nil || parent == child {
		return
	}
	detach(child)
	child.Parent = parent
	parent.Children = append(parent.Children, child)
}

// RemoveChild removes child from parent's child list, clearing its Parent. It is
// a no-op if child is not a direct child of parent.
func RemoveChild(parent, child *Node) {
	if parent == nil || child == nil {
		return
	}
	for i, c := range parent.Children {
		if c == child {
			parent.Children = append(parent.Children[:i:i], parent.Children[i+1:]...)
			child.Parent = nil
			return
		}
	}
}

// InsertBefore inserts newChild immediately before ref among parent's children,
// detaching newChild from any current parent first. If ref is nil or not a child
// of parent, newChild is appended.
func InsertBefore(parent, newChild, ref *Node) {
	if parent == nil || newChild == nil || parent == newChild {
		return
	}
	detach(newChild)
	newChild.Parent = parent
	if ref != nil {
		for i, c := range parent.Children {
			if c == ref {
				tail := append([]*Node{newChild}, parent.Children[i:]...)
				parent.Children = append(parent.Children[:i:i], tail...)
				return
			}
		}
	}
	parent.Children = append(parent.Children, newChild)
}

// detach removes n from its current parent, if any.
func detach(n *Node) {
	if n.Parent != nil {
		RemoveChild(n.Parent, n)
	}
}

// TextContent returns the concatenation of every descendant text node's data,
// in document order (the DOM textContent getter).
func TextContent(n *Node) string {
	var sb strings.Builder
	var walk func(*Node)
	walk = func(x *Node) {
		if x.Type == Text {
			sb.WriteString(x.Text)
		}
		for _, c := range x.Children {
			walk(c)
		}
	}
	walk(n)
	return sb.String()
}

// SetTextContent replaces all of n's children with a single text node (or none,
// for empty text), matching the DOM textContent setter.
func SetTextContent(n *Node, text string) {
	clearChildren(n)
	if text != "" {
		AppendChild(n, NewText(text))
	}
}

// clearChildren detaches and drops all of n's children.
func clearChildren(n *Node) {
	for _, c := range n.Children {
		c.Parent = nil
	}
	n.Children = nil
}

// SetInnerHTML parses htmlSrc as an HTML fragment and replaces n's children with
// the result (the DOM innerHTML setter). On a parse error the children are left
// unchanged and the error is returned.
func SetInnerHTML(n *Node, htmlSrc string) error {
	nodes, err := ParseFragment(htmlSrc)
	if err != nil {
		return err
	}
	clearChildren(n)
	for _, c := range nodes {
		AppendChild(n, c)
	}
	return nil
}

// InnerHTML serializes n's children back to HTML (the DOM innerHTML getter).
// Attribute order is normalised (sorted) so the output is deterministic.
func InnerHTML(n *Node) string {
	var sb strings.Builder
	for _, c := range n.Children {
		serialize(&sb, c)
	}
	return sb.String()
}

// OuterHTML serializes n itself (element and its subtree) back to HTML.
func OuterHTML(n *Node) string {
	var sb strings.Builder
	serialize(&sb, n)
	return sb.String()
}

// voidElements are HTML elements with no closing tag / no children.
var voidElements = map[string]bool{
	"area": true, "base": true, "br": true, "col": true, "embed": true,
	"hr": true, "img": true, "input": true, "link": true, "meta": true,
	"param": true, "source": true, "track": true, "wbr": true,
}

// serialize writes an HTML serialization of n into sb.
func serialize(sb *strings.Builder, n *Node) {
	switch n.Type {
	case Text:
		sb.WriteString(html.EscapeString(n.Text))
	case Element:
		sb.WriteByte('<')
		sb.WriteString(n.Tag)
		keys := make([]string, 0, len(n.Attr))
		for k := range n.Attr {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			sb.WriteByte(' ')
			sb.WriteString(k)
			sb.WriteString(`="`)
			sb.WriteString(html.EscapeString(n.Attr[k]))
			sb.WriteByte('"')
		}
		sb.WriteByte('>')
		if voidElements[n.Tag] {
			return
		}
		for _, c := range n.Children {
			serialize(sb, c)
		}
		sb.WriteString("</")
		sb.WriteString(n.Tag)
		sb.WriteByte('>')
	}
}

// ParseFragment parses an HTML fragment (as if it were the innerHTML of a
// <body>) into a list of detached owned nodes.
func ParseFragment(htmlSrc string) ([]*Node, error) {
	ctx := &html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body}
	parsed, err := html.ParseFragment(strings.NewReader(htmlSrc), ctx)
	if err != nil {
		return nil, err
	}
	container := &html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body}
	for _, p := range parsed {
		container.AppendChild(p)
	}
	holder := &Node{Type: Element, Tag: "body", Attr: map[string]string{}}
	convertChildren(container, holder)
	for _, c := range holder.Children {
		c.Parent = nil
	}
	return holder.Children, nil
}
