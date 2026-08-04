// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package css

import (
	"strconv"
	"strings"
)

// parseLineHeight parses a line-height value: the keyword normal, a unitless
// multiplier (relative to the font-size emRef), a px/em length, or a percentage.
func parseLineHeight(v string, emRef float64) (LineHeight, bool) {
	lv := strings.ToLower(strings.TrimSpace(v))
	if lv == "normal" {
		return LineHeight{Normal: true}, true
	}
	// A bare number (no unit) is a multiplier of the font-size.
	if f, err := strconv.ParseFloat(lv, 64); err == nil {
		if f < 0 {
			return LineHeight{}, false
		}
		return LineHeight{Px: f * emRef}, true
	}
	if l, ok := parseLength(v, emRef); ok && !l.Auto {
		if l.IsPercent {
			return LineHeight{Px: l.Percent * emRef}, true
		}
		return LineHeight{Px: l.Px}, true
	}
	return LineHeight{}, false
}

func parseJustify(lv string) (Justify, bool) {
	switch lv {
	case "flex-start", "start", "left", "normal":
		return JustifyStart, true
	case "flex-end", "end", "right":
		return JustifyEnd, true
	case "center":
		return JustifyCenter, true
	case "space-between":
		return JustifySpaceBetween, true
	case "space-around":
		return JustifySpaceAround, true
	case "space-evenly":
		return JustifySpaceEvenly, true
	}
	return 0, false
}

func parseAlignItems(lv string) (AlignItems, bool) {
	switch lv {
	case "stretch", "normal":
		return AlignStretch, true
	case "flex-start", "start", "self-start":
		return AlignFlexStart, true
	case "flex-end", "end", "self-end":
		return AlignFlexEnd, true
	case "center":
		return AlignCenterItems, true
	}
	return 0, false
}

// applyFlexShorthand parses the flex shorthand. It supports the common forms:
// a single number (grow), "none" (0 0 auto), "auto" (1 1 auto), "initial"
// (0 1 auto), and "<grow> <shrink>? <basis>?".
func applyFlexShorthand(s *Style, v string, emRef float64) {
	fields := strings.Fields(strings.ToLower(v))
	switch {
	case len(fields) == 1 && fields[0] == "none":
		s.FlexGrow, s.FlexShrink, s.FlexBasis = 0, 0, Length{Auto: true}
		return
	case len(fields) == 1 && fields[0] == "auto":
		s.FlexGrow, s.FlexShrink, s.FlexBasis = 1, 1, Length{Auto: true}
		return
	case len(fields) == 1 && fields[0] == "initial":
		s.FlexGrow, s.FlexShrink, s.FlexBasis = 0, 1, Length{Auto: true}
		return
	}
	// Positional: grow [shrink] [basis]. A token that parses as a length (and is
	// not a bare number) is the basis; bare numbers fill grow then shrink.
	sawGrow, sawShrink := false, false
	// Default basis for the numeric form is 0 (per spec flex: 1 → 1 1 0).
	s.FlexGrow, s.FlexShrink, s.FlexBasis = 0, 1, Length{Px: 0}
	for _, f := range fields {
		if l, ok := parseLength(f, emRef); ok && (l.Auto || l.IsPercent || hasUnit(f)) {
			s.FlexBasis = l
			continue
		}
		if n, err := strconv.ParseFloat(f, 64); err == nil && n >= 0 {
			if !sawGrow {
				s.FlexGrow, sawGrow = n, true
			} else if !sawShrink {
				s.FlexShrink, sawShrink = n, true
			}
		}
	}
}

// hasUnit reports whether a token carries a length unit (so a bare number is
// treated as a flex factor rather than a basis).
func hasUnit(f string) bool {
	f = strings.ToLower(f)
	return strings.HasSuffix(f, "px") || strings.HasSuffix(f, "em") ||
		strings.HasSuffix(f, "%") || f == "auto"
}

// borderKeywordWidth maps the thin/medium/thick keywords to pixel widths.
func borderKeywordWidth(tok string) (float64, bool) {
	switch tok {
	case "thin":
		return 1, true
	case "medium":
		return 3, true
	case "thick":
		return 5, true
	}
	return 0, false
}

// borderStyleKeyword maps a border-style token to our BorderStyle, reporting
// whether the token was a recognised style keyword.
func borderStyleKeyword(tok string) (BorderStyle, bool) {
	switch tok {
	case "none", "hidden":
		return BorderNone, true
	case "solid", "dashed", "dotted", "double", "groove", "ridge", "inset", "outset":
		return BorderSolid, true
	}
	return BorderNone, false
}

// applyBorderSideShorthand parses "<width> <style> <color>" (any order) into one
// side. Missing colour defaults to the element's current colour.
func applyBorderSideShorthand(side *BorderSide, v string, emRef float64, cur Color) {
	var out BorderSide
	out.Color = cur
	sawColor := false
	for _, tok := range strings.Fields(strings.ToLower(v)) {
		if w, ok := borderKeywordWidth(tok); ok {
			out.Width = w
			continue
		}
		if st, ok := borderStyleKeyword(tok); ok {
			out.Style = st
			continue
		}
		if l, ok := parseLength(tok, emRef); ok && !l.Auto && !l.IsPercent {
			out.Width = l.Px
			continue
		}
		if c, ok := parseColor(tok); ok {
			out.Color, sawColor = c, true
		}
	}
	_ = sawColor
	*side = out
}

func applyBorderShorthand(b *Borders, v string, emRef float64, cur Color) {
	var side BorderSide
	side.Color = cur
	applyBorderSideShorthand(&side, v, emRef, cur)
	b.Top, b.Right, b.Bottom, b.Left = side, side, side, side
}

// fourValues returns the top,right,bottom,left expansion of a 1-to-4 token list.
func fourValues[T any](vals []T) (t, r, bo, l T, ok bool) {
	switch len(vals) {
	case 1:
		return vals[0], vals[0], vals[0], vals[0], true
	case 2:
		return vals[0], vals[1], vals[0], vals[1], true
	case 3:
		return vals[0], vals[1], vals[2], vals[1], true
	case 4:
		return vals[0], vals[1], vals[2], vals[3], true
	}
	var z T
	return z, z, z, z, false
}

func applyBorderWidth(b *Borders, v string, emRef float64) {
	fields := strings.Fields(strings.ToLower(v))
	widths := make([]float64, 0, len(fields))
	for _, f := range fields {
		if w, ok := borderKeywordWidth(f); ok {
			widths = append(widths, w)
		} else if l, ok := parseLength(f, emRef); ok && !l.Auto && !l.IsPercent {
			widths = append(widths, l.Px)
		} else {
			return
		}
	}
	if t, r, bo, l, ok := fourValues(widths); ok {
		b.Top.Width, b.Right.Width, b.Bottom.Width, b.Left.Width = t, r, bo, l
	}
}

func applyBorderStyle(b *Borders, v string) {
	fields := strings.Fields(strings.ToLower(v))
	styles := make([]BorderStyle, 0, len(fields))
	for _, f := range fields {
		st, ok := borderStyleKeyword(f)
		if !ok {
			return
		}
		styles = append(styles, st)
	}
	if t, r, bo, l, ok := fourValues(styles); ok {
		b.Top.Style, b.Right.Style, b.Bottom.Style, b.Left.Style = t, r, bo, l
	}
}

func applyBorderColor(b *Borders, v string) {
	fields := strings.Fields(v)
	cols := make([]Color, 0, len(fields))
	for _, f := range fields {
		c, ok := parseColor(f)
		if !ok {
			return
		}
		cols = append(cols, c)
	}
	if t, r, bo, l, ok := fourValues(cols); ok {
		b.Top.Color, b.Right.Color, b.Bottom.Color, b.Left.Color = t, r, bo, l
	}
}

func applyBorderEdgeWidth(side *BorderSide, v string, emRef float64) {
	lv := strings.ToLower(strings.TrimSpace(v))
	if w, ok := borderKeywordWidth(lv); ok {
		side.Width = w
		return
	}
	if l, ok := parseLength(v, emRef); ok && !l.Auto && !l.IsPercent {
		side.Width = l.Px
	}
}

// applyMarginShorthand parses the 1-to-4 margin shorthand, honouring `auto` on
// the left and right sides (used for horizontal centring).
func applyMarginShorthand(s *Style, v string, emRef float64) {
	fields := strings.Fields(strings.ToLower(v))
	type mv struct {
		px   float64
		auto bool
	}
	vals := make([]mv, 0, len(fields))
	for _, f := range fields {
		if f == "auto" {
			vals = append(vals, mv{auto: true})
			continue
		}
		l, ok := parseLength(f, emRef)
		if !ok {
			return
		}
		if l.IsPercent {
			vals = append(vals, mv{px: 0})
		} else {
			vals = append(vals, mv{px: l.Px})
		}
	}
	t, r, bo, l, ok := fourValues(vals)
	if !ok {
		return
	}
	// top/bottom auto collapses to 0 in block flow.
	s.Margin.Top, s.Margin.Bottom = t.px, bo.px
	s.Margin.Left, s.MarginLeftAuto = l.px, l.auto
	s.Margin.Right, s.MarginRightAuto = r.px, r.auto
}

// applyMarginSide sets one horizontal margin, recording an `auto` value.
func applyMarginSide(dst *float64, autoFlag *bool, v string, emRef float64) {
	lv := strings.ToLower(strings.TrimSpace(v))
	if lv == "auto" {
		*autoFlag = true
		*dst = 0
		return
	}
	// parseLength never returns Auto here ("auto" is handled above); a plain
	// pixel/em value clears any previous auto flag.
	if l, ok := parseLength(v, emRef); ok && !l.IsPercent && !l.Auto {
		*autoFlag = false
		*dst = l.Px
	}
}
