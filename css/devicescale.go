// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package css

import (
	"strconv"
	"strings"
)

// A cascade run at a device pixel ratio other than 1 (Media.Scale) computes
// every length in DEVICE pixels, so that everything downstream — layout,
// text rasterisation at font-size × scale, borders, shadows, gradients,
// vector images — simply works at the higher density with no notion of a
// scale of its own. This is the CSS px → device px mapping of CSS Values 4
// §6.2 applied once, at the source: an absolute length (px, in, cm, mm, Q,
// pt, pc) and a rem (which this engine resolves against a fixed 16px root)
// are multiplied by the scale, while every relative length (em, %, vw, vh)
// follows from those automatically. Media queries are evaluated separately,
// against Media.Width in CSS px, so breakpoints are unaffected.

// borderWidthKeywordPx are the line-width keywords (CSS Backgrounds 3 §3.3)
// at the px values this engine gives them (see borderKeywordWidth); they are
// absolute lengths in disguise and scale like one.
var borderWidthKeywordPx = map[string]string{"thin": "1px", "medium": "3px", "thick": "5px"}

// takesLineWidth reports whether a property's value can carry a line-width
// keyword (thin/medium/thick).
func takesLineWidth(prop string) bool {
	return strings.HasPrefix(prop, "border") || strings.HasPrefix(prop, "outline") || strings.HasPrefix(prop, "column-rule")
}

// scaleLengths rewrites every absolute length (and rem) in a declaration
// value as the equivalent number of device pixels at scale, e.g. "1px solid
// red" → "2px solid red" and "calc(100% - 1in)" → "calc(100% - 192px)" at
// scale 2. Text inside quotes and url() is left alone, as is any number glued
// to an identifier or a '#' (a hex colour). For a border/outline property the
// line-width keywords are converted too. scale 1 returns value unchanged.
func scaleLengths(prop, value string, scale float64) string {
	if scale == 1 {
		return value
	}
	lw := takesLineWidth(prop)
	var b strings.Builder
	i := 0
	n := len(value)
	for i < n {
		c := value[i]
		switch {
		case c == '"' || c == '\'':
			j := i + 1
			for j < n && value[j] != c {
				if value[j] == '\\' {
					j++
				}
				j++
			}
			if j < n {
				j++
			}
			if j > n {
				j = n
			}
			b.WriteString(value[i:j])
			i = j
			continue
		case hasPrefixFold(value[i:], "url(") && (i == 0 || !isIdentChar(value[i-1])):
			end, ok := matchParen(value, i+3)
			if !ok {
				b.WriteString(value[i:])
				return b.String()
			}
			b.WriteString(value[i : end+1])
			i = end + 1
			continue
		case isIdentChar(c) && !isNumberStart(value, i):
			// An identifier (or a hex colour's digits after '#'): copy it
			// whole, converting a lone line-width keyword.
			j := i
			for j < n && isIdentChar(value[j]) {
				j++
			}
			word := value[i:j]
			if px, ok := borderWidthKeywordPx[strings.ToLower(word)]; ok && lw && (i == 0 || value[i-1] != '#') {
				word = scaleLengths(prop, px, scale)
			}
			b.WriteString(word)
			i = j
			continue
		case isNumberStart(value, i):
			j := i
			if value[j] == '+' || value[j] == '-' {
				j++
			}
			for j < n && (value[j] >= '0' && value[j] <= '9' || value[j] == '.') {
				j++
			}
			numEnd := j
			for j < n && (value[j] >= 'a' && value[j] <= 'z' || value[j] >= 'A' && value[j] <= 'Z') {
				j++
			}
			num, unit := value[i:numEnd], strings.ToLower(value[numEnd:j])
			f, err := strconv.ParseFloat(num, 64)
			px, abs := absoluteUnitPx[unit]
			if unit == "rem" {
				px, abs = 16, true
			}
			if err != nil || !abs || unit == "" {
				b.WriteString(value[i:j])
			} else {
				b.WriteString(strconv.FormatFloat(f*px*scale, 'f', -1, 64))
				b.WriteString("px")
			}
			i = j
			continue
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}

// isNumberStart reports whether a numeric token starts at s[i]: a digit, or
// a '.'/sign followed by a digit, not glued to a preceding identifier
// character or '#'.
func isNumberStart(s string, i int) bool {
	if i > 0 && (isIdentChar(s[i-1]) || s[i-1] == '#') {
		return false
	}
	j := i
	if j < len(s) && (s[j] == '+' || s[j] == '-') {
		j++
	}
	if j < len(s) && s[j] == '.' {
		j++
	}
	return j < len(s) && s[j] >= '0' && s[j] <= '9'
}

func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}
