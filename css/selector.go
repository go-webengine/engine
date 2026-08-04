// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package css

import (
	"strings"

	"github.com/go-webengine/engine/dom"
)

// Selector is a single compound selector: an optional tag, zero or more
// classes, and an optional id. Combinators (descendant, child, sibling) and
// pseudo-classes are not modelled in Phase 0; the parser drops anything it does
// not understand, so a descendant selector "a b" keeps only its last compound.
type Selector struct {
	Tag     string // "" means universal
	Classes []string
	ID      string
}

// Specificity returns the (a, b, c) specificity packed into a single int, with
// a = id count, b = class count, c = tag count, in the usual 0..99 base.
func (s Selector) Specificity() int {
	a, b, c := 0, 0, 0
	if s.ID != "" {
		a = 1
	}
	b = len(s.Classes)
	if s.Tag != "" {
		c = 1
	}
	return a*10000 + b*100 + c
}

// Matches reports whether the selector matches the element node.
func (s Selector) Matches(n *dom.Node) bool {
	if n.Type != dom.Element {
		return false
	}
	if s.Tag != "" && s.Tag != n.Tag {
		return false
	}
	if s.ID != "" && s.ID != n.ID() {
		return false
	}
	if len(s.Classes) > 0 {
		have := map[string]bool{}
		for _, c := range n.Classes() {
			have[c] = true
		}
		for _, want := range s.Classes {
			if !have[want] {
				return false
			}
		}
	}
	return true
}

// ParseSelectorList parses a comma-separated selector list, skipping empty and
// unparseable entries.
func ParseSelectorList(s string) []Selector {
	var out []Selector
	for _, part := range strings.Split(s, ",") {
		if sel, ok := parseCompound(part); ok {
			out = append(out, sel)
		}
	}
	return out
}

// parseCompound parses one selector. A descendant/child combinator is reduced
// to its rightmost compound (the key selector), which keeps behaviour sane
// without full combinator support.
func parseCompound(s string) (Selector, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Selector{}, false
	}
	// Reduce combinators: keep the last space/>-separated token.
	s = strings.ReplaceAll(s, ">", " ")
	s = strings.ReplaceAll(s, "+", " ")
	s = strings.ReplaceAll(s, "~", " ")
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return Selector{}, false // selector was only combinators
	}
	key := fields[len(fields)-1]
	return parseSimple(key)
}

// parseSimple parses a single compound with no combinators, e.g. "a.foo#bar".
func parseSimple(s string) (Selector, bool) {
	var sel Selector
	// Drop pseudo-classes/elements (":hover", "::before").
	if i := strings.IndexByte(s, ':'); i >= 0 {
		s = s[:i]
	}
	if s == "*" {
		return Selector{}, true // universal, matches any element
	}
	i := 0
	// Leading tag (letters/digits, no . or #).
	for i < len(s) && s[i] != '.' && s[i] != '#' {
		i++
	}
	sel.Tag = strings.ToLower(s[:i])
	// Remaining .class / #id tokens.
	for i < len(s) {
		kind := s[i]
		i++
		start := i
		for i < len(s) && s[i] != '.' && s[i] != '#' {
			i++
		}
		name := s[start:i]
		if name == "" {
			continue
		}
		switch kind {
		case '.':
			sel.Classes = append(sel.Classes, name)
		case '#':
			sel.ID = name
		}
	}
	if sel.Tag == "" && sel.ID == "" && len(sel.Classes) == 0 {
		return Selector{}, false
	}
	return sel, true
}
