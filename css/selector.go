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
	// Checked is set by the ":checked" pseudo-class. At static render time there
	// is no user interaction, so an element is checked iff it carries the default
	// "checked" attribute (a checkbox/radio) — see isChecked. This is the pivot of
	// the CSS "checkbox hack" MediaWiki uses to keep collapsed dropdowns hidden.
	Checked bool
	// Not holds the compound selectors of every ":not(...)" attached to this
	// compound. The compound matches only when NONE of them matches the element.
	// A ":not()" argument that is a dynamic pseudo (never matches statically) is
	// dropped here, since negating an always-false selector imposes no constraint.
	Not []compound
	// PseudoElement is set when the compound carries a pseudo-ELEMENT (::before,
	// ::after, ::first-line, ::marker, ::placeholder, …). Unlike a pseudo-CLASS —
	// which only constrains WHICH real element matches, so an unmodelled one may
	// safely degrade to "no constraint" — a pseudo-element targets a GENERATED box
	// that is not the originating element. The engine does not synthesise those
	// boxes, so such a compound must match NOTHING. Degrading it to its base (as
	// the generic "reduce, don't drop" path does) would wrongly apply the
	// pseudo-element's declarations to the real element — e.g. a clearfix
	// `.wrap::after{height:0;overflow:hidden}` would collapse the actual `.wrap`.
	PseudoElement bool
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
	// A pseudo-element targets a generated box, not this element; since the engine
	// does not synthesise pseudo-element boxes, such a compound matches nothing.
	if c.PseudoElement {
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
	// ":checked" — at static render an input is checked iff it has the default
	// "checked" attribute (there is no user interaction to toggle it).
	if c.Checked && !isChecked(n) {
		return false
	}
	// ":not(...)" — the compound fails as soon as any negated selector matches.
	for i := range c.Not {
		if c.Not[i].matches(n) {
			return false
		}
	}
	return true
}

// isChecked reports whether element n is in the checked state at static render
// time: it carries the default "checked" attribute (checkbox/radio) or, for an
// <option>, the "selected" attribute. Value-less HTML boolean attributes are
// parsed by x/net/html to an empty-string value, so presence — not value — is
// what counts.
func isChecked(n *dom.Node) bool {
	if _, ok := n.Attribute("checked"); ok {
		return true
	}
	if _, ok := n.Attribute("selected"); ok {
		return true
	}
	return false
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
	if c.Checked {
		classCount++ // ":checked" is a pseudo-class (class-level weight)
	}
	if c.Tag != "" {
		tagCount = 1
	}
	// ":not(...)" contributes the specificity of its most specific argument.
	var na, nb, nc int
	for _, notC := range c.Not {
		id, cl, tg := notC.specificity()
		if id*10000+cl*100+tg > na*10000+nb*100+nc {
			na, nb, nc = id, cl, tg
		}
	}
	idCount += na
	classCount += nb
	tagCount += nc
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
// Whitespace and combinator characters inside an attribute selector "[...]" or a
// functional pseudo's "(...)" (e.g. the space/comma in ":not(.a, .b)" or a nested
// "~") are literal — they must not split the compound — so they are tracked by
// bracket/paren depth and written through verbatim.
func tokenizeSelector(s string) []selToken {
	var toks []selToken
	var cur strings.Builder
	depth := 0
	flush := func() {
		if cur.Len() > 0 {
			toks = append(toks, selToken{text: cur.String()})
			cur.Reset()
		}
	}
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch == '\\' && i+1 < len(s) { // escape: the next char is always literal
			cur.WriteByte(ch)
			cur.WriteByte(s[i+1])
			i++
			continue
		}
		if depth > 0 { // inside [...] or (...): everything is literal
			cur.WriteByte(ch)
			if ch == '[' || ch == '(' {
				depth++
			} else if ch == ']' || ch == ')' {
				depth--
			}
			continue
		}
		switch ch {
		case '[', '(':
			depth++
			cur.WriteByte(ch)
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
	// Separate the compound's tag/class/id/attribute base from its pseudo tokens
	// at the first top-level (not inside [] or ()) ':'. ":root" is honoured (it
	// selects the document root, where custom properties are typically declared);
	// ":checked" and ":not(...)" are modelled (below); the dynamic interaction
	// pseudo-classes mark the compound so it never matches in a static render;
	// every other pseudo (":nth-child", "::before", …) is dropped as unmodelled —
	// the compound falls back to matching its base rather than dropping the rule.
	base, pseudos := splitPseudos(s)
	for _, p := range pseudos {
		if p == "" {
			continue // a "::"-style pseudo-element produced an empty token
		}
		name, arg := pseudoNameArg(p)
		switch name {
		case "root":
			c.Root = true
		case "checked":
			c.Checked = true
		case "not":
			// An unmodelled or empty ":not()" argument imposes NO constraint
			// rather than dropping the rule — the same "reduce, don't drop"
			// philosophy applied to unknown pseudos and bare "[attr]". This is
			// essential: sites gate their dark theme on rules like
			// `:root:not([data-theme]) { … }`; dropping such a rule (because the
			// attribute negation is unmodelled) would silently disable the theme.
			c.Not = append(c.Not, parseNotArg(arg)...)
		default:
			switch {
			case isDynamicPseudo(name):
				c.Dynamic = true
			case isPseudoElement(name):
				// A pseudo-element styles a generated box, not the real element, so
				// the compound must match nothing (see compound.PseudoElement).
				c.PseudoElement = true
			}
			// Any other pseudo (e.g. :nth-child) is unmodelled and intentionally
			// ignored — the compound degrades to matching its base.
		}
	}
	s = base
	// Drop attribute selectors ("[type=text]"): the constraint is not modelled,
	// so the compound reduces to its tag/class/id prefix.
	if base, _, ok := splitUnescaped(s, '['); ok {
		s = base
	}
	if s == "*" {
		c.Univ = true
		return c, true
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
	// A compound with no tag/class/id and no modelled pseudo reduces to nothing —
	// a bare "::before" is dropped. A bare ":hover"/":checked"/":not()" is kept:
	// a dynamic-only compound never matches statically (equivalent to being
	// dropped), and ":checked"/":not(...)" carry a real constraint on their own.
	if c.Tag == "" && c.ID == "" && len(c.Classes) == 0 &&
		!c.Root && !c.Dynamic && !c.Checked && len(c.Not) == 0 {
		return compound{}, false
	}
	return c, true
}

// splitPseudos separates a compound's base (its tag/class/id/attribute prefix)
// from its pseudo tokens. It splits at every top-level ':' — one that is not
// inside an attribute selector "[...]" or a functional pseudo's "(...)" and not
// backslash-escaped — so ":not(:checked)" stays one token and an attribute value
// containing a colon does not spuriously split. Each returned pseudo has its
// leading ':' stripped (a "::before" pseudo-element yields a leading empty token
// the caller skips); functional pseudos keep their "name(arg)" form.
func splitPseudos(s string) (base string, pseudos []string) {
	depth := 0
	firstColon := -1
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++ // skip the escaped character
		case '[', '(':
			depth++
		case ']', ')':
			if depth > 0 {
				depth--
			}
		case ':':
			if depth == 0 {
				firstColon = i
			}
		}
		if firstColon >= 0 {
			break
		}
	}
	if firstColon < 0 {
		return s, nil
	}
	base = s[:firstColon]
	rest := s[firstColon:] // begins with ':'
	depth = 0
	start := 0
	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case '\\':
			i++
		case '[', '(':
			depth++
		case ']', ')':
			if depth > 0 {
				depth--
			}
		case ':':
			if depth == 0 && i > start {
				pseudos = append(pseudos, strings.TrimPrefix(rest[start:i], ":"))
				start = i
			}
		}
	}
	pseudos = append(pseudos, strings.TrimPrefix(rest[start:], ":"))
	return base, pseudos
}

// pseudoNameArg splits a pseudo token into its lower-cased name and, for a
// functional pseudo, the raw argument between the parentheses ("not(:checked)" →
// "not", ":checked"). The argument keeps its original case (class names inside
// ":not()" are case-sensitive). A non-functional pseudo returns an empty arg.
func pseudoNameArg(p string) (name, arg string) {
	if i := strings.IndexByte(p, '('); i >= 0 {
		name = strings.ToLower(p[:i])
		arg = strings.TrimSuffix(p[i+1:], ")")
		return name, arg
	}
	return strings.ToLower(p), ""
}

// parseNotArg parses the argument of a ":not(...)" into the compound selectors
// the negation must test. The argument is a comma-separated list of simple
// selectors (CSS Level 4 allows a list; the checkbox hack uses a single simple
// selector such as ":checked" or ".foo"). It never fails the surrounding rule —
// an alternative that is empty, dynamic (":hover", never matches statically), or
// unmodelled (an attribute-only "[attr]" or a pseudo-element the parser can't
// reduce to a real constraint) simply contributes no constraint. Only genuine,
// modelled constraints (tag/class/id/:checked/nested :not) are returned, so
// `:not(:checked)` is precise while `:root:not([data-theme])` degrades to
// `:root` rather than dropping the rule.
func parseNotArg(arg string) []compound {
	var out []compound
	for _, alt := range splitSelectorCommas(arg) {
		alt = strings.TrimSpace(alt)
		if alt == "" {
			continue
		}
		neg, ok := parseSimple(alt)
		if !ok || neg.Dynamic {
			continue // unmodelled or always-true-statically → no constraint
		}
		out = append(out, neg)
	}
	return out
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

// isPseudoElement reports whether a pseudo token (lower-cased, colons stripped)
// names a pseudo-ELEMENT — a generated/sub box that is not the originating
// element. Both the CSS2 single-colon spellings (:before, :after, :first-line,
// :first-letter) and the CSS3 double-colon spellings collapse to the same token
// here, so one list covers both. The common vendor-prefixed form-control and
// scrollbar pseudo-elements are included because themes frequently size them,
// and applying that sizing to the real control would be wrong. Functional
// pseudo-elements (::part(), ::slotted(), ::highlight()) arrive name-only (the
// argument having been split off), so their bare names suffice.
func isPseudoElement(p string) bool {
	switch p {
	case "before", "after", "first-line", "first-letter", "marker", "placeholder",
		"selection", "backdrop", "file-selector-button", "cue", "grammar-error",
		"spelling-error", "target-text", "highlight", "part", "slotted",
		"-moz-selection", "-moz-placeholder", "-webkit-input-placeholder",
		"-ms-input-placeholder", "-webkit-scrollbar", "-webkit-scrollbar-thumb",
		"-webkit-scrollbar-track", "-webkit-scrollbar-button":
		return true
	}
	return false
}
