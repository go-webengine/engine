// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package css

import (
	"strconv"
	"strings"
)

// The ch unit (CSS Values 4 §6.1.1): "the used advance measure of the “0”
// (ZERO, U+0030) glyph in the font used to render it", and, where that is
// impossible or impractical to determine, "0.5em wide by 1em tall".
//
// A length has no font to ask when it is parsed, so the cascade rewrites
// every `<number>ch` in a declaration as `<number × advance>px` before the
// declaration is applied, against the element's own font (family, weight,
// style and the font-size it will have) — see resolveCh. The advance comes
// from Media.Advance, the layout's own text measurer, so a ch is as wide as
// the "0" the page is then drawn with.

// Advance measures the advance width in pixels of text in a font; it is the
// shape of layout.Measurer.Measure, which the engine passes to the cascade so
// that font-relative units can see the font.
type Advance func(text string, fam FontFamily, sizePx float64, weight int, italic bool) float64

// zeroAdvance is the advance of "0" in px for a font of the given size: the
// measured one, else the spec's 0.5em fallback (no measurer, or a font that
// measures to nothing).
func zeroAdvance(adv Advance, st *Style) float64 {
	if adv != nil {
		if w := adv("0", st.FontFamily, st.FontSize, st.FontWeight, st.Italic); w > 0 {
			return w
		}
	}
	return st.FontSize / 2
}

// resolveCh rewrites each `<number>ch` of a declaration value as device
// pixels in st's font, leaving quoted text, url() and identifiers alone
// (like scaleLengths). A value without a ch unit is returned as is, and the
// font is not consulted.
func resolveCh(value string, adv Advance, st *Style) string {
	if !strings.Contains(value, "ch") && !strings.Contains(value, "CH") && !strings.Contains(value, "Ch") && !strings.Contains(value, "cH") {
		return value
	}
	var b strings.Builder
	var zero float64
	haveZero := false
	n := len(value)
	for i := 0; i < n; {
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
			j := i
			for j < n && isIdentChar(value[j]) {
				j++
			}
			b.WriteString(value[i:j])
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
			f, err := strconv.ParseFloat(value[i:numEnd], 64)
			if err == nil && strings.EqualFold(value[numEnd:j], "ch") {
				if !haveZero {
					zero, haveZero = zeroAdvance(adv, st), true
				}
				b.WriteString(strconv.FormatFloat(f*zero, 'f', -1, 64))
				b.WriteString("px")
			} else {
				b.WriteString(value[i:j])
			}
			i = j
			continue
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}
