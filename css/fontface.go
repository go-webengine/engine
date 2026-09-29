// Copyright (c) the go-webengine authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package css

import "strings"

// FontFace is one `@font-face` rule: a named family, the files it can be
// loaded from in preference order, and which weight and slant of that family
// the files carry.
//
// A page that wants a typeface the engine does not bundle says so here and
// nowhere else, so this is the only route by which a document's own fonts can
// reach it. Before this existed, `@font-face` was skipped as an unrecognised
// at-rule and every run was set in one of the three bundled families — so a
// poster asking for IBM Plex Sans and Spectral was typeset in Inter and Lora,
// silently and at different metrics.
type FontFace struct {
	// Family is the family name, lowercased: `font-family` inside the rule,
	// which is what a `font-family` declaration elsewhere has to name to
	// reach this face.
	Family string
	// Srcs are the `src` entries in declaration order — a consumer takes the
	// first whose Format it can decode. `local()` entries are dropped: there
	// is no system font database here to look a face up in.
	Srcs []FontSrc
	// Weight is the weight the files carry, 400 when the rule gives none. A
	// range ("font-weight: 100 900", a variable font) collapses to its first
	// value, since only a static instance can be loaded.
	Weight int
	// Italic is set for `font-style: italic` or `oblique`.
	Italic bool
	// Ranges are the `unicode-range` descriptor's ranges: the characters this
	// face is meant to be used for. Nil means the descriptor was absent or
	// unreadable, which is the initial value `U+0-10FFFF` — every character.
	//
	// This is not a refinement: a font service splits ONE family and weight
	// across several faces by range (Google Fonts serves six for a Latin
	// family — cyrillic, cyrillic-ext, greek, vietnamese, latin-ext, latin),
	// so a consumer that ignores it and keeps one face per family and weight
	// keeps whichever came first and typesets the page in a subset that has
	// none of its characters.
	Ranges []UnicodeRange
}

// UnicodeRange is one range of an `@font-face` rule's `unicode-range`,
// inclusive at both ends.
type UnicodeRange struct{ Lo, Hi rune }

// Covers reports whether r is one of the characters this face is for. A face
// that declared no range covers every character, per the descriptor's initial
// value.
func (f FontFace) Covers(r rune) bool {
	if len(f.Ranges) == 0 {
		return true
	}
	for _, u := range f.Ranges {
		if r >= u.Lo && r <= u.Hi {
			return true
		}
	}
	return false
}

// CoversAny reports whether this face is for any of the characters given —
// the test that decides whether a document needs the face at all, and so
// whether its file is worth fetching.
func (f FontFace) CoversAny(runes map[rune]struct{}) bool {
	if len(f.Ranges) == 0 {
		return true
	}
	for r := range runes {
		if f.Covers(r) {
			return true
		}
	}
	return false
}

// ParseUnicodeRange reads a `unicode-range` value: a comma-separated list of
// single code points (`U+26`), ranges (`U+0-7F`) and wildcard forms (`U+4??`,
// which is U+400 to U+4FF). An entry that does not parse is skipped rather
// than failing the list, since a face with a partly readable range is still
// more use than none.
func ParseUnicodeRange(v string) []UnicodeRange {
	var out []UnicodeRange
	for _, entry := range splitTopLevelCommas(v) {
		entry = strings.TrimSpace(entry)
		if len(entry) < 3 || (entry[0] != 'u' && entry[0] != 'U') || entry[1] != '+' {
			continue
		}
		body := entry[2:]
		switch {
		case strings.ContainsAny(body, "?"):
			lo, hi, ok := wildcardRange(body)
			if ok {
				out = append(out, UnicodeRange{lo, hi})
			}
		case strings.Contains(body, "-"):
			parts := strings.SplitN(body, "-", 2)
			lo, ok1 := parseHexRune(parts[0])
			hi, ok2 := parseHexRune(parts[1])
			if ok1 && ok2 && hi >= lo {
				out = append(out, UnicodeRange{lo, hi})
			}
		default:
			if r, ok := parseHexRune(body); ok {
				out = append(out, UnicodeRange{r, r})
			}
		}
	}
	return out
}

// wildcardRange expands a form like "4??" — every '?' stands for any hex
// digit, so the range runs from all-zeros to all-Fs in those positions. The
// '?' must be a suffix: "4?0" is not a range CSS defines.
func wildcardRange(body string) (rune, rune, bool) {
	i := strings.IndexByte(body, '?')
	if i == 0 {
		// All wildcards: the whole plane set the digit count allows.
		if strings.Count(body, "?") != len(body) {
			return 0, 0, false
		}
	}
	for _, c := range body[i:] {
		if c != '?' {
			return 0, 0, false // a digit after a '?' is not a wildcard form
		}
	}
	lo, ok := parseHexRune(strings.ReplaceAll(body, "?", "0"))
	if !ok {
		return 0, 0, false
	}
	hi, ok := parseHexRune(strings.ReplaceAll(body, "?", "f"))
	if !ok {
		return 0, 0, false
	}
	return lo, hi, true
}

// parseHexRune reads up to six hex digits as a code point.
func parseHexRune(s string) (rune, bool) {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 6 {
		return 0, false
	}
	var v rune
	for _, c := range s {
		var d rune
		switch {
		case c >= '0' && c <= '9':
			d = c - '0'
		case c >= 'a' && c <= 'f':
			d = c - 'a' + 10
		case c >= 'A' && c <= 'F':
			d = c - 'A' + 10
		default:
			return 0, false
		}
		v = v<<4 | d
	}
	if v > 0x10ffff {
		return 0, false
	}
	return v, true
}

// FontSrc is one entry of an `@font-face` rule's `src`.
type FontSrc struct {
	// URL is the url() as written — relative to the stylesheet it came from,
	// which is the caller's to resolve.
	URL string
	// Format is the format() hint, lowercased and unquoted ("truetype",
	// "woff2", "opentype", …), or empty when the rule gave none. It is a
	// HINT: a consumer that cannot decode a format should skip the entry,
	// but an entry without one may still be anything.
	Format string
}

// ParseFontFaces returns every `@font-face` rule in a stylesheet source, in
// document order, including those nested in an `@media` block whose condition
// matches m. A rule with no family or no usable src is dropped.
//
// It is a separate pass from ParseStylesheetMedia rather than another Rule
// variant: an @font-face rule has no selector and takes no part in the
// cascade, so nothing that walks rules should have to learn to skip it.
func ParseFontFaces(src string, m Media) []FontFace {
	return parseFontFaces(stripComments(src), m)
}

func parseFontFaces(src string, m Media) []FontFace {
	var faces []FontFace
	i := 0
	for i < len(src) {
		brace := strings.IndexByte(src[i:], '{')
		if brace < 0 {
			break
		}
		prelude := strings.TrimSpace(src[i : i+brace])
		blockStart := i + brace
		blockEnd, ok := matchBrace(src, blockStart)
		if !ok {
			break
		}
		body := src[blockStart+1 : blockEnd]
		i = blockEnd + 1
		// Same reasoning as parseRules: a bare at-rule statement ending in
		// ';' rides along in this prelude, and only the text after the last
		// one describes the construct that owns this brace.
		if semi := strings.LastIndexByte(prelude, ';'); semi >= 0 {
			prelude = strings.TrimSpace(prelude[semi+1:])
		}
		lower := strings.ToLower(prelude)
		switch {
		case strings.HasPrefix(lower, "@font-face"):
			if f, ok := parseFontFaceBody(body); ok {
				faces = append(faces, f)
			}
		case strings.HasPrefix(lower, "@media"):
			if mediaMatchesOn(lower[len("@media"):], m) {
				faces = append(faces, parseFontFaces(body, m)...)
			}
		case strings.HasPrefix(lower, "@supports"), strings.HasPrefix(lower, "@layer"):
			// Both wrap ordinary rule lists, @font-face included; @supports
			// with a condition this engine does not recognise drops its block
			// wholesale, as it does for rules.
			if !strings.HasPrefix(lower, "@supports") || supportsConditionHolds(lower[len("@supports"):]) {
				faces = append(faces, parseFontFaces(body, m)...)
			}
		}
	}
	return faces
}

// parseFontFaceBody reads one rule's declarations. ok is false for a rule
// that names no family or offers no src this engine could ever fetch.
func parseFontFaceBody(body string) (FontFace, bool) {
	f := FontFace{Weight: 400}
	for _, d := range ParseDeclarations(body) {
		switch strings.ToLower(strings.TrimSpace(d.Property)) {
		case "font-family":
			f.Family = normaliseFamilyName(d.Value)
		case "src":
			f.Srcs = parseFontSrc(d.Value)
		case "font-weight":
			// "100 900" is a variable font's range; only its first value can
			// name a static instance to load.
			if w, ok := parseFontWeightKeyword(strings.Fields(strings.ToLower(d.Value))); ok {
				f.Weight = w
			}
		case "font-style":
			lv := strings.ToLower(strings.TrimSpace(d.Value))
			f.Italic = strings.HasPrefix(lv, "italic") || strings.HasPrefix(lv, "oblique")
		case "unicode-range":
			f.Ranges = ParseUnicodeRange(d.Value)
		}
	}
	if f.Family == "" || len(f.Srcs) == 0 {
		return FontFace{}, false
	}
	return f, true
}

// parseFontWeightKeyword reads an @font-face `font-weight`: a number, one of
// the two keywords, or the first value of a range.
func parseFontWeightKeyword(fields []string) (int, bool) {
	if len(fields) == 0 {
		return 0, false
	}
	switch fields[0] {
	case "normal":
		return 400, true
	case "bold":
		return 700, true
	}
	if n, ok := atoiPositive(fields[0]); ok {
		return n, true
	}
	return 0, false
}

// atoiPositive parses a positive decimal integer, without strconv's error
// allocation for the common reject.
func atoiPositive(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
		if n > 1000 {
			return 0, false
		}
	}
	return n, true
}

// parseFontSrc reads an `src` value into its entries, in order, keeping only
// url() ones: a local() entry names a face in a system font database, and
// this engine has none to consult.
func parseFontSrc(v string) []FontSrc {
	var out []FontSrc
	for _, entry := range splitTopLevelCommas(v) {
		entry = strings.TrimSpace(entry)
		u, ok := urlToken(entry)
		if !ok {
			continue
		}
		out = append(out, FontSrc{URL: u, Format: formatHint(entry)})
	}
	return out
}

// urlToken extracts the url(...) target of one src entry, unquoted.
func urlToken(entry string) (string, bool) {
	i := strings.Index(strings.ToLower(entry), "url(")
	if i < 0 {
		return "", false
	}
	rest := entry[i+len("url("):]
	j := strings.IndexByte(rest, ')')
	if j < 0 {
		return "", false
	}
	u := strings.TrimSpace(rest[:j])
	u = strings.Trim(u, `"'`)
	if u = strings.TrimSpace(u); u == "" {
		return "", false
	}
	return u, true
}

// formatHint extracts the format(...) hint of one src entry, lowercased and
// unquoted; empty when the entry gives none. A "woff2-variations" style hint
// keeps only its base token, which is what decides decodability.
func formatHint(entry string) string {
	i := strings.Index(strings.ToLower(entry), "format(")
	if i < 0 {
		return ""
	}
	rest := entry[i+len("format("):]
	j := strings.IndexByte(rest, ')')
	if j < 0 {
		return ""
	}
	h := strings.ToLower(strings.TrimSpace(rest[:j]))
	h = strings.Trim(h, `"'`)
	if k := strings.IndexByte(h, '-'); k > 0 {
		h = h[:k]
	}
	return strings.TrimSpace(h)
}

// splitTopLevelCommas splits on commas that are not inside parentheses or a
// quoted string — src entries carry both.
func splitTopLevelCommas(v string) []string {
	var out []string
	depth, quote, start := 0, byte(0), 0
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '(':
			depth++
		case c == ')':
			if depth > 0 {
				depth--
			}
		case c == ',' && depth == 0:
			out = append(out, v[start:i])
			start = i + 1
		}
	}
	return append(out, v[start:])
}

// normaliseFamilyName trims whitespace and quotes and lowercases a family
// name: CSS family names are ASCII case-insensitive, and lowercasing here is
// what lets a `font-family` declaration and an `@font-face` rule that spell
// the family differently still meet.
func normaliseFamilyName(s string) string {
	return strings.ToLower(strings.Trim(strings.TrimSpace(s), `"'`))
}
