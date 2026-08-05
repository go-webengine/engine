// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

// Package paint rasterises a laid-out box tree onto an *image.RGBA using
// go-opentype for anti-aliased text, go-widgets/painter for backgrounds and
// go-images-decoded bitmaps for <img>. It also provides the Measurer the
// layout package needs (advances + vertical metrics from real font faces).
package paint

import (
	"github.com/go-opentype/fonts/gomono"
	"github.com/go-opentype/fonts/inter"
	"github.com/go-opentype/fonts/lora"
	"github.com/go-opentype/opentype"
	"github.com/go-webengine/engine/css"
)

// styleKey identifies one concrete font file: a family in a given weight class
// (bold or not) and slant (italic or not). The four combinations are the real
// static faces bundled by go-opentype/fonts — no faux-bold or synthesised
// oblique.
type styleKey struct {
	fam    css.FontFamily
	bold   bool
	italic bool
}

// Fonts is a registry of parsed font families (per family × bold × italic) with
// a per-size Face cache. It is not safe for concurrent use (Faces cache
// glyphs); build one per render.
type Fonts struct {
	fonts map[styleKey]*opentype.Font
	faces map[faceKey]*opentype.Face
}

type faceKey struct {
	style styleKey
	size  int
}

// NewFonts parses the bundled families in all four styles: sans = Inter, serif =
// Lora (both with real Bold/Italic/BoldItalic), mono = Go Mono (Regular only —
// bold/italic fall back to it, as the family ships no other styles). It panics
// only if a bundled font fails to parse, which would be a build-time defect in
// the fonts module, not a runtime condition.
func NewFonts() *Fonts {
	f := &Fonts{fonts: map[styleKey]*opentype.Font{}, faces: map[faceKey]*opentype.Face{}}
	set := func(fam css.FontFamily, reg, bold, italic, boldItalic []byte) {
		f.fonts[styleKey{fam, false, false}] = mustParseFont(reg)
		f.fonts[styleKey{fam, true, false}] = mustParseFont(bold)
		f.fonts[styleKey{fam, false, true}] = mustParseFont(italic)
		f.fonts[styleKey{fam, true, true}] = mustParseFont(boldItalic)
	}
	set(css.Sans, inter.TTF, inter.BoldTTF, inter.ItalicTTF, inter.BoldItalicTTF)
	set(css.Serif, lora.TTF, lora.BoldTTF, lora.ItalicTTF, lora.BoldItalicTTF)
	// Go Mono ships a single upright regular; its bold/italic requests fall back
	// to it through font() (the family ships no other styles).
	f.fonts[styleKey{css.Mono, false, false}] = mustParseFont(gomono.TTF)
	return f
}

// mustParseFont parses a bundled font, panicking on failure — which would be a
// build-time defect in the fonts module, not a runtime condition.
func mustParseFont(b []byte) *opentype.Font {
	f, err := opentype.Parse(b)
	if err != nil {
		panic("paint: bundled font failed to parse: " + err.Error())
	}
	return f
}

// font resolves the parsed font for a (family, bold, italic) request, falling
// back to the family's regular style and finally to the sans regular so a lookup
// always yields a usable face.
func (f *Fonts) font(k styleKey) *opentype.Font {
	if ft := f.fonts[k]; ft != nil {
		return ft
	}
	if ft := f.fonts[styleKey{k.fam, false, false}]; ft != nil {
		return ft
	}
	return f.fonts[styleKey{css.Sans, false, false}]
}

// face returns a cached Face for a family + style at an integer pixel size (>=1).
func (f *Fonts) face(fam css.FontFamily, sizePx float64, bold, italic bool) *opentype.Face {
	size := int(sizePx + 0.5)
	if size < 1 {
		size = 1
	}
	key := faceKey{styleKey{fam, bold, italic}, size}
	if fc, ok := f.faces[key]; ok {
		return fc
	}
	fc := f.font(key.style).NewFace(size)
	f.faces[key] = fc
	return fc
}

// styleFace maps a CSS weight/italic to the concrete face (bold at weight >=600).
func (f *Fonts) styleFace(fam css.FontFamily, sizePx float64, weight int, italic bool) *opentype.Face {
	return f.face(fam, sizePx, weight >= 600, italic)
}

// Measure implements layout.Measurer: the advance width of text in the resolved
// face. Bold and italic are real bundled faces, so no faux widening is applied.
func (f *Fonts) Measure(text string, fam css.FontFamily, sizePx float64, weight int, italic bool) float64 {
	return float64(f.styleFace(fam, sizePx, weight, italic).Measure(text))
}

// Metrics implements layout.Measurer: ascent and line height.
func (f *Fonts) Metrics(fam css.FontFamily, sizePx float64, weight int, italic bool) (ascent, lineHeight float64) {
	m := f.styleFace(fam, sizePx, weight, italic).Metrics()
	lh := float64(m.Height)
	if min := 1.15 * sizePx; lh < min {
		lh = min
	}
	return float64(m.Ascent), lh
}
