// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package paint

import (
	"image"
	"testing"

	"github.com/go-webengine/engine/css"
)

// Inter covers arrows and ①–⑩ but not box drawing or braille; Lora has
// neither an arrow nor a circled digit; Go Mono lacks the circled digit.
// DejaVu Sans has all of them; a character neither covers (CJK) stays
// with the family.
func TestRunsSplitByGlyphCoverage(t *testing.T) {
	f := NewFonts()
	got := f.Runs("a─b ⠿ ⠿ 中", css.Sans, 400, false)
	want := []Run{{"a", false}, {"─", true}, {"b ", false}, {"⠿ ⠿ ", true}, {"中", false}}
	if len(got) != len(want) {
		t.Fatalf("runs %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("run %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if r := f.Runs("x↔y①", css.Serif, 700, true); len(r) != 4 || r[1] != (Run{"↔", true}) || r[2] != (Run{"y", false}) || r[3] != (Run{"①", true}) {
		t.Errorf("serif bold italic = %+v, want x | ↔ | y | ① (Lora has neither the arrow nor the digit)", r)
	}
	if r := f.Runs("①", css.Mono, 400, false); len(r) != 1 || !r[0].Fallback {
		t.Errorf("mono ① = %+v, want one fallback run", r)
	}
	if r := f.Runs("plain ↔ ①", css.Sans, 400, false); len(r) != 1 || r[0].Fallback {
		t.Errorf("text Inter covers = %+v, want one family run", r)
	}
	if r := f.Runs("", css.Mono, 400, false); r != nil {
		t.Errorf("empty text = %+v, want nil", r)
	}
	if r := f.Runs(" ─", css.Sans, 400, false); len(r) != 2 || r[0].Fallback || r[0].Text != " " || !r[1].Fallback {
		t.Errorf("leading space then fallback = %+v", r)
	}
}

// Measure counts the fallback glyphs' advances, so layout reserves room
// for them; and the painter draws them, so they are not blank.
func TestFallbackGlyphsAreMeasuredAndDrawn(t *testing.T) {
	f := NewFonts()
	ab := f.Measure("ab", css.Serif, 16, 400, false)
	arrow := f.Measure("a↔b", css.Serif, 16, 400, false)
	if arrow <= ab {
		t.Errorf("Measure(a↔b) = %v, not wider than Measure(ab) = %v", arrow, ab)
	}
	for _, italic := range []bool{false, true} {
		for _, weight := range []int{400, 700} {
			if w := f.Measure("①", css.Serif, 20, weight, italic); w <= 0 {
				t.Errorf("weight %d italic %v: ① measures %v", weight, italic, w)
			}
		}
	}
	dst := image.NewRGBA(image.Rect(0, 0, 60, 30))
	st := &css.Style{FontFamily: css.Serif, FontSize: 20, FontWeight: 400, Color: css.Color{R: 0, G: 0, B: 0, A: 255}}
	end := drawText(dst, f, st, "↔", 2, 22, st.Color, dst.Bounds())
	if end <= 2 {
		t.Fatalf("pen did not advance: %d", end)
	}
	inked := 0
	for i := 3; i < len(dst.Pix); i += 4 {
		if dst.Pix[i] != 0 {
			inked++
		}
	}
	if inked == 0 {
		t.Error("↔ drew no pixel")
	}
	// The same size twice hits the face cache.
	if f.fallbackFace(20, false, false) != f.fallbackFace(20.2, false, false) {
		t.Error("fallback face not cached by rounded size")
	}
	if f.fallbackFace(0.2, false, false) == nil {
		t.Error("a sub-pixel size still yields a face")
	}
}
