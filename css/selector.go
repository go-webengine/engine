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
	// Attrs holds every "[attr]"/"[attr=value]"/… constraint attached to this
	// compound. ALL must match. These used to be dropped entirely ("the
	// constraint is not modelled, the compound reduces to its tag/class/id
	// prefix") — which is the "reduce, don't drop" philosophy applied everywhere
	// else in this file, but is actively WRONG for an attribute selector: on
	// github.com, `.ContentWrapper:where([data-is-hidden-narrow=true])
	// {display:none}` degraded to plain `.ContentWrapper{display:none}` and hid
	// the entire repository content, on every page, regardless of the actual
	// (false) attribute value.
	Attrs []attrMatch
	// Host is set by the ":host" pseudo-class (with or without a functional
	// argument). Unlike every other compound field, it does NOT constrain
	// which element n itself must be — it changes WHAT n is compared against:
	// a Host compound matches only the enclosing shadow tree's host element
	// (supplied out-of-band as the "host" parameter threaded through
	// Selector.MatchesHost / compound.matchesHost), never an ordinary
	// candidate reached by descending the tree. This is the shadow tree's one
	// sanctioned way for its own scoped stylesheet to style the host from the
	// inside (e.g. the `:host(:not([open])) slot {display:none}` idiom real
	// custom elements use to hide slotted content until an interactive
	// state toggles). See matchesHost and cascade.go's shadow-scoped walk.
	Host bool
	// HostHasArg records whether ":host(...)" carried a functional argument at
	// all (true) versus bare ":host" (false). This can't be inferred from
	// HostSelector alone: parseSimpleSelectorList drops any alternative that
	// is unmodelled or always-false statically (":hover", an attribute-only
	// selector it can't reduce, …) — mirroring ":not()" — so ":host(:hover)"
	// ends up with an EMPTY HostSelector, same as bare ":host" would. The two
	// must be treated oppositely: bare ":host" matches host unconditionally;
	// ":host(<list>)" whose every alternative was dropped must NEVER match
	// (mirroring how ":hover" alone never matches) — matching vacuously would
	// be wrong the way ":not()" empty-after-filtering is (correctly) NOT wrong
	// (":not(:hover)" IS always true statically — the negation of an
	// always-false condition).
	HostHasArg bool
	// HostSelector holds the compound alternatives inside ":host(<selector-
	// list>)" (nil for bare ":host", and possibly also nil/empty even with
	// HostHasArg true — see above). The Host compound matches when the host
	// element ALSO matches any one of these — parsed with the same "reduce,
	// don't drop / skip anything unmodelled-or-always-false" logic as ":not()"
	// (see parseSimpleSelectorList), since the argument grammar is the same
	// shape (a list of simple/compound selectors, here to be MATCHED rather
	// than negated).
	HostSelector []compound
}

// attrOp is the comparison an attribute selector performs.
type attrOp byte

const (
	attrPresence  attrOp = iota // [attr]
	attrEqual                   // [attr=value]
	attrPrefix                  // [attr^=value]
	attrSuffix                  // [attr$=value]
	attrSubstring               // [attr*=value]
	attrWordMatch               // [attr~=value] — value is one of a space-separated list
	attrDashMatch               // [attr|=value] — value, or value followed by "-"
)

// attrMatch is one parsed "[...]" attribute selector.
type attrMatch struct {
	Name  string
	Op    attrOp
	Value string
}

// matches reports whether element n's attribute satisfies this constraint.
// Attribute NAME matching is case-insensitive (HTML attribute names always
// are); VALUE matching is case-sensitive — this engine does not model the
// `i`/`s` case-sensitivity flag some selectors carry, only strips its syntax
// so parsing does not choke on it (see parseAttrSelector).
func (a attrMatch) matches(n *dom.Node) bool {
	v, ok := n.Attribute(a.Name)
	if !ok {
		return false
	}
	switch a.Op {
	case attrPresence:
		return true
	case attrEqual:
		return v == a.Value
	case attrPrefix:
		return a.Value != "" && strings.HasPrefix(v, a.Value)
	case attrSuffix:
		return a.Value != "" && strings.HasSuffix(v, a.Value)
	case attrSubstring:
		return a.Value != "" && strings.Contains(v, a.Value)
	case attrWordMatch:
		for _, w := range strings.Fields(v) {
			if w == a.Value {
				return true
			}
		}
		return false
	case attrDashMatch:
		return v == a.Value || strings.HasPrefix(v, a.Value+"-")
	}
	return false
}

// matches reports whether the compound matches an element node. It never
// matches a Host compound — ":host" has no meaning without the enclosing
// shadow tree's host element to compare against, which only matchesHost (via
// an explicit host parameter) can supply. Every call site that might see a
// Host compound without host context (a ":not()" argument, a plain
// document-wide selector, querySelector) is therefore safe by construction.
func (c compound) matches(n *dom.Node) bool {
	if n.Type != dom.Element {
		return false
	}
	if c.Host {
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
	// Both lists are small (a handful of classes on each side even on a
	// utility-class-heavy page), so a linear scan beats allocating and
	// hashing into a map on every single match attempt — this runs once per
	// candidate rule per element, the single hottest path in the cascade.
	if len(c.Classes) > 0 {
		have := n.Classes()
		for _, want := range c.Classes {
			found := false
			for _, cl := range have {
				if cl == want {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
	}
	// ":checked" — at static render an input is checked iff it has the default
	// "checked" attribute (there is no user interaction to toggle it).
	if c.Checked && !isChecked(n) {
		return false
	}
	for _, am := range c.Attrs {
		if !am.matches(n) {
			return false
		}
	}
	// ":not(...)" — the compound fails as soon as any negated selector matches.
	for i := range c.Not {
		if c.Not[i].matches(n) {
			return false
		}
	}
	return true
}

// matchesHost reports whether the compound matches n, evaluated with host as
// the enclosing shadow tree's host element (nil outside any shadow tree). A
// Host compound (":host"/":host(...)") matches only when n IS host itself
// (never an ordinary descendant of it) and, for ":host(<selector-list>)",
// only when host additionally matches one of HostSelector's alternatives.
// Every other compound delegates to the ordinary matches(n), unaffected by
// host — Selector.Matches(n) is exactly MatchesHost(n, nil), so a host of nil
// makes a Host compound fail (via matches' own guard) and every other
// compound behave exactly as before this existed.
func (c compound) matchesHost(n, host *dom.Node) bool {
	if !c.Host {
		return c.matches(n)
	}
	if host == nil || n != host {
		return false
	}
	if !c.HostHasArg {
		return true
	}
	for i := range c.HostSelector {
		if c.HostSelector[i].matches(host) {
			return true
		}
	}
	return false
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
	if c.Host {
		classCount++ // ":host" is itself a pseudo-class (class-level weight)
	}
	classCount += len(c.Attrs) // each attribute selector is class-level weight
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
	// ":host(<selector>)" additionally contributes its most specific
	// argument's specificity, same shape as ":not()" above.
	var ha, hb, hc int
	for i := range c.HostSelector {
		id, cl, tg := c.HostSelector[i].specificity()
		if id*10000+cl*100+tg > ha*10000+hb*100+hc {
			ha, hb, hc = id, cl, tg
		}
	}
	idCount += ha
	classCount += hb
	tagCount += hc
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
// combinator chain from the key selector leftwards. It is exactly
// MatchesHost(n, nil) — outside any shadow tree there is no host to bind
// ":host" to, so a Host compound anywhere in the selector always fails.
func (s Selector) Matches(n *dom.Node) bool {
	return s.MatchesHost(n, nil)
}

// MatchesHost is Matches with host bound as the enclosing shadow tree's host
// element (see cascade.go's shadow-scoped cascade walk), so ":host" and
// ":host(<selector>)" compounds — the shadow tree's one sanctioned way to
// style, or reach past, the host from inside its own scoped stylesheet — can
// match. It is the ONLY way a Host compound ever matches; every other
// compound is unaffected by host.
func (s Selector) MatchesHost(n, host *dom.Node) bool {
	if len(s.parts) == 0 {
		return false
	}
	key := s.parts[len(s.parts)-1]
	if !key.matchesHost(n, host) {
		return false
	}
	return s.matchLeft(len(s.parts)-2, n, host)
}

// matchLeft verifies that part index i (and everything to its left) is
// satisfied relative to the already-matched node matched (which corresponds
// to part i+1), with host bound as in MatchesHost. i < 0 means the whole
// chain is satisfied. Ordinary ancestor/sibling combinators are unaffected by
// host (elementParent/prevElementSibling walk the real DOM exactly as
// before); the one addition is that when a combinator's real-DOM walk is
// exhausted (p == nil: at the top of a shadow tree, since attaching one nils
// out its top-level content's Parent — see dom.attachDeclarativeShadowRoots)
// AND the part being tested there is a Host compound, it is tested against
// host directly instead of failing — the CSS Scoping spec's one sanctioned
// escape from a shadow tree's otherwise fully encapsulated combinator
// matching (a plain, non-":host" compound still correctly finds no further
// ancestor and fails, never leaking into the host's own light-DOM ancestry).
func (s Selector) matchLeft(i int, matched, host *dom.Node) bool {
	if i < 0 {
		return true
	}
	comb := s.combs[i] // combinator between parts[i] and parts[i+1]
	part := s.parts[i]
	switch comb {
	case combChild:
		if p := elementParent(matched); p != nil {
			return part.matchesHost(p, host) && s.matchLeft(i-1, p, host)
		}
		return part.Host && part.matchesHost(host, host) && s.matchLeft(i-1, host, host)
	case combDescendant:
		for p := elementParent(matched); p != nil; p = elementParent(p) {
			if part.matchesHost(p, host) && s.matchLeft(i-1, p, host) {
				return true
			}
		}
		return part.Host && part.matchesHost(host, host) && s.matchLeft(i-1, host, host)
	case combAdjacent:
		prev := prevElementSibling(matched)
		return prev != nil && part.matchesHost(prev, host) && s.matchLeft(i-1, prev, host)
	case combSibling:
		for prev := prevElementSibling(matched); prev != nil; prev = prevElementSibling(prev) {
			if part.matchesHost(prev, host) && s.matchLeft(i-1, prev, host) {
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
		// prefix+alt+suffix textual concatenation is correct when alt is a bare
		// compound (".dark" splices onto ".foo:where(...)" as ".foo.dark", the
		// same element needing both classes) or when the wrapper stands alone
		// (prefix=="", so alt's own combinator chain already means what it says).
		// It is WRONG when the wrapper is compound-attached (prefix!="") AND alt
		// itself ends in a combinator: "X <comb> *" — Tailwind's class-based
		// variant strategy emits exactly this (`:where(.dark, .dark *)` attached
		// to `.dark\:inline`) to mean "the tested element is .dark itself, OR the
		// tested element has an ancestor .dark" (the trailing universal `*` is a
		// no-op stand-in for "the tested element" — it is not "some other
		// element"). Naive concatenation instead produces
		// ".dark\:inline.dark *" — an entirely different selector: "any element
		// with an ancestor matching BOTH classes at once" — silently matching a
		// disjoint set of nodes (or usually nothing at all). See
		// stripTrailingSelfCombinator.
		if prefix != "" {
			if head, comb, ok := stripTrailingSelfCombinator(alt); ok {
				out = append(out, expandFunctionalPseudos(head+string(comb)+prefix+suffix)...)
				continue
			}
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

// stripTrailingSelfCombinator recognises an alternative of the form
// "X <comb> *" — a trailing universal compound standing in for "the tested
// element itself", used to express "has an ancestor/sibling matching X"
// relative to that element (see the comment at its call site for why this
// needs special handling when compound-attached). It reports X (rejoined from
// tokens, so already normalised) and the combinator that connected it to the
// trailing "*", or ok=false when alt isn't shaped this way — a bare compound
// alternative like ".dark" falls through unchanged.
func stripTrailingSelfCombinator(alt string) (head string, comb byte, ok bool) {
	toks := tokenizeSelector(alt)
	if len(toks) < 2 {
		return "", 0, false
	}
	last := toks[len(toks)-1]
	if last.comb || strings.TrimSpace(last.text) != "*" {
		return "", 0, false
	}
	sep := toks[len(toks)-2]
	if !sep.comb {
		return "", 0, false
	}
	var b strings.Builder
	for _, t := range toks[:len(toks)-2] {
		if t.comb {
			b.WriteByte(combinatorChar(t.combKind))
		} else {
			b.WriteString(t.text)
		}
	}
	if b.Len() == 0 {
		return "", 0, false
	}
	return b.String(), combinatorChar(sep.combKind), true
}

// combinatorChar returns the literal selector character for k (a plain space
// for the implicit descendant combinator, which tokenizeSelector parses the
// same as any other whitespace run).
func combinatorChar(k combinator) byte {
	switch k {
	case combChild:
		return '>'
	case combAdjacent:
		return '+'
	case combSibling:
		return '~'
	default:
		return ' '
	}
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
			// An unmodelled ":not()" argument imposes NO constraint rather than
			// dropping the rule — the same "reduce, don't drop" philosophy applied
			// to unknown pseudos. Attribute selectors inside ":not()" ARE modelled
			// (see below), so `:not([data-theme])` is now a real negation, not a
			// no-op.
			c.Not = append(c.Not, parseSimpleSelectorList(arg)...)
		case "host":
			// ":host" / ":host(<selector-list>)" — see compound.Host's doc
			// comment. ":host-context(...)" is a real, related pseudo (matches
			// when host OR any of its real light-DOM ancestors matches the
			// argument) but is intentionally NOT modelled: it falls through to
			// the generic unmodelled-pseudo case below rather than being
			// confused with plain ":host" here. Per the "reduce, don't drop"
			// rule applied to every unknown pseudo, a compound that ALSO
			// carries a tag/class/id (e.g. "div:host-context(.dark)") keeps
			// that constraint and silently ignores just the :host-context part;
			// a compound that is ONLY ":host-context(...)" has nothing left to
			// reduce to, so parseSimple's "reduces to nothing" check below
			// fails it, which drops the WHOLE containing selector (see
			// parseComplex) — never "matches everything". Both confirmed
			// real-world uses of this idiom (developer.mozilla.org,
			// github.com) only need ":host"/":host()", so this is a documented
			// gap (FIDELITY.md), not a silent wrong answer either way.
			c.Host = true
			if arg != "" {
				c.HostHasArg = true
				c.HostSelector = parseSimpleSelectorList(arg)
			}
		default:
			switch {
			case isDynamicPseudo(name):
				c.Dynamic = true
			case isPseudoElement(name):
				// A pseudo-element styles a generated box, not the real element, so
				// the compound must match nothing (see compound.PseudoElement).
				c.PseudoElement = true
			}
			// Any other pseudo (e.g. :nth-child, :host-context) is unmodelled and
			// intentionally ignored — the compound degrades to matching its base.
		}
	}
	s = base
	s, c.Attrs = extractAttrSelectors(s)
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
	// A compound with no tag/class/id and no modelled pseudo/attribute reduces to
	// nothing — a bare "::before" is dropped. A bare ":hover"/":checked"/
	// ":not()"/"[attr]"/":host" is kept: a dynamic-only compound never matches
	// statically (equivalent to being dropped), and ":checked"/":not(...)"/
	// attribute/":host" selectors carry a real constraint on their own.
	if c.Tag == "" && c.ID == "" && len(c.Classes) == 0 &&
		!c.Root && !c.Dynamic && !c.Checked && !c.Host && len(c.Not) == 0 && len(c.Attrs) == 0 {
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

// parseSimpleSelectorList parses a comma-separated list of simple selectors
// shared by ":not(<selector-list>)" and ":host(<selector-list>)" — the two
// functional pseudo-classes in this engine whose argument is that same
// grammar (CSS Level 4 allows a list for both; a single simple selector, e.g.
// ":checked", ".foo" or ":not([open])", is the common real-world case for
// each). Only the compound alternatives are returned; how the caller USES the
// list differs (":not()" negates — the compound fails if ANY matches;
// ":host()" matches — the compound succeeds if ANY matches), but parsing is
// identical. It never fails the surrounding rule — an alternative that is
// empty, dynamic (":hover", never matches statically), or unmodelled (an
// attribute-only "[attr]" or a pseudo-element the parser can't reduce to a
// real constraint) simply contributes no constraint. Only genuine, modelled
// constraints (tag/class/id/:checked/nested :not/:host) are returned, so
// `:not(:checked)` is precise while `:root:not([data-theme])` degrades to
// `:root` rather than dropping the rule.
func parseSimpleSelectorList(arg string) []compound {
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

// extractAttrSelectors removes every top-level "[...]" attribute selector from
// s, parsing each into an attrMatch, and returns the residual string (the
// compound's tag/class/id portion) with those spans removed. A quote inside
// the brackets (single or double) is tracked so a literal ']' in a quoted
// value ("[data-foo=\"a]b\"]") does not end the selector early.
func extractAttrSelectors(s string) (rest string, attrs []attrMatch) {
	var out strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == '\\' && i+1 < len(s) {
			out.WriteByte(s[i])
			out.WriteByte(s[i+1])
			i += 2
			continue
		}
		if s[i] != '[' {
			out.WriteByte(s[i])
			i++
			continue
		}
		j := i + 1
		var quote byte
		for j < len(s) {
			switch {
			case s[j] == '\\' && j+1 < len(s):
				j += 2
				continue
			case quote != 0:
				if s[j] == quote {
					quote = 0
				}
			case s[j] == '\'' || s[j] == '"':
				quote = s[j]
			case s[j] == ']':
				// Falls through to the break below.
			}
			if quote == 0 && s[j] == ']' {
				break
			}
			j++
		}
		if j >= len(s) {
			// Unterminated "[": not valid CSS: keep it literally rather than
			// silently eating the rest of the compound.
			out.WriteByte(s[i])
			i++
			continue
		}
		if am, ok := parseAttrSelector(s[i+1 : j]); ok {
			attrs = append(attrs, am)
		}
		i = j + 1
	}
	return out.String(), attrs
}

// parseAttrSelector parses the text between "[" and "]" of one attribute
// selector: a bare name ("[hidden]", a presence check), or name+operator+value
// ("[data-state=open]", "[href^=https]", …). The value may be quoted (single
// or double) or bare; a trailing case-sensitivity flag ("... i" / "... s") is
// stripped so it does not become part of the value — this engine always
// compares case-sensitively, so the flag's effect itself is not modelled.
func parseAttrSelector(body string) (attrMatch, bool) {
	body = strings.TrimSpace(body)
	if n := len(body); n > 2 && body[n-2] == ' ' {
		switch body[n-1] {
		case 'i', 'I', 's', 'S':
			body = strings.TrimSpace(body[:n-2])
		}
	}
	ops := []struct {
		tok string
		op  attrOp
	}{
		{"^=", attrPrefix}, {"$=", attrSuffix}, {"*=", attrSubstring},
		{"~=", attrWordMatch}, {"|=", attrDashMatch}, {"=", attrEqual},
	}
	for _, o := range ops {
		idx := strings.Index(body, o.tok)
		if idx < 0 {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(body[:idx]))
		if name == "" {
			return attrMatch{}, false
		}
		val := unquoteAttrValue(strings.TrimSpace(body[idx+len(o.tok):]))
		return attrMatch{Name: name, Op: o.op, Value: val}, true
	}
	name := strings.ToLower(strings.TrimSpace(body))
	if name == "" {
		return attrMatch{}, false
	}
	return attrMatch{Name: name, Op: attrPresence}, true
}

// unquoteAttrValue strips one layer of matching single or double quotes.
func unquoteAttrValue(v string) string {
	if len(v) >= 2 {
		if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
			return v[1 : len(v)-1]
		}
	}
	return v
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
