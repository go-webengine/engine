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

// Fonts is a registry of parsed font families with a per-size Face cache. It is
// not safe for concurrent use (Faces cache glyphs); build one per render.
type Fonts struct {
	fonts map[css.FontFamily]*opentype.Font
	faces map[faceKey]*opentype.Face
}

type faceKey struct {
	fam  css.FontFamily
	size int
}

// NewFonts parses the three bundled families (sans=Inter, serif=Lora,
// mono=Go Mono). It panics only if a bundled font fails to parse, which would
// be a build-time defect in the fonts module, not a runtime condition.
func NewFonts() *Fonts {
	return &Fonts{
		fonts: map[css.FontFamily]*opentype.Font{
			css.Sans:  mustParseFont(inter.TTF),
			css.Serif: mustParseFont(lora.TTF),
			css.Mono:  mustParseFont(gomono.TTF),
		},
		faces: map[faceKey]*opentype.Face{},
	}
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

// face returns a cached Face for a family at an integer pixel size (>=1).
func (f *Fonts) face(fam css.FontFamily, sizePx float64) *opentype.Face {
	size := int(sizePx + 0.5)
	if size < 1 {
		size = 1
	}
	key := faceKey{fam, size}
	if fc, ok := f.faces[key]; ok {
		return fc
	}
	font := f.fonts[fam]
	if font == nil {
		font = f.fonts[css.Sans]
	}
	fc := font.NewFace(size)
	f.faces[key] = fc
	return fc
}

// Measure implements layout.Measurer: the advance width of text.
func (f *Fonts) Measure(text string, fam css.FontFamily, sizePx float64, weight int) float64 {
	fc := f.face(fam, sizePx)
	w := fc.Measure(text)
	if weight >= 600 {
		// Faux-bold widens each rune by roughly one device pixel.
		w += len([]rune(text))
	}
	return float64(w)
}

// Metrics implements layout.Measurer: ascent and line height.
func (f *Fonts) Metrics(fam css.FontFamily, sizePx float64, weight int) (ascent, lineHeight float64) {
	m := f.face(fam, sizePx).Metrics()
	lh := float64(m.Height)
	if min := 1.15 * sizePx; lh < min {
		lh = min
	}
	return float64(m.Ascent), lh
}
