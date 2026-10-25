// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package dom

import "strings"

// ParseInlineStyle splits an inline style attribute into ordered
// declarations, each a lowercased property and its trimmed value.
func ParseInlineStyle(s string) [][2]string {
	var out [][2]string
	for _, part := range strings.Split(s, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		colon := strings.IndexByte(part, ':')
		if colon < 0 {
			continue
		}
		prop := strings.TrimSpace(part[:colon])
		val := strings.TrimSpace(part[colon+1:])
		if prop == "" {
			continue
		}
		out = append(out, [2]string{strings.ToLower(prop), val})
	}
	return out
}

// SerializeInlineStyle renders ordered declarations back to a style
// attribute ("prop: value; prop: value").
func SerializeInlineStyle(decls [][2]string) string {
	var sb strings.Builder
	for i, d := range decls {
		if i > 0 {
			sb.WriteString("; ")
		}
		sb.WriteString(d[0])
		sb.WriteString(": ")
		sb.WriteString(d[1])
	}
	return sb.String()
}

// StyleProperty reads one property of the element's inline style ("" if
// unset); prop is a lowercased, hyphenated name (font-size).
func (n *Node) StyleProperty(prop string) string {
	cur, _ := n.Attribute("style")
	for _, d := range ParseInlineStyle(cur) {
		if d[0] == prop {
			return d[1]
		}
	}
	return ""
}

// SetStyleProperty sets (or, for an empty val, removes) one property of the
// element's inline style, keeping the declarations' order, as a script's
// el.style.fontSize = "…" or el.style.setProperty does (CSSOM §6.6.1).
func (n *Node) SetStyleProperty(prop, val string) {
	prop = strings.ToLower(strings.TrimSpace(prop))
	if prop == "" {
		return
	}
	cur, _ := n.Attribute("style")
	decls := ParseInlineStyle(cur)
	if val == "" {
		out := decls[:0]
		for _, d := range decls {
			if d[0] != prop {
				out = append(out, d)
			}
		}
		decls = out
	} else {
		found := false
		for i := range decls {
			if decls[i][0] == prop {
				decls[i][1] = val
				found = true
				break
			}
		}
		if !found {
			decls = append(decls, [2]string{prop, val})
		}
	}
	if n.Attr == nil {
		n.Attr = map[string]string{}
	}
	n.Attr["style"] = SerializeInlineStyle(decls)
}
