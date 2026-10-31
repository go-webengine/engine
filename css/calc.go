// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package css

import (
	"strconv"
	"strings"
)

// resolveCalc replaces every top-level calc(...) expression in value with its
// evaluated plain-length text (e.g. "48px"), the same substitution-level
// treatment resolveLightDark gives light-dark() — run after var()
// substitution, so a var() reference inside calc() has already become a
// literal by the time this runs.
//
// This covers the pure-arithmetic subset (+, -, *, / over lengths and bare
// numbers, with nested parens) that calc() is used for almost everywhere in
// practice — which is EXACTLY what a var()-driven design-token spacing scale
// compiles to. Tailwind v4 (seen live on tailwindcss.com) defines its ENTIRE
// numeric spacing scale this way: `--spacing: .25rem` once at :root, then
// every sized/spaced utility (`size-48`, `h-48`, `p-4`, `gap-4`, `mt-4`, …)
// as `calc(var(--spacing) * 48)`. Without this, every one of those utilities
// failed to parse as a length and its declaration was silently dropped —
// e.g. an `<img class="size-48">` (meant to be a 192×192px avatar) fell back
// to its raw intrinsic pixel size, rendering many times too large.
//
// A calc() whose terms are all percentages (CSS Values 4 §10.1: a sum of
// percentages is a percentage) collapses to one percentage, which layout
// resolves against the containing block as it does a plain one.
//
// A calc() that mixes a percentage (or vw/vh, approximated as a percentage
// elsewhere in this package) with an absolute length cannot be collapsed to
// a single number here — percentages resolve only against a containing
// block at LAYOUT time, which this text-level pass has no access to. That
// case, and any other expression this evaluator does not recognise, makes
// resolveCalc report false, exactly like an unresolvable var(): the
// declaration is dropped, unchanged from this engine's behaviour before
// calc() was understood at all.
func resolveCalc(value string) (string, bool) {
	if findCalcFunc(value, 0) < 0 {
		return value, true
	}
	var b strings.Builder
	i := 0
	for i < len(value) {
		rel := findCalcFunc(value[i:], 0)
		if rel < 0 {
			b.WriteString(value[i:])
			break
		}
		abs := i + rel
		b.WriteString(value[i:abs])
		open := abs + len("calc") // index of '('
		closeIdx, ok := matchParen(value, open)
		if !ok {
			return "", false
		}
		v, kind, ok := evalCalc(value[open+1 : closeIdx])
		switch {
		case !ok || kind == calcNum:
			return "", false
		case kind == calcPct:
			// every term was a percentage (calc(100% - 40%), Marpit's
			// split-background offset): the sum is one percentage, which
			// the property resolves at layout like any other
			b.WriteString(strconv.FormatFloat(v, 'f', -1, 64) + "%")
		default:
			b.WriteString(strconv.FormatFloat(v, 'f', -1, 64) + "px")
		}
		i = closeIdx + 1
	}
	return b.String(), true
}

// findCalcFunc returns the index (relative to from) of the next "calc(" token
// in s at or after from, or -1 if none. Mirrors findVarFunc's guard against
// matching inside a longer identifier.
func findCalcFunc(s string, from int) int {
	for i := from; ; {
		j := indexFold(s[i:], "calc(")
		if j < 0 {
			return -1
		}
		pos := i + j
		if pos == 0 || !isIdentChar(s[pos-1]) {
			return pos
		}
		i = pos + 1
	}
}

// evalCalcExpr evaluates the content of one calc(...) call. hasUnit reports
// whether the result is a length (true) or a dimensionless number (false,
// which is invalid as a final calc() result but valid as an intermediate
// term — a caller wanting a length should require hasUnit).
func evalCalcExpr(s string) (val float64, hasUnit bool, ok bool) {
	val, kind, ok := evalCalc(s)
	if !ok || kind == calcPct {
		return 0, false, false // a percentage is no pixel length (media queries)
	}
	return val, kind == calcLen, true
}

// calcKind is what a calc() term or sum is: a bare number, a length (pixels),
// or a percentage.
type calcKind uint8

const (
	calcNum calcKind = iota
	calcLen
	calcPct
)

// evalCalc evaluates the content of one calc(...) call, with its kind.
func evalCalc(s string) (val float64, kind calcKind, ok bool) {
	p := &calcParser{s: s}
	val, kind, ok = p.expr()
	if !ok {
		return 0, calcNum, false
	}
	p.skipSpace()
	if p.i != len(p.s) {
		return 0, calcNum, false
	}
	return val, kind, true
}

// calcParser is a small recursive-descent parser for the arithmetic grammar
// CSS calc() supports: expr := term (('+'|'-') term)*, term := factor
// (('*'|'/') factor)*, factor := number[unit] | '(' expr ')'.
type calcParser struct {
	s string
	i int
	// depth counts nested '(' ... ')' groups — factor's own recursion into
	// expr for a parenthesized sub-expression (via term) is the only cycle
	// in this grammar, so bounding it there bounds total Go call-stack
	// depth. A value's own source text is this parser's only input, and
	// nothing upstream limits how many parens it can carry (a style="..."
	// attribute on the main document, itself capped only at 32MB — see
	// engine.go's own body-size cap — can trivially carry millions):
	// confirmed live, a 3,000,000-deep calc((((...1...)))) crashes with an
	// unrecoverable "fatal error: stack overflow" (not a panic — recover()
	// cannot catch it) where 1,000,000 does not yet. maxCalcDepth is
	// enormously generous against that measured threshold while being far
	// past anything a real calc() expression ever nests (a handful of
	// levels, in practice).
	depth int

	// basis/hasBasis let a caller with a KNOWN resolution basis (clip-path's
	// own border-box width/height — see evalCalcWithBasis) have a `%` term
	// convert straight to a pixel calcLen instead of the calcPct this
	// parser's zero-value behaviour carries unresolved (see resolveCalc's
	// own doc comment on why: this text-level pass ordinarily has no box to
	// resolve a percentage against). Converting eagerly is what lets a `%`
	// and an absolute length combine in the same +/- sum — expr/term's own
	// "both sides must be the same kind" rule would otherwise reject it,
	// which is exactly the failure a plain evalCalc gives for
	// `calc(100% - 112px)` (confirmed live: vercel.com's own clip-path).
	basis    float64
	hasBasis bool
}

const maxCalcDepth = 256

func (p *calcParser) skipSpace() {
	for p.i < len(p.s) && (p.s[p.i] == ' ' || p.s[p.i] == '\t' || p.s[p.i] == '\n' || p.s[p.i] == '\r') {
		p.i++
	}
}

func (p *calcParser) expr() (float64, calcKind, bool) {
	v, u, ok := p.term()
	if !ok {
		return 0, calcNum, false
	}
	for {
		p.skipSpace()
		if p.i >= len(p.s) || (p.s[p.i] != '+' && p.s[p.i] != '-') {
			break
		}
		op := p.s[p.i]
		p.i++
		p.skipSpace()
		v2, u2, ok := p.term()
		if !ok {
			return 0, calcNum, false
		}
		// + and - require both sides to be the same kind (all lengths, all
		// percentages or all bare numbers) — CSS calc() does not let a length
		// and a dimensionless number be added, and a length and a percentage
		// are only summed at layout, which this stage cannot do.
		if u != u2 {
			return 0, calcNum, false
		}
		if op == '+' {
			v += v2
		} else {
			v -= v2
		}
	}
	return v, u, true
}

func (p *calcParser) term() (float64, calcKind, bool) {
	v, u, ok := p.factor()
	if !ok {
		return 0, calcNum, false
	}
	for {
		p.skipSpace()
		if p.i >= len(p.s) || (p.s[p.i] != '*' && p.s[p.i] != '/') {
			break
		}
		op := p.s[p.i]
		p.i++
		p.skipSpace()
		v2, u2, ok := p.factor()
		if !ok {
			return 0, calcNum, false
		}
		if op == '*' {
			// length * length has no meaning; a plain number multiplies a
			// length or a percentage (or another plain number) in either
			// order.
			if u != calcNum && u2 != calcNum {
				return 0, calcNum, false
			}
			v *= v2
			if u == calcNum {
				u = u2
			}
		} else {
			// Dividing BY a length has no meaning; the divisor must be a
			// plain number.
			if u2 != calcNum || v2 == 0 {
				return 0, calcNum, false
			}
			v /= v2
		}
	}
	return v, u, true
}

func (p *calcParser) factor() (float64, calcKind, bool) {
	p.skipSpace()
	if p.i < len(p.s) && p.s[p.i] == '(' {
		if p.depth >= maxCalcDepth {
			// Past the cap, treat this calc() as unparseable — the same
			// failure this parser already returns for any other malformed
			// input — rather than recursing further into expr.
			return 0, calcNum, false
		}
		p.depth++
		defer func() { p.depth-- }()
		p.i++
		v, u, ok := p.expr()
		if !ok {
			return 0, calcNum, false
		}
		p.skipSpace()
		if p.i >= len(p.s) || p.s[p.i] != ')' {
			return 0, calcNum, false
		}
		p.i++
		return v, u, true
	}
	start := p.i
	if p.i < len(p.s) && (p.s[p.i] == '+' || p.s[p.i] == '-') {
		p.i++
	}
	digitsStart := p.i
	for p.i < len(p.s) && (isCalcDigit(p.s[p.i]) || p.s[p.i] == '.') {
		p.i++
	}
	if p.i == digitsStart {
		return 0, calcNum, false
	}
	numStr := p.s[start:p.i]
	unitStart := p.i
	for p.i < len(p.s) && isIdentChar(p.s[p.i]) {
		p.i++
	}
	if p.i < len(p.s) && p.s[p.i] == '%' {
		// A percentage cannot be resolved to a fixed number at this
		// text-level stage (see the doc comment on resolveCalc), but a sum
		// of percentages is a percentage: it is carried as one.
		if p.i != unitStart {
			return 0, calcNum, false
		}
		p.i++
		n, err := strconv.ParseFloat(numStr, 64)
		if err != nil {
			return 0, calcNum, false
		}
		if p.hasBasis {
			return n / 100 * p.basis, calcLen, true
		}
		return n, calcPct, true
	}
	unit := p.s[unitStart:p.i]
	n, err := strconv.ParseFloat(numStr, 64)
	if err != nil {
		return 0, calcNum, false
	}
	if unit == "" {
		return n, calcNum, true
	}
	// emRef only matters for an "em" term, which real-world calc() usage
	// (a var()-driven spacing scale, a two-length subtraction) essentially
	// never contains; 16 mirrors this package's existing "approximate as the
	// Phase-0 root size" treatment of rem elsewhere in parseLength.
	l, ok := parseLength(numStr+unit, 16)
	if !ok || l.IsPercent {
		return 0, calcNum, false
	}
	return l.Px, calcLen, true
}

func isCalcDigit(c byte) bool { return c >= '0' && c <= '9' }

// evalCalcWithBasis evaluates one calc(...) call's inner expression (the
// text between its outer parens) against a KNOWN resolution basis, letting
// a percentage term combine with an absolute length in the same sum —
// unlike evalCalc/resolveCalc, which must defer any percentage because they
// run at cascade time, before any box exists to resolve one against. Used
// by clip-path's polygon() vertices, resolved at PAINT time against the
// element's own border-box width (for an x coordinate) or height (for a y
// coordinate) — see css.ResolveClipPathPolygon. Not used by anything else:
// every other property's percentage is still deferred to Length.Resolve at
// layout time, unaffected by this.
func evalCalcWithBasis(s string, basisPx float64) (float64, bool) {
	p := &calcParser{s: s, basis: basisPx, hasBasis: true}
	v, kind, ok := p.expr()
	if !ok || kind == calcNum {
		return 0, false
	}
	p.skipSpace()
	if p.i != len(p.s) {
		return 0, false
	}
	return v, true
}
