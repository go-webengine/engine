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
