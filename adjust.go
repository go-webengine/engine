// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package engine

import (
	"context"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/go-webengine/engine/dom"
	"github.com/go-webengine/engine/paint"
)

// Geometry is a laid-out document's geometry as a script reads it through
// CSSOM View: border boxes (getBoundingClientRect, offsetWidth), client
// sizes (clientWidth) and the used values getComputedStyle reports, all in
// CSS px. Like a script's reads, every read first lays the document out
// again when the DOM changed since the last one, so a read right after a
// mutation sees the mutated document. An Engine.Adjust function gets one.
type Geometry struct{ m *layoutMetrics }

// Rect is n's border box in document coordinates, CSS px; ok is false for
// an element that was not laid out.
func (g *Geometry) Rect(n *dom.Node) (x, y, w, h float64, ok bool) {
	g.m.Refresh()
	return g.m.Rect(n)
}

// OffsetWidth is n's offsetWidth: its border-box width rounded to an
// integer, 0 when it was not laid out.
func (g *Geometry) OffsetWidth(n *dom.Node) int {
	_, _, w, _, _ := g.Rect(n)
	return int(math.Round(w))
}

// ClientWidth is n's clientWidth: the width of its padding box rounded to
// an integer, 0 for an inline-level element or one not laid out.
func (g *Geometry) ClientWidth(n *dom.Node) int {
	g.m.Refresh()
	w, _, ok := g.m.ClientSize(n)
	if !ok {
		return 0
	}
	return int(math.Round(w))
}

// Computed is the value getComputedStyle(n).getPropertyValue(prop)
// returns: a used value ("64px", "block"), "" for a property it does not
// resolve. prop is hyphenated (padding-left).
func (g *Geometry) Computed(n *dom.Node, prop string) string {
	g.m.Refresh()
	v, _ := g.m.Computed(n, prop)
	return v
}

// JSParseFloat is ECMAScript's parseFloat (ECMA-262 §19.2.4): the longest
// prefix of s, after leading white space, that is a decimal literal
// ("53.5px" → 53.5, "1e3x" → 1000, "Infinity" → +Inf), NaN when there is
// none. A native Adjust reads getComputedStyle's "64px" with it, as the
// script it replaces does.
func JSParseFloat(s string) float64 {
	s = strings.TrimLeftFunc(s, isJSWhiteSpace)
	i := 0
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		i++
	}
	if strings.HasPrefix(s[i:], "Infinity") {
		if s[0] == '-' {
			return math.Inf(-1)
		}
		return math.Inf(1)
	}
	digits := func(j int) int {
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j++
		}
		return j
	}
	start := i
	i = digits(i)
	intDigits := i > start
	frac := false
	if i < len(s) && s[i] == '.' {
		j := digits(i + 1)
		frac = j > i+1
		if intDigits || frac {
			i = j
		}
	}
	if !intDigits && !frac {
		return math.NaN()
	}
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		j := i + 1
		if j < len(s) && (s[j] == '+' || s[j] == '-') {
			j++
		}
		if k := digits(j); k > j {
			i = k
		}
	}
	v, err := strconv.ParseFloat(s[:i], 64)
	if err != nil {
		// a literal too large for a float64 parses to ±Inf with ErrRange
		if ne, ok := err.(*strconv.NumError); !ok || ne.Err != strconv.ErrRange {
			return math.NaN()
		}
	}
	return v
}

// isJSWhiteSpace is ECMAScript's StrWhiteSpaceChar: WhiteSpace (tab,
// vertical tab, form feed, ZWNBSP U+FEFF, and Unicode's Space_Separator)
// and LineTerminator (LF, CR, U+2028, U+2029).
func isJSWhiteSpace(r rune) bool {
	switch r {
	case '\t', '\v', '\f', '\n', '\r', 0xFEFF, 0x2028, 0x2029:
		return true
	}
	return unicode.Is(unicode.Zs, r)
}

// JSNumberString is ECMAScript's Number::toString(v) (ECMA-262 §6.1.6.1.20):
// the shortest digits that read back as v, in fixed notation for
// 1e-7 ≤ |v| < 1e21 and exponential ("1.5e+21", "1e-7") otherwise. A
// native Adjust writes a length with it (JSNumberString(v)+"px"), as the
// script it replaces does when it concatenates a number to a string.
func JSNumberString(v float64) string {
	switch {
	case math.IsNaN(v):
		return "NaN"
	case math.IsInf(v, 1):
		return "Infinity"
	case math.IsInf(v, -1):
		return "-Infinity"
	case v == 0:
		return "0" // -0 too
	}
	sign := ""
	if v < 0 {
		sign, v = "-", -v
	}
	// the shortest digits s (k of them) and n with v = s × 10^(n−k)
	m, e, _ := strings.Cut(strconv.FormatFloat(v, 'e', -1, 64), "e")
	s := strings.Replace(m, ".", "", 1)
	k := len(s)
	x, _ := strconv.Atoi(e)
	n := x + 1
	switch {
	case k <= n && n <= 21:
		return sign + s + strings.Repeat("0", n-k)
	case 0 < n && n <= 21:
		return sign + s[:n] + "." + s[n:]
	case -6 < n && n <= 0:
		return sign + "0." + strings.Repeat("0", -n) + s
	}
	exp := "e+" + strconv.Itoa(n-1)
	if n-1 < 0 {
		exp = "e-" + strconv.Itoa(1-n)
	}
	if k == 1 {
		return sign + s + exp
	}
	return sign + s[:1] + "." + s[1:] + exp
}

// adjust runs e.Adjust where a page's scripts run, and settles the render
// as the scripts' settle loop does: when Adjust changed the DOM, the
// document is re-cascaded and re-laid out, then its images reloaded and laid
// out once more. A native function injects no script, so one pass is the
// fixpoint.
func (e *Engine) adjust(ctx context.Context, doc *Document, vpW, vpH int, fonts *paint.Fonts, rp *renderPass, initialLayout time.Duration, onStage func(stage string, rp *renderPass)) {
	s := newSettleState(ctx, doc, initialLayout)
	e.Adjust(ctx, doc.Root, &Geometry{m: e.liveMetrics(doc, rp, fonts, vpW, vpH)})
	s.prevLinks = linkKey(doc.Root)
	e.relayoutPass(ctx, doc, vpW, vpH, fonts, rp, s, onStage)
	e.finishSettle(ctx, doc, vpW, vpH, fonts, rp, s)
}
