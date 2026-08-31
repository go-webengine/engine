// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

// Package dom is a small, owned DOM node tree built from golang.org/x/net/html.
//
// The engine deliberately owns its node type rather than passing x/net/html
// nodes around: it keeps the rest of the engine (css, layout, paint)
// independent of the parser and gives a stable, minimal surface — element tag,
// attributes, text — that is trivial to construct in tests.
package dom

import (
	"strings"

	"golang.org/x/net/html"
)

// NodeType enumerates the kinds of node the engine cares about.
type NodeType uint8

const (
	// Document is the synthetic root of a parsed tree.
	Document NodeType = iota
	// Element is a tag node (Tag/Attr populated).
	Element
	// Text is a run of character data (Text populated).
	Text
)

// Node is a single DOM node. Elements carry a lowercased Tag and an attribute
// map; text nodes carry Text. Children are in document order.
type Node struct {
	Type     NodeType
	Tag      string            // lowercased tag name (Element only)
	Attr     map[string]string // lowercased attribute names (Element only)
	Text     string            // character data (Text only)
	Parent   *Node
	Children []*Node

	// Shadow is the shadow root attached to this element (a declarative
	// <template shadowrootmode> hoisted out at parse time — see
	// attachDeclarativeShadowRoots), or nil for a plain element. When set,
	// cascade and layout render Shadow.Children in place of this element's
	// own Children — the light-DOM Children above are NOT rendered directly;
	// they are visible only where a <slot> inside the shadow tree projects
	// them (see layout.renderedChildren / assignedSlotNodes).
	Shadow *ShadowRoot
	// ShadowHost is set on every node belonging to a shadow tree (recursively,
	// via markShadowHost) to that tree's host element; nil for an ordinary
	// light-DOM node. A <slot> element uses its own ShadowHost to find the
	// host whose light-DOM children it may project.
	ShadowHost *Node

	// classCache/classCacheRaw memoize Classes()'s split of Attr["class"],
	// keyed by the exact string it was split from — a scripted className/
	// classList write goes through Attr["class"] directly with no dedicated
	// setter to hook, so the cache self-invalidates by comparing the raw
	// string on every call instead of relying on an explicit invalidation
	// call site. Selector matching calls Classes() on every candidate rule
	// for every element (O(rules x elements) on a class-heavy page), so a
	// class list stayed a top CPU cost (strings.Fields, repeated per call)
	// until this was measured with pprof against tailwindcss.com.
	classCacheRaw string
	classCache    []string
}

// Attribute returns the value of the named attribute (lowercased name) and
// whether it was present.
func (n *Node) Attribute(name string) (string, bool) {
	if n.Attr == nil {
		return "", false
	}
	v, ok := n.Attr[name]
	return v, ok
}

// Classes returns the whitespace-split class list of an element.
func (n *Node) Classes() []string {
	c, ok := n.Attribute("class")
	if !ok {
		return nil
	}
	if c == n.classCacheRaw && n.classCache != nil {
		return n.classCache
	}
	n.classCache = strings.Fields(c)
	n.classCacheRaw = c
	return n.classCache
}

// ID returns the element's id attribute (empty if absent).
func (n *Node) ID() string {
	id, _ := n.Attribute("id")
	return id
}

// Parse turns an HTML document into an owned Node tree. The returned node is a
// Document whose single element child is <html>.
func Parse(htmlSrc string) (*Node, error) {
	root, err := html.Parse(strings.NewReader(htmlSrc))
	if err != nil {
		return nil, err
	}
	doc := &Node{Type: Document}
	convertChildren(root, doc)
	attachDeclarativeShadowRoots(doc)
	return doc, nil
}

// convertChildren walks h's children, converting element and text nodes and
// dropping everything else (comments, doctype, processing instructions).
func convertChildren(h *html.Node, parent *Node) {
	for c := h.FirstChild; c != nil; c = c.NextSibling {
		switch c.Type {
		case html.ElementNode:
			el := &Node{
				Type:   Element,
				Tag:    strings.ToLower(c.Data),
				Attr:   map[string]string{},
				Parent: parent,
			}
			for _, a := range c.Attr {
				el.Attr[strings.ToLower(a.Key)] = a.Val
			}
			parent.Children = append(parent.Children, el)
			convertChildren(c, el)
		case html.TextNode:
			// Preserve the raw text; whitespace handling is a layout concern.
			parent.Children = append(parent.Children, &Node{
				Type:   Text,
				Text:   c.Data,
				Parent: parent,
			})
		}
	}
}

// Find returns the first element in document order with the given tag, or nil.
func Find(n *Node, tag string) *Node {
	if n.Type == Element && n.Tag == tag {
		return n
	}
	for _, c := range n.Children {
		if got := Find(c, tag); got != nil {
			return got
		}
	}
	return nil
}

// Title returns the document's <title> text, trimmed, or "".
func Title(root *Node) string {
	t := Find(root, "title")
	if t == nil {
		return ""
	}
	var sb strings.Builder
	for _, c := range t.Children {
		if c.Type == Text {
			sb.WriteString(c.Text)
		}
	}
	return strings.TrimSpace(sb.String())
}
