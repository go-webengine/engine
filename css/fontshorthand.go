// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package css

import "strings"

// expandFont expands the `font` shorthand (CSS Fonts 4 §2.7):
//
//	[ [ <font-style> || <font-variant-css2> || <font-weight> || <font-width-css3> ]?
//	  <font-size> [ / <line-height> ]? <font-family># ]
//
// into the longhands this engine models. The shorthand first resets every
// longhand it covers, so style, weight and line-height are always emitted
// (normal when the value omits them); font-variant and font-width are accepted
// and dropped, the engine having no model for them. The font-size and the
// font-family list are required: a value without both is invalid and expands
// to nothing, as does a system-font keyword (caption, menu, …), a CSS-wide
// keyword and anything holding var(), whose value is only known at cascade time.
func expandFont(val string, important bool) []Declaration {
	lv := strings.ToLower(val)
	if strings.Contains(lv, "var(") {
		return nil
	}
	toks := splitTopLevel(val)
	style, weight := "normal", "normal"
	i := 0
scan:
	for ; i < len(toks); i++ {
		t := strings.ToLower(toks[i])
		switch {
		case t == "normal", t == "small-caps", fontWidthKeyword(t):
		case t == "italic" || t == "oblique":
			style = t
			// oblique takes an optional angle: `oblique 10deg`.
			if i+1 < len(toks) && strings.HasSuffix(strings.ToLower(toks[i+1]), "deg") {
				i++
			}
		case t == "bold" || t == "bolder" || t == "lighter":
			weight = t
		case isFontWeightNumber(t):
			weight = t
		default:
			break scan
		}
	}
	if i >= len(toks) { // no font-size
		return nil
	}
	sz, lh := toks[i], "normal"
	if k := strings.IndexByte(sz, '/'); k >= 0 {
		sz, lh = sz[:k], sz[k+1:]
		if lh == "" { // `32px/ 1.5`
			if i+1 >= len(toks) {
				return nil
			}
			i++
			lh = toks[i]
		}
	} else if i+1 < len(toks) && strings.HasPrefix(toks[i+1], "/") {
		i++
		lh = toks[i][1:]
		if lh == "" { // `32px / 1.5`
			if i+1 >= len(toks) {
				return nil
			}
			i++
			lh = toks[i]
		}
	}
	family := strings.TrimSpace(strings.Join(toks[i+1:], " "))
	if sz == "" || lh == "" || family == "" {
		return nil
	}
	return []Declaration{
		{Property: "font-style", Value: style, Important: important},
		{Property: "font-weight", Value: weight, Important: important},
		{Property: "font-size", Value: sz, Important: important},
		{Property: "line-height", Value: lh, Important: important},
		{Property: "font-family", Value: family, Important: important},
	}
}

// fontWidthKeyword reports a <font-width-css3> keyword (font-stretch values).
func fontWidthKeyword(t string) bool {
	switch t {
	case "ultra-condensed", "extra-condensed", "condensed", "semi-condensed",
		"semi-expanded", "expanded", "extra-expanded", "ultra-expanded":
		return true
	}
	return false
}

// isFontWeightNumber reports a bare number 1..1000: a size always has a unit,
// so before the size a unitless number is the weight.
func isFontWeightNumber(t string) bool {
	n, ok := atoiPositive(t)
	return ok && n >= 1 && n <= 1000
}
