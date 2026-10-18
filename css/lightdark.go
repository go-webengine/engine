// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package css

import "strings"

// ColorScheme is the computed value of the inherited `color-scheme` property
// (CSS Color Adjust 1 §2.2) reduced to what this engine acts on: which of the
// two UA-supported schemes the element declares support for. The zero value
// is `normal` (no scheme declared). Custom-ident schemes are ignored, and so
// is `only` (it forbids UA overrides such as forced colours, which this
// engine never applies).
type ColorScheme uint8

const (
	ColorSchemeLight ColorScheme = 1 << iota // `light` listed
	ColorSchemeDark                          // `dark` listed
)

// parseColorScheme parses a `color-scheme` value. ok is false for a value
// that is not valid per the grammar `normal | [ light | dark | <custom-ident> ]+
// && only?` as far as this engine can tell (empty, or `normal` mixed with
// other keywords), so the declaration is dropped.
func parseColorScheme(v string) (ColorScheme, bool) {
	fields := strings.Fields(strings.ToLower(v))
	if len(fields) == 0 {
		return 0, false
	}
	if len(fields) == 1 && fields[0] == "normal" {
		return 0, true
	}
	var cs ColorScheme
	idents := 0
	for _, f := range fields {
		switch f {
		case "normal", "initial", "unset", "revert", "default", "none":
			return 0, false
		case "only":
			continue
		case "light":
			cs |= ColorSchemeLight
		case "dark":
			cs |= ColorSchemeDark
		}
		idents++
	}
	if idents == 0 {
		return 0, false // `only` alone
	}
	return cs, true
}

// UsedDark reports whether the element's used colour scheme is dark, per the
// "determine the used color scheme" algorithm of CSS Color Adjust 1 §2.3 with
// this engine's fixed preference: when an element supports BOTH schemes the
// preferred one is used, which here is dark — the same fixed answer
// js/window.go's matchMedia gives for `prefers-color-scheme` (the bench suite's
// reference Chromium runs with a dark appearance). One supported scheme is
// used as is. With none (`normal`) the UA default scheme applies, which is
// light: Gecko's LookAndFeel::ColorSchemeForStyle ("Otherwise default content
// to light"), and Chromium likewise renders a page that never opts in light.
func (cs ColorScheme) UsedDark() bool {
	return cs&ColorSchemeDark != 0
}

// resolveLightDark replaces every top-level light-dark(light, dark) function in
// value with the branch the element's used colour scheme picks (CSS Color 5
// §7: the first colour when the element colour scheme is light, the second
// when it is dark), returning false (declaration dropped, same as an
// unresolvable var()) when a light-dark() is malformed: unterminated, or
// without exactly one top-level comma splitting its two arguments.
func resolveLightDark(value string, dark bool) (string, bool) {
	if findLightDarkFunc(value, 0) < 0 {
		return value, true
	}
	var b strings.Builder
	i := 0
	for i < len(value) {
		rel := findLightDarkFunc(value[i:], 0)
		if rel < 0 {
			b.WriteString(value[i:])
			break
		}
		abs := i + rel
		b.WriteString(value[i:abs])
		open := abs + len("light-dark") // index of '('
		closeIdx, ok := matchParen(value, open)
		if !ok {
			return "", false
		}
		inner := value[open+1 : closeIdx]
		light, darkArg, hasDark := splitVarArgs(inner)
		if !hasDark {
			return "", false
		}
		pick := light
		if dark {
			pick = darkArg
		}
		b.WriteString(strings.TrimSpace(pick))
		i = closeIdx + 1
	}
	return b.String(), true
}

// findLightDarkFunc returns the index (relative to from) of the next
// "light-dark(" token in s at or after from, or -1 if none. Mirrors
// findVarFunc's guard against matching inside a longer identifier.
func findLightDarkFunc(s string, from int) int {
	for i := from; ; {
		j := indexFold(s[i:], "light-dark(")
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
