// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package css

import (
	"testing"
)

// TestResolveFontSizeLengthVW is the independent bug this round found and
// fixed alongside clamp(): a bare `font-size:13vw` was silently resolved
// against the PARENT's font-size (the generic Length.IsPercent path every
// other property's percentage uses) instead of the VIEWPORT width — two
// unrelated bases this engine's Length type cannot tell apart once a vw
// value collapses into the same IsPercent flag a genuine `%` uses.
func TestResolveFontSizeLengthVW(t *testing.T) {
	px, ok := resolveFontSizeLength("13vw", 1024, 20)
	if !ok || px != 133.12 {
		t.Errorf("resolveFontSizeLength(13vw, vw=1024, emRef=20) = %v,%v want 133.12,true", px, ok)
	}
}

// TestResolveFontSizeLengthDVW is round 193's own reopening of round 190's
// bug for the three-character viewport-width variants (dvw/svw/lvw): this
// function's vw branch runs BEFORE ever reaching parseLength's own fix, so
// it needed the identical ordering correction independently. No confirmed
// trigger uses a viewport unit on font-size specifically yet (the confirmed
// trigger, chakra-ui.com's `body{min-height:100dvh}`, is a different
// property) — fixed proactively since it is the exact same failure mode in
// the sibling function, not a speculative new capability.
func TestResolveFontSizeLengthDVW(t *testing.T) {
	for _, u := range []string{"dvw", "svw", "lvw"} {
		px, ok := resolveFontSizeLength("13"+u, 1024, 20)
		if !ok || px != 133.12 {
			t.Errorf("resolveFontSizeLength(13%s, vw=1024, emRef=20) = %v,%v want 133.12,true", u, px, ok)
		}
	}
}

// TestResolveFontSizeLengthPercentStillUsesEmRef is the regression guard for
// the case that was ALREADY correct: a genuine `%` on font-size resolves
// against the parent's font-size (CSS Fonts 4 §5.4), not the viewport —
// confirming the vw fix above did not accidentally repurpose the percentage
// path too.
func TestResolveFontSizeLengthPercentStillUsesEmRef(t *testing.T) {
	px, ok := resolveFontSizeLength("150%", 1024, 20)
	if !ok || px != 30 {
		t.Errorf("resolveFontSizeLength(150%%, vw=1024, emRef=20) = %v,%v want 30,true", px, ok)
	}
}

// TestResolveFontSizeLengthPlainUnits covers the ordinary, non-relative case
// still works unchanged (rem, px).
func TestResolveFontSizeLengthPlainUnits(t *testing.T) {
	if px, ok := resolveFontSizeLength("2rem", 1024, 20); !ok || px != 32 {
		t.Errorf("resolveFontSizeLength(2rem) = %v,%v want 32,true", px, ok)
	}
	if px, ok := resolveFontSizeLength("18px", 1024, 20); !ok || px != 18 {
		t.Errorf("resolveFontSizeLength(18px) = %v,%v want 18,true", px, ok)
	}
	if _, ok := resolveFontSizeLength("auto", 1024, 20); ok {
		t.Error("resolveFontSizeLength(auto) should fail — not a valid font-size")
	}
	if _, ok := resolveFontSizeLength("bogus", 1024, 20); ok {
		t.Error("resolveFontSizeLength(bogus) should fail")
	}
}

// TestParseClampFontSize is the real confirmed trigger (round 190,
// engine#326): carbondesignsystem.com's own compiled stylesheet sets
// `font-size:clamp(4rem,13vw,12rem)` for fluid heading sizes. At a 1024px
// viewport the preferred (13vw = 133.12px) term falls between the min
// (4rem=64px) and max (12rem=192px) bounds, so it wins outright.
func TestParseClampFontSize(t *testing.T) {
	px, ok := parseClampFontSize("clamp(4rem,13vw,12rem)", 1024, 16)
	if !ok || px != 133.12 {
		t.Errorf("parseClampFontSize(clamp(4rem,13vw,12rem), vw=1024) = %v,%v want 133.12,true", px, ok)
	}
}

// TestParseClampFontSizeMinWins covers a narrow viewport pushing the
// preferred vw value below the minimum — the min must win.
func TestParseClampFontSizeMinWins(t *testing.T) {
	// 13vw at 300px = 39px, below the 4rem=64px floor.
	px, ok := parseClampFontSize("clamp(4rem, 13vw, 12rem)", 300, 16)
	if !ok || px != 64 {
		t.Errorf("parseClampFontSize at a narrow viewport = %v,%v want 64,true (the min)", px, ok)
	}
}

// TestParseClampFontSizeMaxWins covers a wide viewport pushing the preferred
// vw value above the maximum — the max must win.
func TestParseClampFontSizeMaxWins(t *testing.T) {
	// 13vw at 2000px = 260px, above the 12rem=192px ceiling.
	px, ok := parseClampFontSize("clamp(4rem, 13vw, 12rem)", 2000, 16)
	if !ok || px != 192 {
		t.Errorf("parseClampFontSize at a wide viewport = %v,%v want 192,true (the max)", px, ok)
	}
}

// TestParseClampFontSizeDegenerateMinAboveMax covers CSS Values 4 §10.4's own
// formula applied exactly (max(MIN, min(PREFERRED, MAX))), not a sequential
// clamp — the two differ when an author gives MIN > MAX. Not a confirmed
// real trigger, but costs nothing to get right once the formula is written
// out explicitly.
func TestParseClampFontSizeDegenerateMinAboveMax(t *testing.T) {
	// min=50px, pref=30px, max=10px. Spec: max(50, min(30,10)) = max(50,10) = 50.
	px, ok := parseClampFontSize("clamp(50px, 30px, 10px)", 1024, 16)
	if !ok || px != 50 {
		t.Errorf("parseClampFontSize(min>max) = %v,%v want 50,true (the spec formula, not a sequential clamp)", px, ok)
	}
}

// TestParseClampFontSizeInvalid covers malformed/unsupported values failing
// cleanly (ok=false), including the deliberately-out-of-scope percentage
// case (see parseClampFontSize's own doc comment for why: no confirmed
// trigger mixes a percentage into a clamp() term the way vw/rem are used).
func TestParseClampFontSizeInvalid(t *testing.T) {
	for _, v := range []string{
		"clamp(4rem,13vw)",       // only 2 args
		"clamp(4rem,13vw,12rem,1rem)", // 4 args
		"clamp(4rem,13vw,bogus)", // unparseable third term
		"clamp(4rem,13vw,12rem", // unterminated paren
		"notclamp(4rem,13vw,12rem)",
		"13vw", // not a clamp() call at all
	} {
		if _, ok := parseClampFontSize(v, 1024, 16); ok {
			t.Errorf("parseClampFontSize(%q) unexpectedly succeeded", v)
		}
	}
}

// TestApplyFontSizeClampAndBareVW exercises the real cascade entry point
// (Style.apply, variadic vw argument) for both the clamp() trigger and the
// independently-fixed bare vw case, and confirms an invalid clamp() leaves
// a pre-existing FontSize untouched (this codebase's standard "an invalid
// declaration is dropped by the cascade" convention).
func TestApplyFontSizeClampAndBareVW(t *testing.T) {
	s := &Style{}
	s.apply(Declaration{Property: "font-size", Value: "clamp(4rem,13vw,12rem)"}, 16, nil, 1024)
	if s.FontSize != 133.12 {
		t.Errorf("font-size:clamp(...) via apply() = %v, want 133.12", s.FontSize)
	}

	s = &Style{}
	s.apply(Declaration{Property: "font-size", Value: "13vw"}, 16, nil, 1024)
	if s.FontSize != 133.12 {
		t.Errorf("font-size:13vw via apply() = %v, want 133.12 (the independently-fixed bare-vw case)", s.FontSize)
	}

	// No vw argument at all (the 60+ existing test call sites' own shape):
	// a bare vw-unit font-size cannot resolve without a viewport width, so
	// it is correctly dropped (ok=false), not silently resolved against 0.
	s = &Style{}
	s.apply(Declaration{Property: "font-size", Value: "13vw"}, 16, nil)
	if s.FontSize != 0 {
		t.Errorf("font-size:13vw with no vw argument = %v, want 0 (resolves against vw=0, the documented default)", s.FontSize)
	}

	s = &Style{FontSize: 42}
	s.apply(Declaration{Property: "font-size", Value: "clamp(4rem,13vw)"}, 16, nil, 1024)
	if s.FontSize != 42 {
		t.Errorf("invalid clamp() via apply() = %v, want unchanged 42", s.FontSize)
	}
}

// TestCascadeFontSizeClampEndToEnd proves the real confirmed trigger
// resolves through the FULL cascade (CascadeVW), not just Style.apply in
// isolation — the same "verify through the real pipeline" discipline this
// session's other rounds (e.g. oklch's own cascade test) already follow.
func TestCascadeFontSizeClampEndToEnd(t *testing.T) {
	// cascadeHTML's own Cascade(root) resolves at DefaultViewportWidth
	// (1024px — see parse.go), the same width every expected value above
	// was computed against.
	html := `<html><head><style>h1{font-size:clamp(4rem,13vw,12rem)}</style></head><body><h1>Heading</h1></body></html>`
	sm := cascadeHTML(t, html)
	h1 := findStyleTag(t, sm, "h1")
	if h1.FontSize != 133.12 {
		t.Errorf("h1 font-size via the real cascade (DefaultViewportWidth=1024) = %v, want 133.12", h1.FontSize)
	}
}
