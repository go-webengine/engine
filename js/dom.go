// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package js

import (
	"strings"

	"github.com/dop251/goja"

	"github.com/go-webengine/engine/css"
	"github.com/go-webengine/engine/dom"
)

// accessor defines a JS accessor property on o. A nil set makes it read-only.
func (b *binder) accessor(o *goja.Object, name string, get func() goja.Value, set func(goja.Value)) {
	getter := b.vm.ToValue(func(goja.FunctionCall) goja.Value { return get() })
	var setter goja.Value
	if set != nil {
		setter = b.vm.ToValue(func(call goja.FunctionCall) goja.Value {
			set(call.Argument(0))
			return goja.Undefined()
		})
	}
	_ = o.DefineAccessorProperty(name, getter, setter, goja.FLAG_TRUE, goja.FLAG_TRUE)
}

// wrap returns the cached JS object for a DOM node, creating it on first use so
// node identity is preserved (el === el). Nil maps to JS null.
func (b *binder) wrap(n *dom.Node) goja.Value {
	if n == nil {
		return goja.Null()
	}
	if o, ok := b.cache[n]; ok {
		return o
	}
	o := b.vm.NewObject()
	b.cache[n] = o
	if n.Type == dom.Text {
		b.defineText(o, n)
	} else {
		b.defineElement(o, n)
	}
	// Stamp the wrapper with its interface prototype (HTMLElement/Text/…) so
	// `node instanceof HTMLElement` holds and core-js/React-DOM subclass checks
	// resolve. The per-instance accessors above still shadow, so behaviour is
	// unchanged; the prototype only adds the instanceof/subclass chain.
	if p := b.protoForNode(n); p != nil {
		_ = o.SetPrototype(p)
	}
	return o
}

// wrapList builds a JS array of wrapped nodes (a NodeList/HTMLCollection stand-in).
func (b *binder) wrapList(nodes []*dom.Node) goja.Value {
	vals := make([]interface{}, len(nodes))
	for i, n := range nodes {
		vals[i] = b.wrap(n)
	}
	return b.vm.NewArray(vals...)
}

// defineText populates a Text node wrapper.
func (b *binder) defineText(o *goja.Object, n *dom.Node) {
	b.accessor(o, "nodeType", func() goja.Value { return b.vm.ToValue(3) }, nil)
	b.accessor(o, "nodeName", func() goja.Value { return b.vm.ToValue("#text") }, nil)
	b.accessor(o, "textContent",
		func() goja.Value { return b.vm.ToValue(n.Text) },
		func(v goja.Value) { n.Text = v.String() })
	b.accessor(o, "nodeValue",
		func() goja.Value { return b.vm.ToValue(n.Text) },
		func(v goja.Value) { n.Text = v.String() })
	b.accessor(o, "data",
		func() goja.Value { return b.vm.ToValue(n.Text) },
		func(v goja.Value) { n.Text = v.String() })
	b.accessor(o, "parentNode", func() goja.Value { return b.wrap(n.Parent) }, nil)
	b.accessor(o, "parentElement", func() goja.Value { return b.wrap(elementParent(n)) }, nil)
	b.accessor(o, "nextSibling", func() goja.Value { return b.wrap(nextSibling(n)) }, nil)
	b.accessor(o, "previousSibling", func() goja.Value { return b.wrap(prevSibling(n)) }, nil)
	b.accessor(o, "ownerDocument", func() goja.Value { return b.documentValue() }, nil)
}

// documentValue returns the JS document object (the wrapper for the root node),
// the value of every node's ownerDocument. React reads node.ownerDocument to key
// its event system, so this must be a real object, never undefined.
func (b *binder) documentValue() goja.Value {
	if d, ok := b.cache[b.root]; ok {
		return d
	}
	return goja.Null()
}

// defineElement populates an Element node wrapper with the supported surface.
func (b *binder) defineElement(o *goja.Object, n *dom.Node) {
	b.accessor(o, "nodeType", func() goja.Value { return b.vm.ToValue(1) }, nil)
	b.accessor(o, "tagName", func() goja.Value { return b.vm.ToValue(strings.ToUpper(n.Tag)) }, nil)
	b.accessor(o, "nodeName", func() goja.Value { return b.vm.ToValue(strings.ToUpper(n.Tag)) }, nil)
	b.accessor(o, "localName", func() goja.Value { return b.vm.ToValue(n.Tag) }, nil)

	b.accessor(o, "id",
		func() goja.Value { return b.vm.ToValue(n.ID()) },
		func(v goja.Value) { b.setAttr(n, "id", v.String()) })
	b.accessor(o, "className",
		func() goja.Value { c, _ := n.Attribute("class"); return b.vm.ToValue(c) },
		func(v goja.Value) { b.setAttr(n, "class", v.String()) })
	b.accessor(o, "classList", func() goja.Value { return b.newClassList(n) }, nil)

	b.accessor(o, "style", func() goja.Value {
		return b.vm.NewDynamicObject(&styleDynObj{b: b, n: n})
	}, nil)

	b.accessor(o, "textContent",
		func() goja.Value { return b.vm.ToValue(dom.TextContent(n)) },
		func(v goja.Value) { dom.SetTextContent(n, v.String()) })
	b.accessor(o, "innerText",
		func() goja.Value { return b.vm.ToValue(dom.TextContent(n)) },
		func(v goja.Value) { dom.SetTextContent(n, v.String()) })
	b.accessor(o, "innerHTML",
		func() goja.Value { return b.vm.ToValue(dom.InnerHTML(n)) },
		func(v goja.Value) {
			if err := dom.SetInnerHTML(n, v.String()); err != nil {
				b.logf("innerHTML: %v", err)
			}
		})
	b.accessor(o, "outerHTML", func() goja.Value { return b.vm.ToValue(dom.OuterHTML(n)) }, nil)

	b.accessor(o, "children", func() goja.Value { return b.wrapList(elementChildren(n)) }, nil)
	b.accessor(o, "childNodes", func() goja.Value { return b.wrapList(n.Children) }, nil)
	b.accessor(o, "childElementCount", func() goja.Value { return b.vm.ToValue(len(elementChildren(n))) }, nil)
	b.accessor(o, "parentNode", func() goja.Value { return b.wrap(n.Parent) }, nil)
	b.accessor(o, "parentElement", func() goja.Value { return b.wrap(elementParent(n)) }, nil)
	b.accessor(o, "firstChild", func() goja.Value { return b.wrap(firstChild(n)) }, nil)
	b.accessor(o, "lastChild", func() goja.Value { return b.wrap(lastChild(n)) }, nil)
	b.accessor(o, "firstElementChild", func() goja.Value { return b.wrap(firstElementChild(n)) }, nil)
	b.accessor(o, "lastElementChild", func() goja.Value { return b.wrap(lastElementChild(n)) }, nil)
	b.accessor(o, "nextElementSibling", func() goja.Value { return b.wrap(nextElementSibling(n)) }, nil)
	b.accessor(o, "previousElementSibling", func() goja.Value { return b.wrap(prevElementSibling(n)) }, nil)
	b.accessor(o, "nextSibling", func() goja.Value { return b.wrap(nextSibling(n)) }, nil)
	b.accessor(o, "previousSibling", func() goja.Value { return b.wrap(prevSibling(n)) }, nil)
	b.accessor(o, "ownerDocument", func() goja.Value { return b.documentValue() }, nil)

	b.accessor(o, "hidden",
		func() goja.Value { _, ok := n.Attribute("hidden"); return b.vm.ToValue(ok) },
		func(v goja.Value) {
			if v.ToBoolean() {
				b.setAttr(n, "hidden", "")
			} else {
				b.removeAttr(n, "hidden")
			}
		})

	o.Set("getAttribute", func(call goja.FunctionCall) goja.Value {
		if v, ok := n.Attribute(strings.ToLower(call.Argument(0).String())); ok {
			return b.vm.ToValue(v)
		}
		return goja.Null()
	})
	o.Set("setAttribute", func(call goja.FunctionCall) goja.Value {
		b.setAttr(n, strings.ToLower(call.Argument(0).String()), call.Argument(1).String())
		return goja.Undefined()
	})
	o.Set("removeAttribute", func(call goja.FunctionCall) goja.Value {
		b.removeAttr(n, strings.ToLower(call.Argument(0).String()))
		return goja.Undefined()
	})
	o.Set("hasAttribute", func(call goja.FunctionCall) goja.Value {
		_, ok := n.Attribute(strings.ToLower(call.Argument(0).String()))
		return b.vm.ToValue(ok)
	})
	o.Set("toggleAttribute", func(call goja.FunctionCall) goja.Value {
		name := strings.ToLower(call.Argument(0).String())
		if _, ok := n.Attribute(name); ok {
			b.removeAttr(n, name)
			return b.vm.ToValue(false)
		}
		b.setAttr(n, name, "")
		return b.vm.ToValue(true)
	})

	o.Set("appendChild", func(call goja.FunctionCall) goja.Value {
		child := b.node(call.Argument(0))
		dom.AppendChild(n, child)
		return b.wrap(child)
	})
	o.Set("append", func(call goja.FunctionCall) goja.Value {
		for _, a := range call.Arguments {
			dom.AppendChild(n, b.coerceNode(a))
		}
		return goja.Undefined()
	})
	o.Set("prepend", func(call goja.FunctionCall) goja.Value {
		ref := firstChild(n)
		for _, a := range call.Arguments {
			dom.InsertBefore(n, b.coerceNode(a), ref)
		}
		return goja.Undefined()
	})
	o.Set("removeChild", func(call goja.FunctionCall) goja.Value {
		child := b.node(call.Argument(0))
		dom.RemoveChild(n, child)
		return b.wrap(child)
	})
	o.Set("replaceChild", func(call goja.FunctionCall) goja.Value {
		neu, old := b.node(call.Argument(0)), b.node(call.Argument(1))
		dom.InsertBefore(n, neu, old)
		dom.RemoveChild(n, old)
		return b.wrap(old)
	})
	o.Set("insertBefore", func(call goja.FunctionCall) goja.Value {
		neu, ref := b.node(call.Argument(0)), b.node(call.Argument(1))
		dom.InsertBefore(n, neu, ref)
		return b.wrap(neu)
	})
	o.Set("remove", func(goja.FunctionCall) goja.Value {
		if n.Parent != nil {
			dom.RemoveChild(n.Parent, n)
		}
		return goja.Undefined()
	})
	o.Set("cloneNode", func(call goja.FunctionCall) goja.Value {
		deep := call.Argument(0).ToBoolean()
		return b.wrap(cloneNode(n, deep))
	})
	o.Set("contains", func(call goja.FunctionCall) goja.Value {
		return b.vm.ToValue(contains(n, b.node(call.Argument(0))))
	})
	o.Set("isEqualNode", func(call goja.FunctionCall) goja.Value {
		return b.vm.ToValue(isEqualNode(n, b.node(call.Argument(0))))
	})

	o.Set("querySelector", func(call goja.FunctionCall) goja.Value {
		if got := b.query(n, call.Argument(0).String(), true); len(got) > 0 {
			return b.wrap(got[0])
		}
		return goja.Null()
	})
	o.Set("querySelectorAll", func(call goja.FunctionCall) goja.Value {
		return b.wrapList(b.query(n, call.Argument(0).String(), false))
	})
	o.Set("getElementsByTagName", func(call goja.FunctionCall) goja.Value {
		return b.wrapList(byTag(n, strings.ToLower(call.Argument(0).String())))
	})
	o.Set("getElementsByClassName", func(call goja.FunctionCall) goja.Value {
		return b.wrapList(byClass(n, strings.Fields(call.Argument(0).String())))
	})
	o.Set("closest", func(call goja.FunctionCall) goja.Value {
		return b.wrap(b.closest(n, call.Argument(0).String()))
	})
	o.Set("matches", func(call goja.FunctionCall) goja.Value {
		return b.vm.ToValue(matchesSelector(n, call.Argument(0).String()))
	})

	o.Set("addEventListener", func(call goja.FunctionCall) goja.Value {
		b.addListener(n, call.Argument(0).String(), call.Argument(1))
		return goja.Undefined()
	})
	o.Set("removeEventListener", func(call goja.FunctionCall) goja.Value {
		b.removeListener(n, call.Argument(0).String(), call.Argument(1))
		return goja.Undefined()
	})
	o.Set("dispatchEvent", func(call goja.FunctionCall) goja.Value {
		b.dispatch(n, eventType(call.Argument(0)), call.Argument(0))
		return b.vm.ToValue(true)
	})

	// Layout/geometry: backed by the real laid-out box tree when a Metrics source
	// is installed (the engine's settle loop), else zeros (the legacy no-layout
	// path). This is what mw.loader / responsive scripts read to decide layout.
	o.Set("getBoundingClientRect", func(goja.FunctionCall) goja.Value { return b.boundingRect(n) })
	o.Set("getClientRects", func(goja.FunctionCall) goja.Value {
		if _, _, _, _, ok := b.rectOf(n); ok {
			return b.vm.NewArray(b.boundingRect(n))
		}
		return b.vm.NewArray()
	})
	o.Set("focus", func(goja.FunctionCall) goja.Value { return goja.Undefined() })
	o.Set("blur", func(goja.FunctionCall) goja.Value { return goja.Undefined() })
	o.Set("click", func(goja.FunctionCall) goja.Value {
		b.dispatch(n, "click", b.newEvent("click"))
		return goja.Undefined()
	})
	o.Set("scrollIntoView", func(goja.FunctionCall) goja.Value { return goja.Undefined() })
	o.Set("insertAdjacentHTML", func(call goja.FunctionCall) goja.Value {
		b.insertAdjacentHTML(n, strings.ToLower(call.Argument(0).String()), call.Argument(1).String())
		return goja.Undefined()
	})
	b.accessor(o, "offsetWidth", func() goja.Value { return b.vm.ToValue(b.borderW(n)) }, nil)
	b.accessor(o, "offsetHeight", func() goja.Value { return b.vm.ToValue(b.borderH(n)) }, nil)
	b.accessor(o, "clientWidth", func() goja.Value { return b.vm.ToValue(b.borderW(n)) }, nil)
	b.accessor(o, "clientHeight", func() goja.Value { return b.vm.ToValue(b.borderH(n)) }, nil)
	b.accessor(o, "scrollWidth", func() goja.Value { return b.vm.ToValue(b.borderW(n)) }, nil)
	b.accessor(o, "scrollHeight", func() goja.Value { return b.vm.ToValue(b.borderH(n)) }, nil)
	b.accessor(o, "offsetTop", func() goja.Value { return b.vm.ToValue(b.offsetTop(n)) }, nil)
	b.accessor(o, "offsetLeft", func() goja.Value { return b.vm.ToValue(b.offsetLeft(n)) }, nil)
	for _, z := range []string{"scrollTop", "scrollLeft"} {
		b.accessor(o, z, func() goja.Value { return b.vm.ToValue(0) }, nil)
	}
	b.accessor(o, "offsetParent", func() goja.Value { return b.wrap(elementParent(n)) }, nil)
	b.accessor(o, "dataset", func() goja.Value { return b.newDataset(n) }, nil)
	b.accessor(o, "value",
		func() goja.Value { v, _ := n.Attribute("value"); return b.vm.ToValue(v) },
		func(v goja.Value) { b.setAttr(n, "value", v.String()) })
	b.accessor(o, "checked",
		func() goja.Value { _, ok := n.Attribute("checked"); return b.vm.ToValue(ok) },
		func(v goja.Value) {
			if v.ToBoolean() {
				b.setAttr(n, "checked", "")
			} else {
				b.removeAttr(n, "checked")
			}
		})
	if n.Tag == "option" {
		b.accessor(o, "selected",
			func() goja.Value { _, ok := n.Attribute("selected"); return b.vm.ToValue(ok) },
			func(v goja.Value) {
				if !v.ToBoolean() {
					b.removeAttr(n, "selected")
					return
				}
				b.setAttr(n, "selected", "")
				// A single-select <select> holds at most one selected
				// option: marking this one selected implicitly deselects
				// its siblings, matching real <option>.selected semantics.
				// <select multiple> is left alone (opting in another option
				// does not, and should not, clear the others).
				if sel := ancestorSelect(n); sel != nil {
					if _, multiple := sel.Attribute("multiple"); !multiple {
						for _, opt := range byTag(sel, "option") {
							if opt != n {
								b.removeAttr(opt, "selected")
							}
						}
					}
				}
			})
	}
	if n.Tag == "select" {
		b.accessor(o, "selectedIndex",
			func() goja.Value { return b.vm.ToValue(selectedOptionIndex(n)) },
			func(v goja.Value) { b.selectOptionAt(n, int(v.ToInteger())) })
	}
	b.accessor(o, "href",
		func() goja.Value { v, _ := n.Attribute("href"); return b.vm.ToValue(v) },
		func(v goja.Value) { b.setAttr(n, "href", v.String()) })
	b.accessor(o, "src",
		func() goja.Value { v, _ := n.Attribute("src"); return b.vm.ToValue(v) },
		func(v goja.Value) { b.setAttr(n, "src", v.String()) })
	b.accessor(o, "title",
		func() goja.Value { v, _ := n.Attribute("title"); return b.vm.ToValue(v) },
		func(v goja.Value) { b.setAttr(n, "title", v.String()) })
	b.accessor(o, "isConnected", func() goja.Value { return b.vm.ToValue(rooted(n, b.root)) }, nil)
}

// ancestorSelect walks up from an <option> (possibly through an <optgroup>)
// to its owning <select>, or nil if it is not inside one.
func ancestorSelect(n *dom.Node) *dom.Node {
	for p := n.Parent; p != nil; p = p.Parent {
		if p.Type == dom.Element && p.Tag == "select" {
			return p
		}
	}
	return nil
}

// selectedOptionIndex returns the index (in document order, descending into
// <optgroup>) of the first <option> descendant of sel carrying a "selected"
// attribute, or 0 if sel has options but none is marked (the HTML default:
// the first option is selected unless another explicitly is), or -1 if sel
// has no <option> descendants at all.
func selectedOptionIndex(sel *dom.Node) int {
	opts := byTag(sel, "option")
	for i, opt := range opts {
		if _, ok := opt.Attribute("selected"); ok {
			return i
		}
	}
	if len(opts) > 0 {
		return 0
	}
	return -1
}

// selectOptionAt marks the i-th <option> descendant (document order) of sel
// as selected, clearing "selected" from every other option. An out-of-range
// i (matching how selectedIndex=-1 or an overlarge index behaves in a real
// browser: no option ends up selected) just clears every option.
func (b *binder) selectOptionAt(sel *dom.Node, i int) {
	for idx, opt := range byTag(sel, "option") {
		if idx == i {
			b.setAttr(opt, "selected", "")
		} else {
			b.removeAttr(opt, "selected")
		}
	}
}

// setAttr/removeAttr mutate the attribute map (creating it as needed).
func (b *binder) setAttr(n *dom.Node, name, val string) {
	if n.Attr == nil {
		n.Attr = map[string]string{}
	}
	n.Attr[name] = val
}

func (b *binder) removeAttr(n *dom.Node, name string) {
	if n.Attr != nil {
		delete(n.Attr, name)
	}
}

// newRange builds a best-effort Range object: the structural methods are wired
// as no-ops with sensible return shapes, enough that selection/measurement code
// (react-intl, tooltip positioning) does not throw.
func (b *binder) newRange() goja.Value {
	o := b.vm.NewObject()
	if p := b.protos["Range"]; p != nil {
		_ = o.SetPrototype(p)
	}
	o.Set("collapsed", true)
	o.Set("startOffset", 0)
	o.Set("endOffset", 0)
	o.Set("commonAncestorContainer", goja.Null())
	noop := func(goja.FunctionCall) goja.Value { return goja.Undefined() }
	for _, m := range []string{"setStart", "setEnd", "setStartBefore", "setStartAfter",
		"setEndBefore", "setEndAfter", "selectNode", "selectNodeContents", "collapse",
		"deleteContents", "insertNode", "surroundContents", "detach"} {
		o.Set(m, noop)
	}
	o.Set("cloneRange", func(goja.FunctionCall) goja.Value { return b.newRange() })
	o.Set("cloneContents", func(goja.FunctionCall) goja.Value { return b.wrap(dom.NewElement("#fragment")) })
	o.Set("extractContents", func(goja.FunctionCall) goja.Value { return b.wrap(dom.NewElement("#fragment")) })
	o.Set("createContextualFragment", func(call goja.FunctionCall) goja.Value {
		frag := dom.NewElement("#fragment")
		if nodes, err := dom.ParseFragment(call.Argument(0).String()); err == nil {
			for _, c := range nodes {
				dom.AppendChild(frag, c)
			}
		}
		return b.wrap(frag)
	})
	o.Set("getBoundingClientRect", func(goja.FunctionCall) goja.Value { return b.zeroRect() })
	o.Set("getClientRects", func(goja.FunctionCall) goja.Value { return b.vm.NewArray() })
	o.Set("toString", func(goja.FunctionCall) goja.Value { return b.vm.ToValue("") })
	return o
}

// newDOMImplementation stubs document.implementation. createHTMLDocument is
// the one method with a confirmed real caller: jQuery's own support-detection
// (`y.createHTMLDocument = (E.implementation.createHTMLDocument("").body...`)
// and jQuery.parseHTML both call it unconditionally at load/first-use — with
// `document.implementation` entirely absent (this engine's prior state),
// reading `.createHTMLDocument` off `undefined` threw a TypeError that
// aborted jQuery's own bootstrap before it finished assigning the global `$`,
// so every OTHER script on the page that expects `$` to exist failed too with
// a plain ReferenceError. hasFeature always reporting true matches every
// real browser's own (permanently deprecated, always-true) implementation.
func (b *binder) newDOMImplementation() *goja.Object {
	o := b.vm.NewObject()
	o.Set("createHTMLDocument", func(call goja.FunctionCall) goja.Value {
		return b.newDetachedHTMLDocument()
	})
	o.Set("hasFeature", func(goja.FunctionCall) goja.Value { return b.vm.ToValue(true) })
	return o
}

// newDetachedHTMLDocument builds the minimal document-shaped object
// createHTMLDocument's real callers need: a `<html><head></head><body></body>
// </html>` tree of REAL elements (so `.body.innerHTML = "..."` parses actual
// child nodes the same way it would on the main document, and a created
// `<base>`/other element behaves identically to one from `document.
// createElement`) plus `createElement`, all scoped to nodes no different in
// kind from the real document's — this engine has no ownerDocument-scoped
// parsing behaviour to diverge from. Not a full Document (no querySelector,
// no event dispatch, …): jQuery's own usage never reaches for those on the
// document `createHTMLDocument` returns, and nothing else in this engine
// calls it, so implementing more would be unconfirmed, speculative surface.
func (b *binder) newDetachedHTMLDocument() *goja.Object {
	html := dom.NewElement("html")
	head := dom.NewElement("head")
	body := dom.NewElement("body")
	dom.AppendChild(html, head)
	dom.AppendChild(html, body)
	d := b.vm.NewObject()
	d.Set("documentElement", b.wrap(html))
	d.Set("head", b.wrap(head))
	d.Set("body", b.wrap(body))
	d.Set("createElement", func(call goja.FunctionCall) goja.Value {
		return b.wrap(dom.NewElement(call.Argument(0).String()))
	})
	return d
}

// newDataset exposes element.dataset (data-* attributes) as a dynamic object.
func (b *binder) newDataset(n *dom.Node) goja.Value {
	return b.vm.NewDynamicObject(&datasetDynObj{b: b, n: n})
}

// zeroRect returns a DOMRect-shaped object of zeros.
func (b *binder) zeroRect() goja.Value {
	r := b.vm.NewObject()
	for _, k := range []string{"top", "right", "bottom", "left", "x", "y", "width", "height"} {
		r.Set(k, 0)
	}
	return r
}

// node extracts the *dom.Node backing a wrapped JS value, or nil.
func (b *binder) node(v goja.Value) *dom.Node {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return nil
	}
	obj, ok := v.(*goja.Object)
	if !ok {
		return nil
	}
	for n, o := range b.cache {
		if o == obj {
			return n
		}
	}
	return nil
}

// coerceNode turns an appendChild/append argument into a node: an existing node
// wrapper, or a text node from a string/other primitive.
func (b *binder) coerceNode(v goja.Value) *dom.Node {
	if n := b.node(v); n != nil {
		return n
	}
	return dom.NewText(v.String())
}

// query runs a selector over n's descendants (never n itself), returning the
// first match when firstOnly, else all matches in document order.
func (b *binder) query(scope *dom.Node, selector string, firstOnly bool) []*dom.Node {
	sels := css.ParseSelectorList(selector)
	if len(sels) == 0 {
		return nil
	}
	var out []*dom.Node
	var walk func(n *dom.Node) bool
	walk = func(n *dom.Node) bool {
		for _, c := range n.Children {
			if c.Type == dom.Element && matchesAny(sels, c) {
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

// closest walks n and its ancestors for the first element matching selector.
func (b *binder) closest(n *dom.Node, selector string) *dom.Node {
	sels := css.ParseSelectorList(selector)
	for cur := n; cur != nil; cur = elementParent(cur) {
		if cur.Type == dom.Element && matchesAny(sels, cur) {
			return cur
		}
	}
	return nil
}

// insertAdjacentHTML parses html and inserts it at the given position.
func (b *binder) insertAdjacentHTML(n *dom.Node, position, htmlSrc string) {
	nodes, err := dom.ParseFragment(htmlSrc)
	if err != nil {
		b.logf("insertAdjacentHTML: %v", err)
		return
	}
	switch position {
	case "beforeend":
		for _, c := range nodes {
			dom.AppendChild(n, c)
		}
	case "afterbegin":
		ref := firstChild(n)
		for _, c := range nodes {
			dom.InsertBefore(n, c, ref)
		}
	case "beforebegin":
		if n.Parent != nil {
			for _, c := range nodes {
				dom.InsertBefore(n.Parent, c, n)
			}
		}
	case "afterend":
		if n.Parent != nil {
			ref := nextSibling(n)
			for _, c := range nodes {
				dom.InsertBefore(n.Parent, c, ref)
			}
		}
	}
}

// installDocument builds and installs the document object on the global scope.
func (b *binder) installDocument() *goja.Object {
	d := b.vm.NewObject()
	b.cache[b.root] = d // so document === node lookups resolve

	b.accessor(d, "nodeType", func() goja.Value { return b.vm.ToValue(9) }, nil)
	b.accessor(d, "documentElement", func() goja.Value { return b.wrap(dom.Find(b.root, "html")) }, nil)
	b.accessor(d, "body", func() goja.Value { return b.wrap(dom.Find(b.root, "body")) }, nil)
	b.accessor(d, "head", func() goja.Value { return b.wrap(dom.Find(b.root, "head")) }, nil)
	b.accessor(d, "readyState", func() goja.Value { return b.vm.ToValue("complete") }, nil)
	b.accessor(d, "characterSet", func() goja.Value { return b.vm.ToValue("UTF-8") }, nil)
	b.accessor(d, "compatMode", func() goja.Value { return b.vm.ToValue("CSS1Compat") }, nil)
	b.accessor(d, "hidden", func() goja.Value { return b.vm.ToValue(false) }, nil)
	b.accessor(d, "visibilityState", func() goja.Value { return b.vm.ToValue("visible") }, nil)
	b.accessor(d, "title",
		func() goja.Value { return b.vm.ToValue(dom.Title(b.root)) },
		func(v goja.Value) { b.setTitle(v.String()) })
	b.accessor(d, "cookie",
		func() goja.Value { return b.vm.ToValue(b.cookie) },
		func(v goja.Value) { b.cookie = mergeCookie(b.cookie, v.String()) })
	b.accessor(d, "implementation", func() goja.Value { return b.newDOMImplementation() }, nil)

	d.Set("getElementById", func(call goja.FunctionCall) goja.Value {
		return b.wrap(byID(b.root, call.Argument(0).String()))
	})
	d.Set("querySelector", func(call goja.FunctionCall) goja.Value {
		if got := b.query(b.root, call.Argument(0).String(), true); len(got) > 0 {
			return b.wrap(got[0])
		}
		return goja.Null()
	})
	d.Set("querySelectorAll", func(call goja.FunctionCall) goja.Value {
		return b.wrapList(b.query(b.root, call.Argument(0).String(), false))
	})
	d.Set("getElementsByTagName", func(call goja.FunctionCall) goja.Value {
		return b.wrapList(byTag(b.root, strings.ToLower(call.Argument(0).String())))
	})
	d.Set("getElementsByClassName", func(call goja.FunctionCall) goja.Value {
		return b.wrapList(byClass(b.root, strings.Fields(call.Argument(0).String())))
	})
	d.Set("getElementsByName", func(call goja.FunctionCall) goja.Value {
		return b.wrapList(byName(b.root, call.Argument(0).String()))
	})
	d.Set("createElement", func(call goja.FunctionCall) goja.Value {
		return b.wrap(dom.NewElement(call.Argument(0).String()))
	})
	d.Set("createElementNS", func(call goja.FunctionCall) goja.Value {
		return b.wrap(dom.NewElement(call.Argument(1).String()))
	})
	d.Set("createTextNode", func(call goja.FunctionCall) goja.Value {
		return b.wrap(dom.NewText(call.Argument(0).String()))
	})
	d.Set("createComment", func(call goja.FunctionCall) goja.Value {
		return b.wrap(dom.NewText(""))
	})
	d.Set("createDocumentFragment", func(goja.FunctionCall) goja.Value {
		return b.wrap(dom.NewElement("#fragment"))
	})
	d.Set("addEventListener", func(call goja.FunctionCall) goja.Value {
		b.addListener(b.docNode, call.Argument(0).String(), call.Argument(1))
		return goja.Undefined()
	})
	d.Set("removeEventListener", func(call goja.FunctionCall) goja.Value {
		b.removeListener(b.docNode, call.Argument(0).String(), call.Argument(1))
		return goja.Undefined()
	})
	d.Set("dispatchEvent", func(call goja.FunctionCall) goja.Value {
		b.dispatch(b.docNode, eventType(call.Argument(0)), call.Argument(0))
		return b.vm.ToValue(true)
	})
	d.Set("hasFocus", func(goja.FunctionCall) goja.Value { return b.vm.ToValue(true) })
	d.Set("elementFromPoint", func(goja.FunctionCall) goja.Value { return goja.Null() })
	d.Set("elementsFromPoint", func(goja.FunctionCall) goja.Value { return b.vm.NewArray() })
	d.Set("getSelection", func(goja.FunctionCall) goja.Value { return goja.Null() })
	d.Set("createRange", func(goja.FunctionCall) goja.Value { return b.newRange() })
	d.Set("createEvent", func(call goja.FunctionCall) goja.Value { return b.newEvent("") })
	d.Set("createNodeIterator", func(goja.FunctionCall) goja.Value { return b.vm.NewObject() })
	d.Set("createTreeWalker", func(goja.FunctionCall) goja.Value { return b.vm.NewObject() })
	d.Set("importNode", func(call goja.FunctionCall) goja.Value {
		return b.wrap(cloneNode(b.node(call.Argument(0)), call.Argument(1).ToBoolean()))
	})
	d.Set("adoptNode", func(call goja.FunctionCall) goja.Value { return call.Argument(0) })
	d.Set("contains", func(call goja.FunctionCall) goja.Value {
		return b.vm.ToValue(contains(b.root, b.node(call.Argument(0))))
	})
	d.Set("execCommand", func(goja.FunctionCall) goja.Value { return b.vm.ToValue(false) })
	d.Set("queryCommandState", func(goja.FunctionCall) goja.Value { return b.vm.ToValue(false) })
	d.Set("write", func(goja.FunctionCall) goja.Value { return goja.Undefined() })
	d.Set("writeln", func(goja.FunctionCall) goja.Value { return goja.Undefined() })
	d.Set("open", func(goja.FunctionCall) goja.Value { return d })
	d.Set("close", func(goja.FunctionCall) goja.Value { return goja.Undefined() })
	b.accessor(d, "currentScript", func() goja.Value { return goja.Null() }, nil)
	b.accessor(d, "activeElement", func() goja.Value { return b.wrap(dom.Find(b.root, "body")) }, nil)
	b.accessor(d, "scrollingElement", func() goja.Value { return b.wrap(dom.Find(b.root, "html")) }, nil)
	if p := b.protos["Document"]; p != nil {
		_ = d.SetPrototype(p)
	}
	return d
}

// setTitle updates (or creates) the document <title> text.
func (b *binder) setTitle(text string) {
	t := dom.Find(b.root, "title")
	if t == nil {
		head := dom.Find(b.root, "head")
		if head == nil {
			return
		}
		t = dom.NewElement("title")
		dom.AppendChild(head, t)
	}
	dom.SetTextContent(t, text)
}

// --- selector / tree helpers -------------------------------------------------

func matchesAny(sels []css.Selector, n *dom.Node) bool {
	for _, s := range sels {
		if s.Matches(n) {
			return true
		}
	}
	return false
}

func matchesSelector(n *dom.Node, selector string) bool {
	return matchesAny(css.ParseSelectorList(selector), n)
}

func byID(root *dom.Node, id string) *dom.Node {
	var found *dom.Node
	var walk func(n *dom.Node)
	walk = func(n *dom.Node) {
		if found != nil {
			return
		}
		if n.Type == dom.Element && n.ID() == id {
			found = n
			return
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(root)
	return found
}

func byTag(root *dom.Node, tag string) []*dom.Node {
	var out []*dom.Node
	all := tag == "*"
	var walk func(n *dom.Node)
	walk = func(n *dom.Node) {
		for _, c := range n.Children {
			if c.Type == dom.Element && (all || c.Tag == tag) {
				out = append(out, c)
			}
			walk(c)
		}
	}
	walk(root)
	return out
}

func byClass(root *dom.Node, want []string) []*dom.Node {
	var out []*dom.Node
	var walk func(n *dom.Node)
	walk = func(n *dom.Node) {
		for _, c := range n.Children {
			if c.Type == dom.Element && hasAllClasses(c, want) {
				out = append(out, c)
			}
			walk(c)
		}
	}
	walk(root)
	return out
}

func hasAllClasses(n *dom.Node, want []string) bool {
	if len(want) == 0 {
		return false
	}
	_, set := classSet(n)
	for _, w := range want {
		if !set[w] {
			return false
		}
	}
	return true
}

func byName(root *dom.Node, name string) []*dom.Node {
	var out []*dom.Node
	var walk func(n *dom.Node)
	walk = func(n *dom.Node) {
		for _, c := range n.Children {
			if c.Type == dom.Element {
				if v, ok := c.Attribute("name"); ok && v == name {
					out = append(out, c)
				}
			}
			walk(c)
		}
	}
	walk(root)
	return out
}

func elementChildren(n *dom.Node) []*dom.Node {
	var out []*dom.Node
	for _, c := range n.Children {
		if c.Type == dom.Element {
			out = append(out, c)
		}
	}
	return out
}

func elementParent(n *dom.Node) *dom.Node {
	p := n.Parent
	for p != nil && p.Type != dom.Element {
		p = p.Parent
	}
	return p
}

func firstChild(n *dom.Node) *dom.Node {
	if len(n.Children) == 0 {
		return nil
	}
	return n.Children[0]
}

func lastChild(n *dom.Node) *dom.Node {
	if len(n.Children) == 0 {
		return nil
	}
	return n.Children[len(n.Children)-1]
}

func firstElementChild(n *dom.Node) *dom.Node {
	for _, c := range n.Children {
		if c.Type == dom.Element {
			return c
		}
	}
	return nil
}

func lastElementChild(n *dom.Node) *dom.Node {
	for i := len(n.Children) - 1; i >= 0; i-- {
		if n.Children[i].Type == dom.Element {
			return n.Children[i]
		}
	}
	return nil
}

func siblings(n *dom.Node) []*dom.Node {
	if n.Parent == nil {
		return nil
	}
	return n.Parent.Children
}

func nextSibling(n *dom.Node) *dom.Node {
	sib := siblings(n)
	for i, c := range sib {
		if c == n && i+1 < len(sib) {
			return sib[i+1]
		}
	}
	return nil
}

func prevSibling(n *dom.Node) *dom.Node {
	sib := siblings(n)
	for i, c := range sib {
		if c == n && i > 0 {
			return sib[i-1]
		}
	}
	return nil
}

func nextElementSibling(n *dom.Node) *dom.Node {
	sib := siblings(n)
	seen := false
	for _, c := range sib {
		if seen && c.Type == dom.Element {
			return c
		}
		if c == n {
			seen = true
		}
	}
	return nil
}

func prevElementSibling(n *dom.Node) *dom.Node {
	sib := siblings(n)
	var prev *dom.Node
	for _, c := range sib {
		if c == n {
			return prev
		}
		if c.Type == dom.Element {
			prev = c
		}
	}
	return nil
}

func contains(root, target *dom.Node) bool {
	if target == nil {
		return false
	}
	for cur := target; cur != nil; cur = cur.Parent {
		if cur == root {
			return true
		}
	}
	return false
}

func rooted(n, root *dom.Node) bool { return contains(root, n) }

// isEqualNode reports whether a and b are the same TYPE of node with the same
// tag/attributes (elements) or data (text), and equal children in the same
// order, recursively — a structural comparison, unlike `===`/isSameNode's
// reference identity. Two nil nodes (both representing JS null) are equal;
// exactly one nil is not. Confirmed load-bearing live: react.dev's hydration/
// reconciliation calls `node.isEqualNode(...)` on real DOM nodes eight times
// per render — entirely missing before this (this engine had no
// Node.prototype method by this name at all), each call threw a TypeError.
// Fixing it is a real, independently-confirmed bug fix (found via a corpus-
// wide sweep of Engine.JSLog output, not from chasing any one visual
// symptom) — checked, and it is NOT the cause of react.dev's separate,
// already-documented reskin/orphaned-node gap (rounds 10/19): that washed-
// out-prose symptom is still present, unchanged, after this fix. The two
// are independent defects that happen to both live in the same settle path.
func isEqualNode(a, b *dom.Node) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.Type != b.Type || a.Tag != b.Tag || a.Text != b.Text {
		return false
	}
	if len(a.Attr) != len(b.Attr) {
		return false
	}
	for k, v := range a.Attr {
		if bv, ok := b.Attr[k]; !ok || bv != v {
			return false
		}
	}
	if len(a.Children) != len(b.Children) {
		return false
	}
	for i, ca := range a.Children {
		if !isEqualNode(ca, b.Children[i]) {
			return false
		}
	}
	return true
}

func cloneNode(n *dom.Node, deep bool) *dom.Node {
	c := &dom.Node{Type: n.Type, Tag: n.Tag, Text: n.Text}
	if n.Attr != nil {
		c.Attr = map[string]string{}
		for k, v := range n.Attr {
			c.Attr[k] = v
		}
	}
	if deep {
		for _, ch := range n.Children {
			dom.AppendChild(c, cloneNode(ch, true))
		}
	}
	return c
}
