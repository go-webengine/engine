// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package layout

import (
	"math/rand"
	"testing"

	"github.com/go-webengine/engine/dom"
)

func mkNode(parent *dom.Node) *dom.Node {
	n := &dom.Node{Type: dom.Element, Tag: "div", Parent: parent}
	if parent != nil {
		parent.Children = append(parent.Children, n)
	}
	return n
}

// TestCommonAncestorBasicShapes covers the hand-checkable cases: self,
// parent/child, siblings, cousins at different depths, and two disjoint
// trees (no shared ancestor at all).
func TestCommonAncestorBasicShapes(t *testing.T) {
	root := mkNode(nil)
	a := mkNode(root)
	b := mkNode(root)
	aa := mkNode(a)
	ab := mkNode(a)
	aaa := mkNode(aa)

	cases := []struct {
		name string
		x, y *dom.Node
		want *dom.Node
	}{
		{"self", aaa, aaa, aaa},
		{"parent-child", a, aa, a},
		{"child-parent", aa, a, a},
		{"siblings", aa, ab, a},
		{"cousins-different-depth", aaa, ab, a},
		{"far-apart-same-root", aaa, b, root},
		{"root-and-leaf", root, aaa, root},
	}
	for _, c := range cases {
		if got := commonAncestor(c.x, c.y); got != c.want {
			t.Errorf("%s: commonAncestor = %p, want %p", c.name, got, c.want)
		}
	}

	otherRoot := mkNode(nil)
	otherLeaf := mkNode(otherRoot)
	if got := commonAncestor(aaa, otherLeaf); got != nil {
		t.Errorf("disjoint trees: commonAncestor = %p, want nil", got)
	}
	// Equal depth in disjoint trees — the case the depth-equalizing co-walk
	// must terminate on both sides reaching nil simultaneously, not run
	// past it.
	if got := commonAncestor(aa, otherLeaf); got != nil {
		t.Errorf("disjoint trees, equal depth: commonAncestor = %p, want nil", got)
	}
}

// TestCommonAncestorNilInputs covers a nil node on either or both sides —
// markNoWrap's own guard already skips these before calling, but the
// function's doc comment promises nil-safety independently of that caller.
func TestCommonAncestorNilInputs(t *testing.T) {
	n := mkNode(nil)
	if got := commonAncestor(nil, nil); got != nil {
		t.Errorf("commonAncestor(nil, nil) = %p, want nil", got)
	}
	if got := commonAncestor(n, nil); got != nil {
		t.Errorf("commonAncestor(n, nil) = %p, want nil", got)
	}
	if got := commonAncestor(nil, n); got != nil {
		t.Errorf("commonAncestor(nil, n) = %p, want nil", got)
	}
}

// commonAncestorOriginal is the pre-round-198 implementation (ancestor set
// + scan), kept here only as this test's own oracle — not used by
// production code.
func commonAncestorOriginal(a, b *dom.Node) *dom.Node {
	if a == b {
		return a
	}
	seen := map[*dom.Node]bool{}
	for n := a; n != nil; n = n.Parent {
		seen[n] = true
	}
	for n := b; n != nil; n = n.Parent {
		if seen[n] {
			return n
		}
	}
	return nil
}

// TestCommonAncestorAgreesWithOriginalOnRandomTree is this fix's own
// correctness proof: 2000 random node pairs over a randomly shaped
// ~500-node tree, checked against the original ancestor-set implementation
// kept above as an oracle (round 198, no engine# — performance only).
func TestCommonAncestorAgreesWithOriginalOnRandomTree(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	root := mkNode(nil)
	nodes := []*dom.Node{root}
	for i := 0; i < 500; i++ {
		parent := nodes[rng.Intn(len(nodes))]
		nodes = append(nodes, mkNode(parent))
	}
	for i := 0; i < 2000; i++ {
		a := nodes[rng.Intn(len(nodes))]
		b := nodes[rng.Intn(len(nodes))]
		got, want := commonAncestor(a, b), commonAncestorOriginal(a, b)
		if got != want {
			t.Fatalf("pair %d: commonAncestor(%p,%p) = %p, oracle wants %p", i, a, b, got, want)
		}
	}
}

// BenchmarkCommonAncestor is this fix's own before/after baseline (round
// 198, no engine# — performance only, no behaviour change). The tree
// deliberately mirrors realistic inline-markup NESTING DEPTH (a chain of
// ~16 levels, as real markup like nested <div><p><span><em><strong>...>
// commonly reaches), not a shallow random-attachment tree (which has only
// O(log n) depth and under-states the original version's real cost — an
// earlier draft of this benchmark used one and measured the gap as
// negligible, which did not match profiling a real render; rebuilt once
// that was noticed, rather than trusting the first number).
func BenchmarkCommonAncestor(b *testing.B) {
	root := mkNode(nil)
	cur := root
	chain := []*dom.Node{root}
	for i := 0; i < 16; i++ {
		cur = mkNode(cur)
		chain = append(chain, cur)
	}
	// x and y are siblings' descendants near the bottom of two branches
	// that fork a few levels up — the realistic "two adjacent words inside
	// differently-nested inline elements" shape markNoWrap sees.
	fork := chain[len(chain)-4]
	x := mkNode(mkNode(mkNode(fork)))
	y := mkNode(mkNode(mkNode(fork)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		commonAncestor(x, y)
	}
}
