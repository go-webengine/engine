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
	// Dynamic is set when the compound carries a dynamic (interaction) pseudo-
	// class — :hover, :active, :focus, :focus-within, :focus-visible or :target.
	// A static full-page render has nothing hovered/focused/targeted, so such a
	// compound never matches (its non-dynamic siblings in a selector list still
	// apply, and its non-dynamic parts on other compounds are unaffected).
	Dynamic bool
}

// matches reports whether the compound matches an element node.
func (c compound) matches(n *dom.Node) bool {
	if n.Type != dom.Element {
		return false
	}
	// A dynamic pseudo-class can never match at static render time.
	if c.Dynamic {
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
	if c.Dynamic {
		classCount++ // a dynamic pseudo-class also contributes class-level weight
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
// unparseable entries. Commas inside functional pseudo-classes (`:is(a, b)`) are
// respected (not treated as list separators), and `:is()` / `:where()` /
// `:matches()` wrappers are expanded into plain selectors — the form modern
// Tailwind emits for its dark-mode and variant rules
// (`:is(.dark .dark\:bg-wash-dark)`).
func ParseSelectorList(s string) []Selector {
	var out []Selector
	for _, part := range splitSelectorCommas(s) {
		for _, expanded := range expandFunctionalPseudos(part) {
			if sel, ok := parseComplex(expanded); ok {
				out = append(out, sel)
			}
		}
	}
	return out
}

// splitTopLevelComma splits a selector list on commas that are not nested inside
// parentheses or square brackets.
func splitSelectorCommas(s string) []string {
	var parts []string
	depth := 0
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++ // skip escaped char
		case '(', '[':
			depth++
		case ')', ']':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				parts = append(parts, s[start:i])
				start = i + 1
			}
		}
	}
	return append(parts, s[start:])
}

// expandFunctionalPseudos rewrites a single complex selector so that the first
// `:is(...)` / `:where(...)` / `:matches(...)` it contains is spliced out: each
// alternative inside the parentheses is substituted in place, and the result is
// expanded again (so nested/multiple wrappers resolve). Splicing works whether
// the wrapper is a compound part (`h1:is(.a,.b)` → `h1.a`, `h1.b`) or sits in a
// descendant chain (`:is(.dark .foo)` → `.dark .foo`). Specificity of `:where`
// is approximated as its argument's (a minor over-count that does not affect
// these pages). Selectors with no such wrapper are returned unchanged.
func expandFunctionalPseudos(sel string) []string {
	open, argOpen := findFunctionalPseudo(sel)
	if open < 0 {
		return []string{sel}
	}
	close, ok := matchParenAt(sel, argOpen)
	if !ok {
		return []string{sel} // malformed; leave as-is (will likely fail to parse)
	}
	prefix := sel[:open]
	suffix := sel[close+1:]
	inner := sel[argOpen+1 : close]
	var out []string
	for _, alt := range splitSelectorCommas(inner) {
		alt = strings.TrimSpace(alt)
		if alt == "" {
			continue
		}
		out = append(out, expandFunctionalPseudos(prefix+alt+suffix)...)
	}
	if len(out) == 0 {
		// An empty :is()/:where() matches nothing; drop the wrapper's effect by
		// keeping the surrounding selector.
		return expandFunctionalPseudos(prefix + suffix)
	}
	return out
}

// findFunctionalPseudo returns the index of the ':' starting the first
// :is(/:where(/:matches( pseudo (unescaped), and the index of its '('. It
// returns (-1,-1) when none is present.
func findFunctionalPseudo(s string) (colon, paren int) {
	ls := strings.ToLower(s)
	for _, name := range []string{":is(", ":where(", ":matches("} {
		if i := indexUnescaped(ls, name); i >= 0 {
			return i, i + len(name) - 1
		}
	}
	return -1, -1
}

// indexUnescaped returns the index of the first occurrence of sub in s that is
// not immediately preceded by a backslash.
func indexUnescaped(s, sub string) int {
	from := 0
	for {
		i := strings.Index(s[from:], sub)
		if i < 0 {
			return -1
		}
		abs := from + i
		if abs == 0 || s[abs-1] != '\\' {
			return abs
		}
		from = abs + 1
	}
}

// matchParenAt returns the index of the ')' matching the '(' at open.
func matchParenAt(s string, open int) (int, bool) {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i, true
			}
		}
	}
	return 0, false
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
// It respects CSS identifier escapes: a backslash escapes the next character, so
// a class token like `.dark\:bg-wash-dark` (Tailwind's `dark:`, `sm:`, `lg:`
// variant syntax) is parsed as the single class `dark:bg-wash-dark` rather than
// being truncated at the colon. Without this, no Tailwind variant class matches.
func parseSimple(s string) (compound, bool) {
	var c compound
	// Handle pseudo-classes/elements at the first UNescaped ':'. ":root" is
	// honoured (it selects the document root, where custom properties are
	// typically declared); the dynamic interaction pseudo-classes mark the
	// compound so it never matches in a static render; every other pseudo
	// (":nth-child", "::before", …) is dropped as unmodelled.
	if base, pseudo, ok := splitUnescaped(s, ':'); ok {
		for _, p := range strings.Split(strings.ToLower(pseudo), ":") {
			if p == "root" {
				c.Root = true
			}
			if isDynamicPseudo(p) {
				c.Dynamic = true
			}
		}
		s = base
	}
	// Drop attribute selectors ("[type=text]"): the constraint is not modelled,
	// so the compound reduces to its tag/class/id prefix.
	if base, _, ok := splitUnescaped(s, '['); ok {
		s = base
	}
	if s == "*" {
		return compound{Univ: true, Root: c.Root, Dynamic: c.Dynamic}, true
	}
	tag, parts := scanCompound(s)
	c.Tag = strings.ToLower(tag)
	for _, p := range parts {
		if p.name == "" {
			continue
		}
		switch p.kind {
		case '.':
			c.Classes = append(c.Classes, p.name)
		case '#':
			c.ID = p.name
		}
	}
	// A compound with no tag/class/id/:root and no universal reduces to nothing —
	// a bare ":hover" or "::before" is dropped (an unattached dynamic pseudo would
	// select nothing in a static render anyway, so failing to parse it is
	// equivalent to it never matching).
	if c.Tag == "" && c.ID == "" && len(c.Classes) == 0 && !c.Root {
		return compound{}, false
	}
	return c, true
}

// splitUnescaped splits s at the first occurrence of sep that is not preceded by
// a backslash escape, returning the text before it, the text from sep onward,
// and whether a separator was found.
func splitUnescaped(s string, sep byte) (before, after string, found bool) {
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' {
			i++ // skip the escaped character
			continue
		}
		if s[i] == sep {
			return s[:i], s[i:], true
		}
	}
	return s, "", false
}

type compoundPart struct {
	kind byte // '.' for class, '#' for id
	name string
}

// scanCompound splits a compound base (no pseudo, no attribute) into its leading
// tag and its class/id parts, unescaping backslash escapes in each identifier.
// An unescaped '.' or '#' starts a new class/id; an escaped one is a literal
// character of the current identifier.
func scanCompound(s string) (tag string, parts []compoundPart) {
	var cur strings.Builder
	kind := byte(0) // 0 == the leading tag
	flush := func() {
		if kind == 0 {
			tag = cur.String()
		} else {
			parts = append(parts, compoundPart{kind: kind, name: cur.String()})
		}
		cur.Reset()
	}
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch == '\\' && i+1 < len(s) {
			cur.WriteByte(s[i+1]) // literal escaped character
			i++
			continue
		}
		if ch == '.' || ch == '#' {
			flush()
			kind = ch
			continue
		}
		cur.WriteByte(ch)
	}
	flush()
	return tag, parts
}

// isDynamicPseudo reports whether a pseudo token (already lower-cased, with any
// leading colon stripped by the caller's split) is a dynamic interaction pseudo-
// class that cannot match in a static screenshot. Functional forms keep their
// argument list (e.g. "focus-within"); we match on the leading keyword only.
func isDynamicPseudo(p string) bool {
	switch p {
	case "hover", "active", "focus", "focus-within", "focus-visible", "target":
		return true
	}
	return false
}
