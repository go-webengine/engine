// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package css

import "testing"

func TestResolveLightDarkPicksBranchByScheme(t *testing.T) {
	got, ok := resolveLightDark("light-dark(white, black)", true)
	if !ok || got != "black" {
		t.Fatalf("resolveLightDark dark = %q,%v want %q,true", got, ok, "black")
	}
	got, ok = resolveLightDark("light-dark(white, black)", false)
	if !ok || got != "white" {
		t.Fatalf("resolveLightDark light = %q,%v want %q,true", got, ok, "white")
	}
}

func TestResolveLightDarkNoOccurrence(t *testing.T) {
	got, ok := resolveLightDark("1px solid red", true)
	if !ok || got != "1px solid red" {
		t.Fatalf("resolveLightDark no-op = %q,%v", got, ok)
	}
}

func TestResolveLightDarkMultipleAndNestedCommas(t *testing.T) {
	// Two calls in one value, plain text trailing the last call (exercising the
	// "no further match" loop exit), and a branch value that itself contains a
	// top-level comma (e.g. rgb(...)) which must not be mistaken for the
	// separator.
	got, ok := resolveLightDark("light-dark(rgb(1,2,3), rgb(4,5,6)) light-dark(a, b) !important", true)
	if !ok || got != "rgb(4,5,6) b !important" {
		t.Fatalf("resolveLightDark multi = %q,%v", got, ok)
	}
}

func TestResolveLightDarkMalformed(t *testing.T) {
	if _, ok := resolveLightDark("light-dark(onlyone)", true); ok {
		t.Error("missing second argument should be invalid")
	}
	if _, ok := resolveLightDark("light-dark(a, b", true); ok {
		t.Error("unterminated light-dark( should be invalid")
	}
}

func TestResolveLightDarkIdentifierBoundary(t *testing.T) {
	// "light-dark(" preceded by an identifier char is not a real function
	// token (mirrors findVarFunc's own boundary guard).
	if got := findLightDarkFunc("mylight-dark(a,b)", 0); got != -1 {
		t.Errorf("mylight-dark( matched at %d", got)
	}
}

// TestCascadeLightDarkThroughVar covers the real end-to-end shape seen live on
// developer.mozilla.org: a custom property's raw value is itself wrapped in
// light-dark(), reached only after var() substitution unwraps it — the
// light-dark() resolution in resolveDeclValue must run on the FINAL
// substituted text, not just on a literal declaration.
func TestCascadeLightDarkThroughVar(t *testing.T) {
	// `color-scheme: light dark` (what a page opting into both schemes
	// declares on :root) supports both, so the engine's preferred scheme,
	// dark, is used (CSS Color Adjust 1 §2.3).
	st := styleOf(t, `<html style="color-scheme:light dark"><body>`+
		`<p style="--bg:light-dark(white,black);background-color:var(--bg)">x</p>`+
		`</body></html>`, "p")
	if st.Background != (Color{0, 0, 0, 255}) {
		t.Errorf("background = %v, want black (dark branch)", st.Background)
	}
	// No color-scheme anywhere: the UA default scheme, light.
	st = styleOf(t, `<html><body>`+
		`<p style="--bg:light-dark(white,black);background-color:var(--bg)">x</p>`+
		`</body></html>`, "p")
	if st.Background != (Color{255, 255, 255, 255}) {
		t.Errorf("background = %v, want white (light branch, color-scheme normal)", st.Background)
	}
}

// TestCascadeColorSchemeKeywords covers the CSS-wide keywords on the
// inherited color-scheme property: inherit/unset take the parent's value,
// initial resets to normal (light).
func TestCascadeColorSchemeKeywords(t *testing.T) {
	for _, tc := range []struct {
		kw   string
		want ColorScheme
	}{
		{"inherit", ColorSchemeDark},
		{"unset", ColorSchemeDark},
		{"initial", 0},
		{"bogus-only only", 0},           // custom ident: valid, no UA scheme
		{"normal dark", ColorSchemeDark}, // invalid: dropped, inherited value stays
	} {
		st := styleOf(t, `<html><body><div style="color-scheme:dark">`+
			`<p style="color-scheme:`+tc.kw+`">x</p></div></body></html>`, "p")
		if st.ColorScheme != tc.want {
			t.Errorf("color-scheme:%s = %v, want %v", tc.kw, st.ColorScheme, tc.want)
		}
	}
	var s Style
	s.apply(Declaration{Property: "color-scheme", Value: "unset"}, 16, nil) // bare apply: no parent
	if s.ColorScheme != 0 {
		t.Errorf("bare unset = %v, want normal", s.ColorScheme)
	}
}

func TestParseColorScheme(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want ColorScheme
		ok   bool
	}{
		{"normal", 0, true},
		{"light", ColorSchemeLight, true},
		{"dark", ColorSchemeDark, true},
		{"light dark", ColorSchemeLight | ColorSchemeDark, true},
		{"only dark", ColorSchemeDark, true},
		{"dark only", ColorSchemeDark, true},
		{"sepia", 0, true},
		{"only", 0, false},
		{"normal light", 0, false},
		{"", 0, false},
	} {
		got, ok := parseColorScheme(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("parseColorScheme(%q) = %v,%v want %v,%v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
	if (ColorSchemeLight|ColorSchemeDark).UsedDark() != true || ColorScheme(0).UsedDark() || ColorSchemeLight.UsedDark() {
		t.Error("UsedDark: want dark for {light,dark} (preferred), light for normal and light")
	}
}
