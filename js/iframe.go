// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package js

import (
	"strings"

	"github.com/dop251/goja"
	"github.com/go-webengine/engine/dom"
)

// defineIframe adds contentDocument/contentWindow to an <iframe> element
// wrapper defineElement already built — real, live DOM access to the SAME
// nested tree the iframe's own render used (n.IframeDoc, set by the engine's
// loadIframe), not a snapshot or a cross-runtime copy. Both are nil/null
// until the iframe has actually loaded something (no src/srcdoc, an
// unfetchable src, or past the engine's own recursion-depth cap — the exact
// cases loadIframe itself already declines).
//
// Deliberately narrower than a real Window/Document, no confirmed trigger
// yet for more: no document.write/open/close (replacing the nested content
// and re-rendering it live is a separate, larger piece of work — the
// iframe's own render already happened once, before any parent script ran),
// and no live script-to-script bridge at all — the nested document's OWN
// JavaScript already finished running (inside loadIframe's own recursive
// RenderDocument call) by the time a parent script ever gets to read this,
// so mutating the returned document changes real nodes but triggers no
// re-render of the iframe's own visible bitmap this round, and there is no
// second, still-running goja.Runtime on the other end to call into — goja's
// own documented contract ("not possible to pass object values between
// runtimes") rules out a live bridge to one even if there were.
func (b *binder) defineIframe(o *goja.Object, n *dom.Node) {
	b.accessor(o, "contentDocument", func() goja.Value {
		if n.IframeDoc == nil {
			return goja.Null()
		}
		return b.contentDocument(n)
	}, nil)
	b.accessor(o, "contentWindow", func() goja.Value {
		if n.IframeDoc == nil {
			return goja.Null()
		}
		return b.contentWindow(n)
	}, nil)
}

// contentWindow returns (creating once, then caching, so identity holds
// across repeated reads) a minimal window-shaped object for iframe's own
// nested document: just `.document` and `.location.href`, the two
// properties a real cross-document script overwhelmingly reads in
// practice (an auto-resize script's `contentWindow.document.body
// .scrollHeight`, a same-origin embed's `contentWindow.location.href`
// check) — not a full Window (no fetch/setTimeout/addEventListener of its
// own; there is no second running script context backing it to deliver
// any of those against).
func (b *binder) contentWindow(n *dom.Node) goja.Value {
	if b.iframeWin == nil {
		b.iframeWin = map[*dom.Node]*goja.Object{}
	}
	if w, ok := b.iframeWin[n]; ok {
		return w
	}
	w := b.vm.NewObject()
	w.Set("document", b.contentDocument(n))
	loc := b.vm.NewObject()
	href := ""
	if src, ok := n.Attribute("src"); ok {
		href = src
	}
	loc.Set("href", href)
	w.Set("location", loc)
	b.iframeWin[n] = w
	return w
}

// contentDocument returns (creating once, then caching) a minimal but real
// Document-shaped object scoped to n.IframeDoc rather than this binder's own
// b.root — reusing wrap() and the SAME query helpers (byID/b.query/byTag)
// the top-level document's own getElementById etc. already use, just
// parameterized on the nested root instead of b.root (those helpers already
// took an explicit root parameter; only the top-level document's own
// wiring hardcodes b.root). Element-level access through the returned
// nodes — style, textContent, attributes, classList, all of defineElement,
// which has no b.root dependency of its own — is fully real, live DOM
// access.
func (b *binder) contentDocument(n *dom.Node) goja.Value {
	if b.iframeDoc == nil {
		b.iframeDoc = map[*dom.Node]*goja.Object{}
	}
	if d, ok := b.iframeDoc[n]; ok {
		return d
	}
	root := n.IframeDoc
	d := b.vm.NewObject()
	b.accessor(d, "nodeType", func() goja.Value { return b.vm.ToValue(9) }, nil)
	b.accessor(d, "documentElement", func() goja.Value { return b.wrap(dom.Find(root, "html")) }, nil)
	b.accessor(d, "body", func() goja.Value {
		if els := byTag(root, "body"); len(els) > 0 {
			return b.wrap(els[0])
		}
		return goja.Null()
	}, nil)
	b.accessor(d, "title", func() goja.Value { return b.vm.ToValue(dom.Title(root)) }, nil)
	d.Set("getElementById", func(call goja.FunctionCall) goja.Value {
		return b.wrap(byID(root, call.Argument(0).String()))
	})
	d.Set("querySelector", func(call goja.FunctionCall) goja.Value {
		if got := b.query(root, call.Argument(0).String(), true); len(got) > 0 {
			return b.wrap(got[0])
		}
		return goja.Null()
	})
	d.Set("querySelectorAll", func(call goja.FunctionCall) goja.Value {
		return b.wrapList(b.query(root, call.Argument(0).String(), false))
	})
	d.Set("getElementsByTagName", func(call goja.FunctionCall) goja.Value {
		return b.wrapList(byTag(root, strings.ToLower(call.Argument(0).String())))
	})
	d.Set("getElementsByClassName", func(call goja.FunctionCall) goja.Value {
		return b.wrapList(byClass(root, strings.Fields(call.Argument(0).String())))
	})
	b.iframeDoc[n] = d
	return d
}
