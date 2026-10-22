// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package css

import (
	"testing"

	"github.com/go-webengine/engine/dom"
)

// user-select (CSS UI 4 §6.1): none passes to descendants whose value is
// auto; text, all and contain take it back; generated content is none
// unless its own rule says otherwise.
func TestCascadeUserSelect(t *testing.T) {
	root, err := dom.Parse(`<html><body><style>
		.n { user-select: none }
		.w { -webkit-user-select: none }
		.t { user-select: text }
		.a { user-select: auto }
		.d::before { content: "1" }
		.s::before { content: "2"; user-select: text }
		.u::before { content: "3"; user-select: auto }
	</style>
	<div class="n" id="n"><span id="child">x</span><span class="t" id="text">y</span><span class="a" id="auto">z</span></div>
	<div class="w" id="w"></div>
	<p id="plain">p</p>
	<span class="d" id="d"></span><span class="s" id="s"></span><span class="u" id="u"></span>
	</body></html>`)
	if err != nil {
		t.Fatal(err)
	}
	sm := Cascade(root)
	var find func(n *dom.Node, id string) *dom.Node
	find = func(n *dom.Node, id string) *dom.Node {
		if n.ID() == id {
			return n
		}
		for _, c := range n.Children {
			if f := find(c, id); f != nil {
				return f
			}
		}
		return nil
	}
	byID := func(id string) *Style {
		found := find(root, id)
		if found == nil || sm[found] == nil {
			t.Fatalf("no style for #%s", id)
		}
		return sm[found]
	}
	for id, want := range map[string]bool{"n": true, "w": true, "child": true, "text": false, "auto": true, "plain": false} {
		if got := byID(id).UserSelectNone; got != want {
			t.Errorf("#%s UserSelectNone = %v, want %v", id, got, want)
		}
	}
	for id, want := range map[string]bool{"d": true, "s": false, "u": true} {
		st := byID(id)
		if st.Before == nil {
			t.Fatalf("#%s has no ::before", id)
		}
		if st.UserSelectNone || st.Before.UserSelectNone != want {
			t.Errorf("#%s: element %v, ::before %v, want false and %v", id, st.UserSelectNone, st.Before.UserSelectNone, want)
		}
	}
}
