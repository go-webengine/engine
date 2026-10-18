// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package paint

import (
	"testing"

	"github.com/go-opentype/fonts/cabin"
	"github.com/go-opentype/opentype"
	"github.com/go-webengine/engine/css"
)

// The bundled families are parsed once and shared, but every registry is
// its own: a face registered with one is not seen by another.
func TestNewFontsSharesBundledFonts(t *testing.T) {
	a, b := NewFonts(), NewFonts()
	k := styleKey{css.Sans, false, false}
	if a.fonts[k] == nil || a.fonts[k] != b.fonts[k] {
		t.Error("the bundled sans is not one shared parsed font")
	}
	if a.fallback[faceStyle{}] == nil || a.fallback[faceStyle{}] != b.fallback[faceStyle{}] {
		t.Error("the fallback is not one shared parsed font")
	}
	if err := a.Register("Cabin", 400, false, cabin.TTF); err != nil {
		t.Fatal(err)
	}
	if !a.Registered("cabin") || b.Registered("cabin") {
		t.Error("a face registered with one registry leaked into another")
	}
}

func TestRegisterFont(t *testing.T) {
	ft, err := opentype.Parse(cabin.TTF)
	if err != nil {
		t.Fatal(err)
	}
	a, b := NewFonts(), NewFonts()
	for _, f := range []*Fonts{a, b} {
		if err := f.RegisterFont(" Cabin ", 400, false, ft); err != nil {
			t.Fatal(err)
		}
	}
	fam := named("cabin", css.GenericSans)
	if a.Measure("Partenaires", fam, 40, 400, false) != b.Measure("Partenaires", fam, 40, 400, false) {
		t.Error("one parsed font measures differently in two registries")
	}
	if err := a.RegisterFont("  ", 400, false, ft); err == nil {
		t.Error("a face with no family name was registered")
	}
	if err := a.RegisterFont("x", 400, false, nil); err == nil {
		t.Error("a nil font was registered")
	}
}
