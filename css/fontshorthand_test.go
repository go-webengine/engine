// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package css

import (
	"strings"
	"testing"
)

// The `font` shorthand (CSS Fonts 4 §2.7) expands to the longhands it covers,
// resetting style, weight and line-height when it omits them
// (go-webengine/engine#404).
func TestFontShorthandExpansion(t *testing.T) {
	for _, tc := range []struct {
		in                                 string
		style, weight, size, lineH, family string
	}{
		{`32px V, serif`, "normal", "normal", "32px", "normal", "V, serif"},
		{`32px/40px V,serif`, "normal", "normal", "32px", "40px", "V,serif"},
		{`italic bold 12px/1.5 "Open Sans", sans-serif`, "italic", "bold", "12px", "1.5", `"Open Sans", sans-serif`},
		{`700 small-caps condensed 1em Georgia`, "normal", "700", "1em", "normal", "Georgia"},
		{`oblique 10deg lighter 14px Arial`, "oblique", "lighter", "14px", "normal", "Arial"},
		{`normal normal 16px Arial`, "normal", "normal", "16px", "normal", "Arial"},
		{`20px /1.2 Arial`, "normal", "normal", "20px", "1.2", "Arial"},
		{`20px / 1.2 Arial`, "normal", "normal", "20px", "1.2", "Arial"},
		{`20px/ 1.2 Arial`, "normal", "normal", "20px", "1.2", "Arial"},
		{`calc(10px + 2px) Arial`, "normal", "normal", "calc(10px + 2px)", "normal", "Arial"},
	} {
		got := ParseDeclarations("font:" + tc.in)
		want := []Declaration{
			{Property: "font-style", Value: tc.style}, {Property: "font-weight", Value: tc.weight},
			{Property: "font-size", Value: tc.size}, {Property: "line-height", Value: tc.lineH},
			{Property: "font-family", Value: tc.family},
		}
		if len(got) != len(want) {
			t.Errorf("%q -> %v", tc.in, got)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("%q: %v, want %v", tc.in, got[i], want[i])
			}
		}
	}
	if d := ParseDeclarations("font:14px Arial !important"); len(d) != 5 || !d[0].Important || !d[4].Important {
		t.Errorf("!important lost: %v", d)
	}
	// Invalid or unresolvable at parse time: nothing, as before.
	for _, bad := range []string{`bold Arial`, `12px`, `12px/`, `12px /`, `12px/1.5`, `inherit`, `var(--f)`, `italic`} {
		if d := ParseDeclarations("font:" + bad); len(d) != 0 {
			t.Errorf("%q expanded to %v", bad, d)
		}
	}
}

// End to end: the shorthand reaches the computed style, family included, and a
// later longhand still wins over it.
func TestFontShorthandCascade(t *testing.T) {
	p := styleOf(t, `<html><head><style>p{font:italic 700 32px/48px Foo,serif}</style></head><body><p>a</p></body></html>`, "p")
	if p.FontSize != 32 || p.FontWeight != 700 || !p.Italic || !strings.Contains(p.FontFamily.Names, "foo") {
		t.Errorf("p: size %v weight %d italic %v family %q", p.FontSize, p.FontWeight, p.Italic, p.FontFamily.Names)
	}
	q := styleOf(t, `<html><head><style>q{font:32px Foo; font-size:20px}</style></head><body><q>b</q></body></html>`, "q")
	if q.FontSize != 20 || q.FontFamily.Names != "foo" {
		t.Errorf("q: size %v family %q", q.FontSize, q.FontFamily.Names)
	}
}
