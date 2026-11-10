// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

//go:build !webengine_nojs

// This file is the Go-facing counterpart to js/dom.go's querySelector
// binding: a host driving synthetic.go's Focus/Type/Click or formsubmit.go's
// Submit/PreviewSubmit needs to resolve a CSS selector to a *dom.Node
// without going through JS at all (the automation layer's own scenario
// steps name elements by selector, not by node reference). The underlying
// matcher is already public — css.ParseSelectorList/(css.Selector).Matches —
// js/dom.go's own unexported query (js/dom.go:1178) is this same small
// tree-walk over the identical primitives; reimplemented here rather than
// exported from js, since it has no dependency on anything js-package-private.
package engine

import (
	"github.com/go-webengine/engine/css"
	"github.com/go-webengine/engine/dom"
)

// QuerySelector returns the first element in document order, under the
// live document's own root, matching selector — the same result a page
// script's document.querySelector(selector) would see right now. ok is
// false for an empty/unparseable selector or no match; never guesses
// between multiple matches (the caller gets the first, exactly as a real
// querySelector would, not an error).
func (d *LiveDocument) QuerySelector(selector string) (*dom.Node, bool) {
	got := queryAll(d.doc.Root, selector, true)
	if len(got) == 0 {
		return nil, false
	}
	return got[0], true
}

// QuerySelectorAll returns every element in document order, under the live
// document's own root, matching selector (nil for an empty/unparseable
// selector or no match).
func (d *LiveDocument) QuerySelectorAll(selector string) []*dom.Node {
	return queryAll(d.doc.Root, selector, false)
}

// queryAll is js/dom.go's own query (js/dom.go:1178), reimplemented once
// here over the same exported primitives (css.ParseSelectorList, Selector's
// own Matches) — see this file's own doc comment for why it isn't shared
// directly instead.
func queryAll(scope *dom.Node, selector string, firstOnly bool) []*dom.Node {
	sels := css.ParseSelectorList(selector)
	if len(sels) == 0 {
		return nil
	}
	var out []*dom.Node
	var walk func(n *dom.Node) bool
	walk = func(n *dom.Node) bool {
		for _, c := range n.Children {
			if c.Type == dom.Element && matchesAnySelector(sels, c) {
				out = append(out, c)
				if firstOnly {
					return true
				}
			}
			if walk(c) {
				return true
			}
		}
		return false
	}
	walk(scope)
	return out
}

// matchesAnySelector is js/dom.go's own matchesAny (js/dom.go:1440).
func matchesAnySelector(sels []css.Selector, n *dom.Node) bool {
	for _, s := range sels {
		if s.Matches(n) {
			return true
		}
	}
	return false
}
