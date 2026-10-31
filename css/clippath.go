// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package css

import "strings"

// ClipPathPoint is one vertex of a clip-path: polygon(), in pixels relative
// to the top-left corner of whatever reference box it was resolved
// against.
type ClipPathPoint struct{ X, Y float64 }

// ResolveClipPathPolygon resolves Style.ClipPath's raw text — a
// `polygon(...)` call, optionally prefixed by its fill-rule, as parse.go's
// `case "clip-path"` already validated it to be before storing it — into
// concrete pixel vertices, given the reference box's width and height. The
// only reference box this engine models is the spec's default, border-box
// (CSS Masking 1 §8: "If no reference box is specified, the border-box will
// be used"); a <geometry-box> keyword is not recognised (see
// Style.ClipPath's own doc comment).
//
// This is deferred to PAINT time — boxW/boxH are not known any earlier —
// the same "percentage deferred until a box exists" treatment width/
// height's own Length.IsPercent already gets at layout time. A calc() term
// (CSS Shapes 1 §2.2: each coordinate is a <length-percentage>) is resolved
// by evalCalcWithBasis against boxW for the x half of a pair and boxH for
// the y half — the box dimension it is itself being evaluated relative to.
func ResolveClipPathPolygon(raw string, boxW, boxH float64) (points []ClipPathPoint, evenOdd bool, ok bool) {
	s := strings.TrimSpace(raw)
	low := strings.ToLower(s)
	switch {
	case strings.HasPrefix(low, "evenodd"):
		evenOdd = true
		s = strings.TrimSpace(s[len("evenodd"):])
	case strings.HasPrefix(low, "nonzero"):
		s = strings.TrimSpace(s[len("nonzero"):])
	}
	low = strings.ToLower(s)
	if !strings.HasPrefix(low, "polygon(") || !strings.HasSuffix(s, ")") {
		return nil, false, false
	}
	inner := s[len("polygon(") : len(s)-1]
	for _, pair := range splitTopLevelCommas(inner) {
		toks := splitTopLevelFields(strings.TrimSpace(pair))
		if len(toks) != 2 {
			return nil, false, false
		}
		x, ok1 := clipPathCoord(toks[0], boxW)
		y, ok2 := clipPathCoord(toks[1], boxH)
		if !ok1 || !ok2 {
			return nil, false, false
		}
		points = append(points, ClipPathPoint{X: x, Y: y})
	}
	if len(points) < 3 {
		// A polygon with fewer than 3 vertices encloses no area — CSS
		// Shapes 1 requires at least one coordinate pair syntactically, but
		// a real clip needs a closed shape.
		return nil, false, false
	}
	return points, evenOdd, true
}

// splitTopLevelFields splits s on whitespace runs OUTSIDE any parenthesised
// group — unlike strings.Fields, which would wrongly split a single
// `calc(100% - 112.00px)` coordinate into four pieces at the spaces its own
// `-` operator needs (confirmed live: this is exactly how vercel.com's own
// clip-path coordinate pair, `100% calc(100% - 112.00px)`, is written).
func splitTopLevelFields(s string) []string {
	var out []string
	depth, start := 0, -1
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '(':
			depth++
			if start < 0 {
				start = i
			}
		case c == ')':
			if depth > 0 {
				depth--
			}
		case depth == 0 && (c == ' ' || c == '\t' || c == '\n' || c == '\r'):
			if start >= 0 {
				out = append(out, s[start:i])
				start = -1
			}
		default:
			if start < 0 {
				start = i
			}
		}
	}
	if start >= 0 {
		out = append(out, s[start:])
	}
	return out
}

// clipPathCoord resolves one polygon() coordinate — a <length-percentage>
// or a calc() of one — against basisPx (the reference box's own width for
// an x coordinate, height for a y coordinate — CSS Shapes 1 §2.2).
func clipPathCoord(tok string, basisPx float64) (float64, bool) {
	if hasPrefixFold(tok, "calc(") && strings.HasSuffix(tok, ")") {
		return evalCalcWithBasis(tok[len("calc("):len(tok)-1], basisPx)
	}
	l, ok := parseLength(tok, 16)
	if !ok || l.Auto {
		return 0, false
	}
	if l.IsPercent {
		return l.Percent * basisPx, true
	}
	return l.Px, true
}
