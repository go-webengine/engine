// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package css

import "testing"

// TestResolveClipPathPolygonBareCoords covers the plain, no-calc() case:
// percentages and bare lengths resolved against a known border box.
func TestResolveClipPathPolygonBareCoords(t *testing.T) {
	pts, evenOdd, ok := ResolveClipPathPolygon("polygon(0 0, 100% 0, 50% 100%)", 200, 100)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if evenOdd {
		t.Error("no fill-rule given: should default to nonzero (evenOdd=false)")
	}
	want := []ClipPathPoint{{0, 0}, {200, 0}, {100, 100}}
	if len(pts) != len(want) {
		t.Fatalf("got %d points, want %d", len(pts), len(want))
	}
	for i, p := range pts {
		if p != want[i] {
			t.Errorf("point %d = %+v, want %+v", i, p, want[i])
		}
	}
}

// TestResolveClipPathPolygonVercelTrigger is the real confirmed trigger
// (round 194, engine#330): vercel.com's own homepage sets this exact
// clip-path, in static server-rendered markup, on an always-present
// decorative background <div> — a "paper-peel" corner cut mixing a bare
// percentage, a bare "0", and a calc() of the two.
func TestResolveClipPathPolygonVercelTrigger(t *testing.T) {
	raw := "polygon(0 0, 100% 0, 100% calc(100% - 112.00px), calc(100% - 112.00px) 100%, 0 100%)"
	pts, evenOdd, ok := ResolveClipPathPolygon(raw, 1000, 500)
	if !ok {
		t.Fatal("expected ok=true for the confirmed real trigger")
	}
	if evenOdd {
		t.Error("no fill-rule given: should default to nonzero")
	}
	want := []ClipPathPoint{
		{0, 0},
		{1000, 0},
		{1000, 500 - 112},
		{1000 - 112, 500},
		{0, 500},
	}
	if len(pts) != len(want) {
		t.Fatalf("got %d points, want %d", len(pts), len(want))
	}
	for i, p := range pts {
		if p != want[i] {
			t.Errorf("point %d = %+v, want %+v", i, p, want[i])
		}
	}
}

// TestResolveClipPathPolygonFillRule covers both the "evenodd" and
// "nonzero" leading fill-rule keywords being recognised and the latter
// (and no keyword at all) defaulting correctly.
func TestResolveClipPathPolygonFillRule(t *testing.T) {
	_, evenOdd, ok := ResolveClipPathPolygon("evenodd, polygon(0 0, 10px 0, 10px 10px)", 100, 100)
	if ok {
		t.Fatal("a comma between the fill-rule and polygon() is not the real grammar; must reject")
	}
	_, evenOdd, ok = ResolveClipPathPolygon("evenodd polygon(0 0, 10px 0, 10px 10px)", 100, 100)
	if !ok || !evenOdd {
		t.Errorf("evenOdd=%v,ok=%v, want true,true", evenOdd, ok)
	}
	_, evenOdd, ok = ResolveClipPathPolygon("nonzero polygon(0 0, 10px 0, 10px 10px)", 100, 100)
	if !ok || evenOdd {
		t.Errorf("evenOdd=%v,ok=%v, want false,true", evenOdd, ok)
	}
}

// TestResolveClipPathPolygonInvalid covers malformed/unsupported values
// being reported as ok=false, including the shape functions and the
// geometry-box keyword this engine does not model.
func TestResolveClipPathPolygonInvalid(t *testing.T) {
	for _, v := range []string{
		"circle(50%)",
		"ellipse(50% 50%)",
		"inset(10px)",
		"path('M0 0')",
		"url(#clip)",
		"polygon(0 0, 100% 0)",           // only 2 vertices — no area
		"polygon(0 0, 100% 0, bogus 0)",  // unparseable coordinate
		"polygon(0 0 0, 100% 0, 0 100%)", // 3 tokens in one pair
		"polygon(0 0, 100% 0, 0 100%",    // unterminated paren
		"border-box polygon(0 0, 100% 0, 0 100%)",
		"none",
	} {
		if _, _, ok := ResolveClipPathPolygon(v, 100, 100); ok {
			t.Errorf("ResolveClipPathPolygon(%q) unexpectedly succeeded", v)
		}
	}
}

// TestEvalCalcWithBasisMixesPercentAndPx is the independent unit-level
// check for the calc() fix: a percentage and an absolute length combining
// in the same sum once a basis is known, which evalCalc itself (the
// cascade-time, basis-less path) correctly refuses — see resolveCalc's own
// doc comment.
func TestEvalCalcWithBasisMixesPercentAndPx(t *testing.T) {
	v, ok := evalCalcWithBasis("100% - 112.00px", 500)
	if !ok || v != 388 {
		t.Errorf("evalCalcWithBasis(100%% - 112px, basis=500) = %v,%v want 388,true", v, ok)
	}
	v, ok = evalCalcWithBasis("50% + 50%", 200)
	if !ok || v != 200 {
		t.Errorf("evalCalcWithBasis(50%%+50%%, basis=200) = %v,%v want 200,true", v, ok)
	}
	if _, ok := evalCalcWithBasis("1 + 2", 200); ok {
		t.Error("a calc() with no length/percentage term at all is not a valid length — must reject")
	}
}

// TestCascadeClipPathEndToEnd proves the real confirmed trigger survives
// the FULL cascade (CascadeVW) as literal text on Style.ClipPath, including
// its calc() term — not just resolveClipPathValue/apply() in isolation.
func TestCascadeClipPathEndToEnd(t *testing.T) {
	html := `<html><body><div id="peel" style="clip-path:polygon(0 0, 100% 0, 100% calc(100% - 112.00px), calc(100% - 112.00px) 100%, 0 100%)">x</div></body></html>`
	sm := cascadeHTML(t, html)
	div := findStyleTag(t, sm, "div")
	if div.ClipPath != "polygon(0 0, 100% 0, 100% calc(100% - 112.00px), calc(100% - 112.00px) 100%, 0 100%)" {
		t.Errorf("Style.ClipPath via the real cascade = %q", div.ClipPath)
	}
}

// TestCascadeClipPathVarSubstitution covers resolveClipPathValue's own
// var()-substitution branch (not exercised by a clip-path with no var() at
// all): a custom property supplying one of the polygon's coordinates.
func TestCascadeClipPathVarSubstitution(t *testing.T) {
	html := `<html><body><div id="d" style="--cut:80%;clip-path:polygon(0 0, 100% 0, var(--cut) 100%)">x</div></body></html>`
	sm := cascadeHTML(t, html)
	div := findStyleTag(t, sm, "div")
	if div.ClipPath != "polygon(0 0, 100% 0, 80% 100%)" {
		t.Errorf("Style.ClipPath after var() substitution = %q", div.ClipPath)
	}
}

// TestCascadeClipPathUnresolvableVarDrops covers resolveClipPathValue's
// own failure branch: a var() with no matching custom property and no
// fallback must drop the whole declaration, leaving ClipPath at its
// (inherited/initial) empty value — clip-path is not inherited, so this is
// simply "no clip", not a stale previous value.
func TestCascadeClipPathUnresolvableVarDrops(t *testing.T) {
	html := `<html><body><div id="d" style="clip-path:polygon(0 0, var(--missing) 0, 0 100%)">x</div></body></html>`
	sm := cascadeHTML(t, html)
	div := findStyleTag(t, sm, "div")
	if div.ClipPath != "" {
		t.Errorf("Style.ClipPath with an unresolvable var() = %q, want empty (declaration dropped)", div.ClipPath)
	}
}

// TestApplyClipPathUnsupportedShapeLeavesUnchanged covers parse.go's own
// "an unsupported value drops the declaration" convention: an unrecognised
// clip-path value (a shape this engine does not model) must leave a
// pre-existing ClipPath untouched, not clear or corrupt it.
func TestApplyClipPathUnsupportedShapeLeavesUnchanged(t *testing.T) {
	s := &Style{ClipPath: "polygon(0 0, 1px 0, 1px 1px)"}
	s.apply(Declaration{Property: "clip-path", Value: "circle(50%)"}, 16, nil)
	if s.ClipPath != "polygon(0 0, 1px 0, 1px 1px)" {
		t.Errorf("unsupported clip-path value via apply() = %q, want unchanged", s.ClipPath)
	}
	s.apply(Declaration{Property: "clip-path", Value: "none"}, 16, nil)
	if s.ClipPath != "" {
		t.Errorf("clip-path:none via apply() = %q, want cleared to empty", s.ClipPath)
	}
}

// TestResolveCalcStillRejectsMixedKinds is the regression guard: the
// ordinary, basis-less evalCalc/resolveCalc path used by every OTHER
// property must still defer (reject) a mixed percent+px calc() exactly as
// before — this round's fix is scoped to clip-path's own basis-aware path,
// not a change to calc()'s general cascade-time behaviour.
func TestResolveCalcStillRejectsMixedKinds(t *testing.T) {
	if _, ok := resolveCalc("calc(100% - 112px)"); ok {
		t.Error("resolveCalc must still reject a mixed percent+px calc() — this is a pre-existing, separately-disclosed limitation this round does not touch")
	}
}
