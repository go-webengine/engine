// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package paint

import (
	"errors"
	"os"
	"testing"

	"github.com/go-opentype/opentype"
	"github.com/go-webengine/engine/css"
)

// Outfit (SIL OFL) is a variable font with a wght axis, 100..900.
func variableFonts(t *testing.T) (*Fonts, css.FontFamily) {
	t.Helper()
	b, err := os.ReadFile("../testdata/fonts/Outfit-wght.ttf")
	if err != nil {
		t.Fatal(err)
	}
	f := NewFonts()
	if err := f.Register("v", 100, false, b); err != nil {
		t.Fatal(err)
	}
	return f, css.FontFamily{Names: "v", Generic: css.GenericSans}
}

// A variable font is instanced at each CSS weight: the advances change with it
// (go-webengine/engine#399), a weight outside the axis is clamped to its end,
// and a font without a weight axis keeps one face per bold slot.
func TestVariableFontWeightInstances(t *testing.T) {
	f, fam := variableFonts(t)
	var prev float64
	for _, w := range []int{100, 300, 500, 700, 900} {
		m := f.Measure("Weight sample", fam, 32, w, false)
		if m <= prev {
			t.Errorf("weight %d: width %v not above %v", w, m, prev)
		}
		prev = m
	}
	if f.styleFace(fam, 32, 1000, false) != f.styleFace(fam, 32, 900, false) ||
		f.styleFace(fam, 32, 20, false) != f.styleFace(fam, 32, 100, false) {
		t.Error("a weight outside the axis range is not clamped to its end")
	}
	if f.styleFace(fam, 32, 400, false) == f.styleFace(fam, 32, 500, false) {
		t.Error("two weights share one face of a variable font")
	}
	// A static font (Go Mono) ignores the weight; the bundled Inter and Lora
	// regulars are variable, so 500 is its own face but 400 is the default
	// master, drawn on the unvaried path.
	g := NewFonts()
	if g.styleFace(css.Mono, 16, 400, false) != g.styleFace(css.Mono, 16, 500, false) {
		t.Error("a static font gets one face per weight")
	}
	if g.styleFace(css.Sans, 16, 400, false) == g.styleFace(css.Sans, 16, 500, false) {
		t.Error("the bundled variable Inter ignores weight 500")
	}
	if g.styleFace(css.Sans, 16, 400, false) == g.styleFace(css.Sans, 16, 700, false) {
		t.Error("the bold slot was lost")
	}
}

// Registering a face after the style was resolved re-asks which font answers.
func TestVariableFontRegisterAfterUse(t *testing.T) {
	f, fam := variableFonts(t)
	a := f.styleFace(fam, 32, 400, false)
	b, err := os.ReadFile("../testdata/fonts/Outfit-wght.ttf")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Register("v", 400, false, b); err != nil { // nearer the slot's 400: displaces
		t.Fatal(err)
	}
	if f.styleFace(fam, 32, 400, false) == a {
		t.Error("the face cache survived a registration")
	}
}

// glyphFont is the static instance a PDF embeds.
func TestGlyphFontInstance(t *testing.T) {
	f, fam := variableFonts(t)
	style := styleKey{fam, true, false}
	bold := f.glyphFont(style, 900)
	if len(bold.Axes()) != 0 {
		t.Errorf("instance still has axes: %v", bold.Axes())
	}
	if f.glyphFont(style, 900) != bold || f.glyphFont(style, 1000) != bold {
		t.Error("instances are not cached per clamped weight")
	}
	if f.glyphFont(style, 100) == bold {
		t.Error("two weights share one instance")
	}
	// A static font is its own glyph font.
	g := NewFonts()
	k := styleKey{css.Mono, false, false}
	if g.glyphFont(k, 400) != g.font(k) || g.glyphFont(styleKey{css.Sans, false, false}, 400) != g.font(styleKey{css.Sans, false, false}) {
		t.Error("a static font, or a variable one at its default, was replaced")
	}
	// An instance that cannot be built draws as the default master.
	defer func(old func(*opentype.Font, map[string]float64) (*opentype.Font, error)) { instantiate = old }(instantiate)
	instantiate = func(*opentype.Font, map[string]float64) (*opentype.Font, error) { return nil, errors.New("no") }
	if got := f.glyphFont(style, 300); got != f.font(style) {
		t.Error("a failed instance was not replaced by the default master")
	}
}

// The raster painter's faces are built at the exact size, cached per size and
// weight, and cleared by a registration; the fallback face likewise.
func TestDrawFaceExactSizeCache(t *testing.T) {
	f, fam := variableFonts(t)
	run := Run{Text: "a"}
	a := f.drawFace(run, fam, 22.4, 400, false)
	if a.Size() != 22.4 || f.drawFace(run, fam, 22.4, 400, false) != a {
		t.Errorf("exact face: size %v, cached %v", a.Size(), f.drawFace(run, fam, 22.4, 400, false) == a)
	}
	if f.drawFace(run, fam, 22.5, 400, false) == a || f.drawFace(run, fam, 22.4, 700, false) == a {
		t.Error("a size or weight shares a face")
	}
	if f.drawFace(run, fam, 0.2, 400, false).Size() != 1 {
		t.Error("a size under 1px is not clamped")
	}
	fb := Run{Text: "↔", Fallback: true}
	b := f.drawFace(fb, fam, 22.4, 400, false)
	if b.Size() != 22.4 || f.drawFace(fb, fam, 22.4, 400, false) != b {
		t.Error("the fallback exact face is not cached")
	}
	bb, err := os.ReadFile("../testdata/fonts/Outfit-wght.ttf")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Register("v", 400, false, bb); err != nil {
		t.Fatal(err)
	}
	if f.drawFace(run, fam, 22.4, 400, false) == a {
		t.Error("the exact-face cache survived a registration")
	}
}
