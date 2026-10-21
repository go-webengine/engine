// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package paint

import (
	"testing"

	"github.com/go-webengine/engine/css"
)

// TestMeasureCachesRepeatedCalls proves the cache actually fires, not just
// that Measure is correct (determinism alone would make a plain "call twice,
// compare" test pass even with no cache at all). A cache hit skips Runs and
// every run's Face.Shape entirely — both allocate (Runs builds a []Run via
// strings.Builder; Shape builds a []GlyphID) — so AllocsPerRun on a REPEATED
// call should land at ~0, where the first (necessarily uncached) call does
// not. Found live on cia.gov: ~25% of total render CPU time was in Measure,
// almost entirely re-shaping text this engine had already shaped at the
// exact same size earlier in the SAME render (a repeated nav-link label, a
// repeated button string, …) — this engine's own layout algorithms (line-
// breaking, intrinsic-width estimation) call Measure on the same run more
// than once by design, not as a bug.
func TestMeasureCachesRepeatedCalls(t *testing.T) {
	f := NewFonts()
	const text = "a moderately long repeated string, long enough that shaping it costs something real"
	want := f.Measure(text, css.Sans, 16, 400, false)
	allocs := testing.AllocsPerRun(100, func() {
		if w := f.Measure(text, css.Sans, 16, 400, false); w != want {
			t.Fatalf("cached Measure = %v, want %v", w, want)
		}
	})
	if allocs > 1 {
		t.Errorf("Measure allocated %.1f times per cached call, want ~0 (the cache should skip Runs/Shape entirely on a repeat)", allocs)
	}
}

// TestMeasureCacheDistinguishesEveryKeyDimension confirms the cache key
// covers every argument Measure's result actually depends on: changing any
// ONE of text/family/size/weight/italic while holding the rest fixed must
// change the measured width (or at least not silently reuse an unrelated
// cache entry) — a key missing a dimension would alias two different real
// measurements together.
func TestMeasureCacheDistinguishesEveryKeyDimension(t *testing.T) {
	f := NewFonts()
	base := f.Measure("sample text", css.Sans, 16, 400, false)
	variants := map[string]float64{
		"text":   f.Measure("different text", css.Sans, 16, 400, false),
		"family": f.Measure("sample text", css.Serif, 16, 400, false),
		"size":   f.Measure("sample text", css.Sans, 24, 400, false),
		"weight": f.Measure("sample text", css.Sans, 16, 700, false),
		"italic": f.Measure("sample text", css.Sans, 16, 400, true),
	}
	for dim, w := range variants {
		if w == base {
			t.Errorf("varying %s gave the SAME width as the base case (%v) -- cache key may be missing this dimension", dim, base)
		}
	}
}

// BenchmarkMeasureRepeatedText exercises the pattern round 176 found
// dominating a real page's layout cost: the SAME text measured repeatedly
// (line-breaking and intrinsic-width computation both re-measure a run more
// than once; a real page also just repeats the same short strings --
// navigation labels, button text -- many times over).
func BenchmarkMeasureRepeatedText(b *testing.B) {
	f := NewFonts()
	const text = "Learn more about this topic"
	for i := 0; i < b.N; i++ {
		f.Measure(text, css.Sans, 16, 400, false)
	}
}
