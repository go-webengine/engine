// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package css

import (
	"strings"

	"github.com/go-webengine/engine/dom"
)

// combinator is the relationship between two adjacent compound selectors.
type combinator uint8

const (
	combDescendant combinator = iota // "a b"  — b is a descendant of a
	combChild                        // "a > b" — b is a child of a
	combAdjacent                     // "a + b" — b immediately follows a
	combSibling                      // "a ~ b" — b follows a among siblings
)

// compound is a single compound selector: an optional tag, zero or more
// classes and an optional id. A universal ("*") compound has all fields empty.
type compound struct {
	Tag     string
	Classes []string
	ID      string
	Univ    bool // explicit "*" (matches any element)
	Root    bool // ":root" pseudo-class (matches the document root element)
}

// matches reports whether the compound matches an element node.
func (c compound) matches(n *dom.Node) bool {
	if n.Type != dom.Element {
		return false
	}
	// ":root" matches only the document root element (an element with no element
	// ancestor, i.e. <html>). This is where stylesheets define most custom
	// properties, so honouring it is essential for var() to resolve.
	if c.Root && elementParent(n) != nil {
		return false
	}
	if c.Tag != "" && c.Tag != n.Tag {
		return false
	}
	if c.ID != "" && c.ID != n.ID() {
		return false
	}
	if len(c.Classes) > 0 {
		have := map[string]bool{}
		for _, cl := range n.Classes() {
			have[cl] = true
		}
		for _, want := range c.Classes {
			if !have[want] {
				return false
			}
		}
	}
	return true
}

func (c compound) specificity() (idCount, classCount, tagCount int) {
	if c.ID != "" {
		idCount = 1
	}
	classCount = len(c.Classes)
	if c.Root {
		classCount++ // a pseudo-class contributes class-level specificity
	}
	if c.Tag != "" {
		tagCount = 1
	}
	return
}

// Selector is a full complex selector: a chain of compound selectors joined by
// combinators. parts is in source order (left to right); parts[len-1] is the
// key selector matched against the candidate element, and each preceding part
// constrains an ancestor or sibling via combs[i] (the combinator between
// parts[i] and parts[i+1]).
type Selector struct {
	parts []compound
	combs []combinator
}

// Specificity returns the (a, b, c) specificity packed into a single int, with
// a = id count, b = class count, c = tag count, summed over all compounds.
func (s Selector) Specificity() int {
	var a, b, c int
	for _, p := range s.parts {
		id, cl, tg := p.specificity()
		a, b, c = a+id, b+cl, c+tg
	}
	return a*10000 + b*100 + c
}

// Matches reports whether the selector matches element n, evaluating the
// combinator chain from the key selector leftwards.
func (s Selector) Matches(n *dom.Node) bool {
	if len(s.parts) == 0 {
		return false
	}
	key := s.parts[len(s.parts)-1]
	if !key.matches(n) {
		return false
	}
	return s.matchLeft(len(s.parts)-2, n)
}

// matchLeft verifies that part index i (and everything to its left) is
// satisfied relative to the already-matched node matched (which corresponds to
// part i+1). i < 0 means the whole chain is satisfied.
func (s Selector) matchLeft(i int, matched *dom.Node) bool {
	if i < 0 {
		return true
	}
	comb := s.combs[i] // combinator between parts[i] and parts[i+1]
	part := s.parts[i]
	switch comb {
	case combChild:
		p := elementParent(matched)
		return p != nil && part.matches(p) && s.matchLeft(i-1, p)
	case combDescendant:
		for p := elementParent(matched); p != nil; p = elementParent(p) {
			if part.matches(p) && s.matchLeft(i-1, p) {
				return true
			}
		}
		return false
	case combAdjacent:
		prev := prevElementSibling(matched)
		return prev != nil && part.matches(prev) && s.matchLeft(i-1, prev)
	case combSibling:
		for prev := prevElementSibling(matched); prev != nil; prev = prevElementSibling(prev) {
			if part.matches(prev) && s.matchLeft(i-1, prev) {
				return true
			}
		}
		return false
	}
	return false
}

func elementParent(n *dom.Node) *dom.Node {
	p := n.Parent
	for p != nil && p.Type != dom.Element {
		p = p.Parent
	}
	return p
}

func prevElementSibling(n *dom.Node) *dom.Node {
	if n.Parent == nil {
		return nil
	}
	var prev *dom.Node
	for _, c := range n.Parent.Children {
		if c == n {
			return prev
		}
		if c.Type == dom.Element {
			prev = c
		}
	}
	return prev
}

// ParseSelectorList parses a comma-separated selector list, skipping empty and
// unparseable entries.
func ParseSelectorList(s string) []Selector {
	var out []Selector
	for _, part := range strings.Split(s, ",") {
		if sel, ok := parseComplex(part); ok {
			out = append(out, sel)
		}
	}
	return out
}

// parseComplex parses one complex selector into a compound chain, keeping the
// combinators. An unknown compound makes the whole selector fail (skipped).
func parseComplex(s string) (Selector, bool) {
	toks := tokenizeSelector(s)
	if len(toks) == 0 {
		return Selector{}, false
	}
	if toks[0].comb || toks[len(toks)-1].comb {
		return Selector{}, false // leading/trailing combinator is malformed
	}
	var sel Selector
	pendingComb := combDescendant
	haveComb := false
	for _, t := range toks {
		if t.comb {
			if haveComb {
				return Selector{}, false // two combinators in a row
			}
			pendingComb, haveComb = t.combKind, true
			continue
		}
		c, ok := parseSimple(t.text)
		if !ok {
			return Selector{}, false
		}
		if len(sel.parts) > 0 {
			sel.combs = append(sel.combs, pendingComb)
		}
		sel.parts = append(sel.parts, c)
		pendingComb, haveComb = combDescendant, false
	}
	if len(sel.parts) == 0 {
		return Selector{}, false
	}
	return sel, true
}

type selToken struct {
	text     string
	comb     bool
	combKind combinator
}

// tokenizeSelector splits a complex selector into compound tokens and explicit
// combinator tokens (>, +, ~). Whitespace between compounds is a descendant
// combinator; whitespace surrounding an explicit combinator is folded away.
func tokenizeSelector(s string) []selToken {
	var toks []selToken
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			toks = append(toks, selToken{text: cur.String()})
			cur.Reset()
		}
	}
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch ch {
		case ' ', '\t', '\n', '\r', '\f':
			flush()
			if n := len(toks); n > 0 && !toks[n-1].comb {
				toks = append(toks, selToken{comb: true, combKind: combDescendant})
			}
		case '>', '+', '~':
			flush()
			kind := combChild
			if ch == '+' {
				kind = combAdjacent
			} else if ch == '~' {
				kind = combSibling
			}
			// Fold a preceding whitespace-descendant into this explicit one.
			if n := len(toks); n > 0 && toks[n-1].comb && toks[n-1].combKind == combDescendant {
				toks = toks[:n-1]
			}
			toks = append(toks, selToken{comb: true, combKind: kind})
		default:
			cur.WriteByte(ch)
		}
	}
	flush()
	if n := len(toks); n > 0 && toks[n-1].comb && toks[n-1].combKind == combDescendant {
		toks = toks[:n-1] // trailing whitespace produced a dangling combinator
	}
	return toks
}

// parseSimple parses a single compound with no combinators, e.g. "a.foo#bar".
func parseSimple(s string) (compound, bool) {
	var c compound
	// Handle pseudo-classes/elements. ":root" is honoured (it selects the
	// document root, where custom properties are typically declared); every
	// other pseudo (":hover", "::before", …) is dropped as unmodelled.
	if i := strings.IndexByte(s, ':'); i >= 0 {
		for _, p := range strings.Split(strings.ToLower(s[i:]), ":") {
			if p == "root" {
				c.Root = true
			}
		}
		s = s[:i]
	}
	// Drop attribute selectors ("[type=text]"): the constraint is not modelled,
	// so the compound reduces to its tag/class/id prefix.
	if i := strings.IndexByte(s, '['); i >= 0 {
		s = s[:i]
	}
	if s == "*" {
		return compound{Univ: true, Root: c.Root}, true
	}
	i := 0
	for i < len(s) && s[i] != '.' && s[i] != '#' {
		i++
	}
	c.Tag = strings.ToLower(s[:i])
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
			c.Classes = append(c.Classes, name)
		case '#':
			c.ID = name
		}
	}
	if c.Tag == "" && c.ID == "" && len(c.Classes) == 0 && !c.Root {
		return compound{}, false
	}
	return c, true
}
