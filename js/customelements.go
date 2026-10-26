// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package js

import (
	"github.com/dop251/goja"

	"github.com/go-webengine/engine/dom"
)

// upgradeExisting constructs ctor, with `this` bound to the real element, for
// every element already in the document (light DOM and any shadow tree)
// matching name — the one confirmed real-world shape this round closes (see
// newCustomElementRegistry's doc comment for what is deliberately NOT
// covered: later-inserted elements, observedAttributes, the lifecycle
// callbacks, reentrant defines).
func (b *binder) upgradeExisting(name string, ctor goja.Value) {
	var walk func(n *dom.Node)
	walk = func(n *dom.Node) {
		if n.Type == dom.Element {
			if n.Tag == name {
				b.upgradeElement(n, ctor)
			}
			if n.Shadow != nil {
				for _, c := range n.Shadow.Children {
					walk(c)
				}
			}
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(b.root)
}

// upgradeElement invokes ctor as a `new` expression with n's existing
// wrapper standing in for the freshly allocated object goja's own
// [[Construct]] would otherwise use as `this` — see newInterfaceCtor's doc
// comment for the mechanism. Errors and panics are contained (one bad
// custom-element constructor must not abort the rest of the script), the
// same "best-effort" convention callSafely uses for event callbacks.
func (b *binder) upgradeElement(n *dom.Node, ctor goja.Value) {
	construct, ok := goja.AssertConstructor(ctor)
	if !ok {
		return
	}
	wrapper, ok := b.wrap(n).(*goja.Object)
	if !ok {
		return
	}
	ctorObj, ok := ctor.(*goja.Object)
	if !ok {
		return
	}
	prev := b.pendingUpgrade
	b.pendingUpgrade = wrapper
	defer func() {
		b.pendingUpgrade = prev
		if r := recover(); r != nil {
			b.logf("custom element upgrade panic: %v", r)
		}
	}()
	b.vm.ClearInterrupt()
	if _, err := construct(ctorObj); err != nil {
		b.logf("custom element upgrade: %v", err)
	}
}

// wrapShadowRoot returns the cached JS object for a shadow root, creating it
// on first use so identity is preserved (el.shadowRoot read twice, or
// compared against attachShadow()'s own return value, is the same object) —
// the ShadowRoot counterpart to wrap()'s node cache.
func (b *binder) wrapShadowRoot(sr *dom.ShadowRoot) goja.Value {
	if sr == nil {
		return goja.Null()
	}
	if o, ok := b.shadowCache[sr]; ok {
		return o
	}
	o := b.vm.NewObject()
	if b.shadowCache == nil {
		b.shadowCache = map[*dom.ShadowRoot]*goja.Object{}
	}
	b.shadowCache[sr] = o
	b.accessor(o, "host", func() goja.Value { return b.wrap(sr.Host) }, nil)
	// "open"/"closed" are not recorded (dom.ShadowRoot's own doc comment) —
	// reporting "open" unconditionally matches the one confirmed real caller,
	// which always passes {mode:"open"}.
	b.accessor(o, "mode", func() goja.Value { return b.vm.ToValue("open") }, nil)
	o.Set("appendChild", func(call goja.FunctionCall) goja.Value {
		child := b.node(call.Argument(0))
		if child == nil {
			return goja.Null()
		}
		sr.AppendChild(child)
		return call.Argument(0)
	})
	if p := b.protos["ShadowRoot"]; p != nil {
		_ = o.SetPrototype(p)
	}
	return o
}
