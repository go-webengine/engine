// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package js

import (
	"strings"

	"github.com/dop251/goja"
	"github.com/go-webengine/engine/dom"
)

// classSet returns the element's ordered class list and a membership set.
func classSet(n *dom.Node) ([]string, map[string]bool) {
	classes := n.Classes()
	set := make(map[string]bool, len(classes))
	for _, c := range classes {
		set[c] = true
	}
	return classes, set
}

// writeClasses stores the ordered class list back onto the element's class attr.
func writeClasses(n *dom.Node, classes []string) {
	if n.Attr == nil {
		n.Attr = map[string]string{}
	}
	n.Attr["class"] = strings.Join(classes, " ")
}

// classAdd adds each name to the element's class list (idempotent, order-stable).
func classAdd(n *dom.Node, names ...string) {
	classes, set := classSet(n)
	for _, name := range names {
		if name == "" || set[name] {
			continue
		}
		classes = append(classes, name)
		set[name] = true
	}
	writeClasses(n, classes)
}

// classRemove drops each name from the element's class list.
func classRemove(n *dom.Node, names ...string) {
	drop := make(map[string]bool, len(names))
	for _, name := range names {
		drop[name] = true
	}
	classes, _ := classSet(n)
	out := classes[:0:0]
	for _, c := range classes {
		if !drop[c] {
			out = append(out, c)
		}
	}
	writeClasses(n, out)
}

// classContains reports whether name is present in the element's class list.
func classContains(n *dom.Node, name string) bool {
	_, set := classSet(n)
	return set[name]
}

// classToggle flips name's presence and returns whether it is now present.
func classToggle(n *dom.Node, name string) bool {
	if classContains(n, name) {
		classRemove(n, name)
		return false
	}
	classAdd(n, name)
	return true
}

// newClassList builds the DOMTokenList object exposed as element.classList,
// reflecting live through to the element's class attribute.
func (b *binder) newClassList(n *dom.Node) goja.Value {
	o := b.vm.NewObject()
	o.Set("add", func(call goja.FunctionCall) goja.Value {
		classAdd(n, argStrings(call)...)
		return goja.Undefined()
	})
	o.Set("remove", func(call goja.FunctionCall) goja.Value {
		classRemove(n, argStrings(call)...)
		return goja.Undefined()
	})
	o.Set("toggle", func(call goja.FunctionCall) goja.Value {
		name := call.Argument(0).String()
		// Two-arg form: force is a boolean deciding presence.
		if len(call.Arguments) >= 2 {
			force := call.Argument(1).ToBoolean()
			if force {
				classAdd(n, name)
			} else {
				classRemove(n, name)
			}
			return b.vm.ToValue(force)
		}
		return b.vm.ToValue(classToggle(n, name))
	})
	o.Set("contains", func(call goja.FunctionCall) goja.Value {
		return b.vm.ToValue(classContains(n, call.Argument(0).String()))
	})
	o.Set("replace", func(call goja.FunctionCall) goja.Value {
		old, neu := call.Argument(0).String(), call.Argument(1).String()
		if classContains(n, old) {
			classRemove(n, old)
			classAdd(n, neu)
			return b.vm.ToValue(true)
		}
		return b.vm.ToValue(false)
	})
	o.Set("item", func(call goja.FunctionCall) goja.Value {
		classes, _ := classSet(n)
		i := int(call.Argument(0).ToInteger())
		if i < 0 || i >= len(classes) {
			return goja.Null()
		}
		return b.vm.ToValue(classes[i])
	})
	b.accessor(o, "length", func() goja.Value {
		classes, _ := classSet(n)
		return b.vm.ToValue(len(classes))
	}, nil)
	b.accessor(o, "value", func() goja.Value {
		v, _ := n.Attribute("class")
		return b.vm.ToValue(v)
	}, func(v goja.Value) {
		writeClasses(n, strings.Fields(v.String()))
	})
	return o
}

// parseInlineStyle splits an inline style attribute into ordered declarations.
func parseInlineStyle(s string) [][2]string { return dom.ParseInlineStyle(s) }

// serializeInlineStyle renders ordered declarations back to a style attribute.
func serializeInlineStyle(decls [][2]string) string { return dom.SerializeInlineStyle(decls) }

// camelToKebab converts a CSS-style camelCase property (backgroundColor) to its
// hyphenated form (background-color). Already-hyphenated names pass through.
func camelToKebab(s string) string {
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			sb.WriteByte('-')
			sb.WriteByte(c - 'A' + 'a')
		} else {
			sb.WriteByte(c)
		}
	}
	return sb.String()
}

// styleProp reads a single inline property value ("" if unset).
func styleProp(n *dom.Node, prop string) string { return n.StyleProperty(prop) }

// setStyleProp sets (or, for empty val, removes) a single inline property,
// preserving declaration order.
func setStyleProp(n *dom.Node, prop, val string) { n.SetStyleProperty(prop, val) }

// styleDynObj implements goja.DynamicObject so that both camelCase property
// access (el.style.display = "none") and the setProperty/cssText API mutate the
// element's inline style attribute live.
type styleDynObj struct {
	b *binder
	n *dom.Node
}

func (s *styleDynObj) Get(key string) goja.Value {
	switch key {
	case "cssText":
		v, _ := s.n.Attribute("style")
		return s.b.vm.ToValue(v)
	case "setProperty":
		return s.b.vm.ToValue(func(call goja.FunctionCall) goja.Value {
			setStyleProp(s.n, call.Argument(0).String(), call.Argument(1).String())
			return goja.Undefined()
		})
	case "getPropertyValue":
		return s.b.vm.ToValue(func(call goja.FunctionCall) goja.Value {
			return s.b.vm.ToValue(styleProp(s.n, strings.ToLower(call.Argument(0).String())))
		})
	case "removeProperty":
		return s.b.vm.ToValue(func(call goja.FunctionCall) goja.Value {
			prop := strings.ToLower(call.Argument(0).String())
			old := styleProp(s.n, prop)
			setStyleProp(s.n, prop, "")
			return s.b.vm.ToValue(old)
		})
	default:
		return s.b.vm.ToValue(styleProp(s.n, camelToKebab(key)))
	}
}

func (s *styleDynObj) Set(key string, val goja.Value) bool {
	if key == "cssText" {
		if s.n.Attr == nil {
			s.n.Attr = map[string]string{}
		}
		s.n.Attr["style"] = val.String()
		return true
	}
	setStyleProp(s.n, camelToKebab(key), val.String())
	return true
}

func (s *styleDynObj) Has(key string) bool {
	switch key {
	case "cssText", "setProperty", "getPropertyValue", "removeProperty":
		return true
	}
	return styleProp(s.n, camelToKebab(key)) != ""
}

func (s *styleDynObj) Delete(key string) bool {
	setStyleProp(s.n, camelToKebab(key), "")
	return true
}

func (s *styleDynObj) Keys() []string {
	cur, _ := s.n.Attribute("style")
	decls := parseInlineStyle(cur)
	out := make([]string, 0, len(decls))
	for _, d := range decls {
		out = append(out, d[0])
	}
	return out
}

// argStrings returns every argument of a call coerced to string.
func argStrings(call goja.FunctionCall) []string {
	out := make([]string, 0, len(call.Arguments))
	for _, a := range call.Arguments {
		out = append(out, a.String())
	}
	return out
}
