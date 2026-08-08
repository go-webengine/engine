// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package js

import (
	"net/url"

	"github.com/dop251/goja"

	"github.com/go-webengine/engine/dom"
)

// addListener registers handler for typ on node n (deduplicating identical
// registrations, matching the DOM).
func (b *binder) addListener(n *dom.Node, typ string, handler goja.Value) {
	if handler == nil || goja.IsUndefined(handler) || goja.IsNull(handler) {
		return
	}
	if b.listeners[n] == nil {
		b.listeners[n] = map[string][]goja.Value{}
	}
	for _, h := range b.listeners[n][typ] {
		if h == handler {
			return
		}
	}
	b.listeners[n][typ] = append(b.listeners[n][typ], handler)
}

// removeListener unregisters a previously added handler.
func (b *binder) removeListener(n *dom.Node, typ string, handler goja.Value) {
	m := b.listeners[n]
	if m == nil {
		return
	}
	hs := m[typ]
	for i, h := range hs {
		if h == handler {
			m[typ] = append(hs[:i], hs[i+1:]...)
			return
		}
	}
}

// dispatch fires every listener registered for typ on n, invoking each with the
// event as argument and n as `this`. Handler errors/panics are contained.
func (b *binder) dispatch(n *dom.Node, typ string, event goja.Value) {
	m := b.listeners[n]
	if m == nil {
		return
	}
	self := b.wrap(n)
	if n == b.windowNode || n == b.docNode {
		self = b.vm.GlobalObject()
	}
	// Copy so a handler that mutates the list mid-dispatch is safe.
	hs := append([]goja.Value(nil), m[typ]...)
	for _, h := range hs {
		fn := b.handlerFunc(h)
		b.callSafely(fn, self, event)
	}
}

// handlerFunc resolves an EventListener value to a callable: a function, or an
// object exposing handleEvent.
func (b *binder) handlerFunc(h goja.Value) goja.Callable {
	if fn, ok := goja.AssertFunction(h); ok {
		return fn
	}
	if obj, ok := h.(*goja.Object); ok {
		if fn, ok := goja.AssertFunction(obj.Get("handleEvent")); ok {
			return fn
		}
	}
	return nil
}

// newEvent builds a minimal Event object of the given type.
func (b *binder) newEvent(typ string) *goja.Object {
	o := b.vm.NewObject()
	o.Set("type", typ)
	o.Set("bubbles", false)
	o.Set("cancelable", false)
	o.Set("defaultPrevented", false)
	o.Set("target", goja.Null())
	o.Set("currentTarget", goja.Null())
	o.Set("timeStamp", 0)
	o.Set("isTrusted", false)
	noop := func(goja.FunctionCall) goja.Value { return goja.Undefined() }
	o.Set("preventDefault", func(goja.FunctionCall) goja.Value {
		o.Set("defaultPrevented", true)
		return goja.Undefined()
	})
	o.Set("stopPropagation", noop)
	o.Set("stopImmediatePropagation", noop)
	o.Set("composedPath", func(goja.FunctionCall) goja.Value { return b.vm.NewArray() })
	return o
}

// eventType extracts the type of a dispatched event value.
func eventType(v goja.Value) string {
	if obj, ok := v.(*goja.Object); ok {
		if t := obj.Get("type"); t != nil && !goja.IsUndefined(t) {
			return t.String()
		}
	}
	if v == nil {
		return ""
	}
	return v.String()
}

// installConstructors wires the constructible globals hydration code expects:
// Event/CustomEvent, the observer classes, URL, XMLHttpRequest and the Node
// type-constant holder.
func (b *binder) installConstructors(g *goja.Object) {
	g.Set("Event", func(call goja.ConstructorCall) *goja.Object {
		ev := b.newEvent(call.Argument(0).String())
		applyEventInit(ev, call.Argument(1))
		return ev
	})
	g.Set("CustomEvent", func(call goja.ConstructorCall) *goja.Object {
		ev := b.newEvent(call.Argument(0).String())
		init := call.Argument(1)
		applyEventInit(ev, init)
		ev.Set("detail", goja.Null())
		if obj, ok := init.(*goja.Object); ok {
			if d := obj.Get("detail"); d != nil {
				ev.Set("detail", d)
			}
		}
		return ev
	})

	observer := func(call goja.ConstructorCall) *goja.Object {
		o := b.vm.NewObject()
		noop := func(goja.FunctionCall) goja.Value { return goja.Undefined() }
		o.Set("observe", noop)
		o.Set("unobserve", noop)
		o.Set("disconnect", noop)
		o.Set("takeRecords", func(goja.FunctionCall) goja.Value { return b.vm.NewArray() })
		return o
	}
	g.Set("MutationObserver", observer)
	g.Set("IntersectionObserver", observer)
	g.Set("ResizeObserver", observer)
	g.Set("PerformanceObserver", observer)

	g.Set("URL", func(call goja.ConstructorCall) *goja.Object {
		return b.buildURL(call.Argument(0).String(), call.Argument(1))
	})
	g.Set("Image", func(call goja.ConstructorCall) *goja.Object {
		return b.vm.NewObject()
	})

	node := b.vm.NewObject()
	for name, val := range map[string]int{
		"ELEMENT_NODE": 1, "TEXT_NODE": 3, "COMMENT_NODE": 8, "DOCUMENT_NODE": 9,
		"DOCUMENT_FRAGMENT_NODE": 11, "ATTRIBUTE_NODE": 2, "CDATA_SECTION_NODE": 4,
		"PROCESSING_INSTRUCTION_NODE": 7, "DOCUMENT_TYPE_NODE": 10,
	} {
		node.Set(name, val)
	}
	g.Set("Node", node)
}

// applyEventInit copies the bubbles/cancelable flags from an EventInit dict.
func applyEventInit(ev *goja.Object, init goja.Value) {
	obj, ok := init.(*goja.Object)
	if !ok {
		return
	}
	if v := obj.Get("bubbles"); v != nil && !goja.IsUndefined(v) {
		ev.Set("bubbles", v.ToBoolean())
	}
	if v := obj.Get("cancelable"); v != nil && !goja.IsUndefined(v) {
		ev.Set("cancelable", v.ToBoolean())
	}
}

// buildURL constructs a WHATWG-ish URL object (a subset: the parsed components).
func (b *binder) buildURL(ref string, baseV goja.Value) *goja.Object {
	var u *url.URL
	if base := optString(baseV); base != "" {
		if bu, err := url.Parse(base); err == nil {
			if r, err := url.Parse(ref); err == nil {
				u = bu.ResolveReference(r)
			}
		}
	}
	if u == nil {
		u, _ = url.Parse(ref)
	}
	if u == nil {
		u = &url.URL{}
	}
	o := b.vm.NewObject()
	o.Set("href", u.String())
	o.Set("protocol", withColon(u.Scheme))
	o.Set("host", u.Host)
	o.Set("hostname", u.Hostname())
	o.Set("port", u.Port())
	o.Set("pathname", pathOr(u.Path))
	o.Set("search", withPrefix("?", u.RawQuery))
	o.Set("hash", withPrefix("#", u.Fragment))
	o.Set("origin", origin(u))
	o.Set("searchParams", b.newURLSearchParams(b.vm.ToValue(u.RawQuery)))
	o.Set("toString", func(goja.FunctionCall) goja.Value { return b.vm.ToValue(u.String()) })
	return o
}

// optString returns the string of v unless it is undefined/null.
func optString(v goja.Value) string {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return ""
	}
	return v.String()
}
