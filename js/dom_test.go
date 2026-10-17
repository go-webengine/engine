// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package js

import (
	"testing"

	"github.com/go-webengine/engine/dom"
)

// deepChain builds a depth-level-deep chain of div elements under a single
// root, bypassing dom.Parse (which would refuse it past 512 levels) — the
// same shape a page's own createElement+appendChild loop can build at
// runtime, since dom.AppendChild has no depth check of its own.
func deepChain(depth int) *dom.Node {
	root := &dom.Node{Type: dom.Element, Tag: "div", Attr: map[string]string{}}
	cur := root
	for i := 0; i < depth; i++ {
		d := &dom.Node{Type: dom.Element, Tag: "div", Attr: map[string]string{}}
		d.Parent = cur
		cur.Children = append(cur.Children, d)
		cur = d
	}
	return root
}

func TestIsEqualNodeDoesNotCrashOnPathologicallyDeepTree(t *testing.T) {
	// A script calling node.isEqualNode(other) reaches this independently of
	// the cascade/layout depth caps round 166 added — isEqualNode is never
	// in that pipeline at all. Before maxDOMWalkDepth existed, this
	// recursion had no bound; this uses 10,000, comfortably past
	// maxDOMWalkDepth(512) while staying fast to build and run.
	//
	// The two chains are IDENTICAL in shape, which is deliberate: past the
	// cap this function reports "not equal" unconditionally (see its own
	// doc comment — a documented, intentional degradation, not a bug), so
	// this specifically proves the cap-triggered path returns rather than
	// crashes, not that equal deep trees stay reported as equal.
	a := deepChain(10000)
	b := deepChain(10000)
	if isEqualNode(a, b, 0) {
		t.Error("want false past the depth cap, even for identical trees")
	}
}

func TestIsEqualNodeDeepMismatchPastCapIsNotEqual(t *testing.T) {
	a := deepChain(10000)
	b := deepChain(9999) // one level shallower
	if isEqualNode(a, b, 0) {
		t.Error("trees of different depth should never compare equal")
	}
}

func TestCloneNodeDoesNotCrashOnPathologicallyDeepTree(t *testing.T) {
	root := deepChain(10000)
	clone := cloneNode(root, true)
	if clone == nil {
		t.Fatal("cloneNode returned nil")
	}
	if clone.Tag != root.Tag {
		t.Errorf("clone.Tag = %q, want %q", clone.Tag, root.Tag)
	}
}

func countNodes(n *dom.Node) int {
	c := 1
	for _, ch := range n.Children {
		c += countNodes(ch)
	}
	return c
}

func TestCloneNodeCapsTotalNodeCountPerCall(t *testing.T) {
	// A single cloneNode(true) call recurses depth-first, so maxDOMWalkDepth
	// alone does not bound a BUSHY tree: round 172 found that a script doing
	// `root.appendChild(root.cloneNode(true))` in a loop builds one (each
	// clone nests a full copy of everything so far, one level deeper under
	// root), no single call's own recursion ever exceeding the depth cap,
	// yet TOTAL node count still doubling every iteration — measured at
	// ~20GB of real memory in under 10 seconds. A wide tree with more than
	// maxCloneNodes children exercises the same budget this round added,
	// without needing the full doubling construction to prove it works.
	root := &dom.Node{Type: dom.Element, Tag: "div", Attr: map[string]string{}}
	for i := 0; i < maxCloneNodes+1000; i++ {
		ch := &dom.Node{Type: dom.Element, Tag: "span", Attr: map[string]string{}}
		ch.Parent = root
		root.Children = append(root.Children, ch)
	}
	clone := cloneNode(root, true)
	if got := countNodes(clone); got > maxCloneNodes {
		t.Errorf("cloneNode produced %d nodes, want <= %d (maxCloneNodes)", got, maxCloneNodes)
	}
}

func TestCloneNodeCapsTotalNodeCountInsideTemplateContent(t *testing.T) {
	// The <template> content-fragment clone (see cloneNode's own doc
	// comment, cloning steps for template elements) is a SEPARATE loop from
	// the generic Children walk, sharing the same budget — this exercises
	// that loop's own budget-exhausted break specifically, not just the
	// generic Children one TestCloneNodeCapsTotalNodeCountPerCall already
	// covers.
	tpl := &dom.Node{Type: dom.Element, Tag: "template", Attr: map[string]string{}}
	tpl.Content = &dom.Node{Type: dom.Element, Tag: "#fragment", Attr: map[string]string{}}
	for i := 0; i < maxCloneNodes+1000; i++ {
		ch := &dom.Node{Type: dom.Element, Tag: "span", Attr: map[string]string{}}
		ch.Parent = tpl.Content
		tpl.Content.Children = append(tpl.Content.Children, ch)
	}
	clone := cloneNode(tpl, true)
	if got := countNodes(clone.Content); got > maxCloneNodes {
		t.Errorf("cloned template content has %d nodes, want <= %d (maxCloneNodes)", got, maxCloneNodes)
	}
}
