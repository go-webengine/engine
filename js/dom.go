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

	// Layout/geometry stubs: enough for scripts that probe them without crashing.
	o.Set("getBoundingClientRect", func(goja.FunctionCall) goja.Value { return b.zeroRect() })
	o.Set("getClientRects", func(goja.FunctionCall) goja.Value { return b.vm.NewArray() })
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
	for _, z := range []string{"offsetWidth", "offsetHeight", "clientWidth", "clientHeight", "scrollWidth", "scrollHeight", "scrollTop", "scrollLeft", "offsetTop", "offsetLeft"} {
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
	d.Set("write", func(goja.FunctionCall) goja.Value { return goja.Undefined() })
	d.Set("writeln", func(goja.FunctionCall) goja.Value { return goja.Undefined() })
	d.Set("open", func(goja.FunctionCall) goja.Value { return d })
	d.Set("close", func(goja.FunctionCall) goja.Value { return goja.Undefined() })
	b.accessor(d, "currentScript", func() goja.Value { return goja.Null() }, nil)
	b.accessor(d, "activeElement", func() goja.Value { return b.wrap(dom.Find(b.root, "body")) }, nil)
	b.accessor(d, "scrollingElement", func() goja.Value { return b.wrap(dom.Find(b.root, "html")) }, nil)
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
