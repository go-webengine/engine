// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package paint

import (
	"os"
	"testing"

	"github.com/go-opentype/opentype"
	"github.com/go-webengine/engine/css"
)

// BenchmarkGlyphFontPages is a vector export: one NewFonts per page, each
// asking for the instances of a variable font at a few weights
// (go-webengine/engine#443). One op is a cold 20-page export.
func BenchmarkGlyphFontPages(b *testing.B) {
	raw, err := os.ReadFile("../testdata/fonts/Outfit-wght.ttf")
	if err != nil {
		b.Fatal(err)
	}
	ft, err := opentype.Parse(raw)
	if err != nil {
		b.Fatal(err)
	}
	style := styleKey{css.FontFamily{Names: "v", Generic: css.GenericSans}, false, false}
	b.ReportAllocs()
	for b.Loop() {
		resetSharedInstances() // a cold cache: one document's export
		for range 20 {
			f := NewFonts()
			if err := f.RegisterFont("v", 100, false, ft); err != nil {
				b.Fatal(err)
			}
			for _, w := range []int{300, 400, 500, 700} {
				f.glyphFont(style, w)
			}
		}
	}
}
