// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

// Package paint rasterises a laid-out box tree onto an *image.RGBA using
// go-opentype for anti-aliased text, go-widgets/painter for backgrounds and
// go-gfx-decoded bitmaps for <img>. It also provides the Measurer the
// layout package needs (advances + vertical metrics from real font faces).
package paint

import (
	"strings"

	"github.com/go-opentype/fonts/dejavusans"
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

	// fallback is the last-resort family — DejaVu Sans, for its coverage
	// (arrows, enclosed alphanumerics, mathematical operators, box drawing,
	// Greek, Cyrillic, …) rather than its looks — set per character the
	// family's own face has no glyph for; see Runs. A browser falls back
	// per character to the system's fonts; a self-contained engine bundles
	// its equivalent. CJK is beyond it: a character neither face covers
	// stays with the family and draws as nothing, as before.
	fallback      map[faceStyle]*opentype.Font
	fallbackFaces map[fallbackKey]*opentype.Face
}

// faceStyle is a weight/slant pair, the part of a styleKey a fallback
// face still varies by.
type faceStyle struct{ bold, italic bool }

type fallbackKey struct {
	faceStyle
	size int
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
	f := &Fonts{fonts: map[styleKey]*opentype.Font{}, faces: map[faceKey]*opentype.Face{},
		fallback: map[faceStyle]*opentype.Font{}, fallbackFaces: map[fallbackKey]*opentype.Face{}}
	f.fallback[faceStyle{false, false}] = mustParseFont(dejavusans.TTF)
	f.fallback[faceStyle{true, false}] = mustParseFont(dejavusans.BoldTTF)
	f.fallback[faceStyle{false, true}] = mustParseFont(dejavusans.ItalicTTF)
	f.fallback[faceStyle{true, true}] = mustParseFont(dejavusans.BoldItalicTTF)
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

// Measure returns the advance width of text in CSS px at the exact size
// asked: the glyphs' advances in font units scaled to sizePx, unrounded,
// for the family's face and for the fallback face on the runs that need
// it (see Runs). The raster painter still draws with a face at the nearest
// whole-pixel size and whole-pixel advances, so its glyphs sit on the pixel
// grid; layout, though, measures the truth — a consumer that draws at the
// true size (a PDF) then finds the words where the layout put them. Before
// this, a 14.4 px bold word was measured with a 14 px face, 2.8 % short,
// and printed at 14.4 px it ran into the space after it.
func (f *Fonts) Measure(text string, fam css.FontFamily, sizePx float64, weight int, italic bool) float64 {
	w := 0.0
	for _, run := range f.Runs(text, fam, weight, italic) {
		font := f.font(styleKey{fam, weight >= 600, italic})
		if run.Fallback {
			font = f.fallbackFont(weight >= 600, italic)
		}
		upem := float64(font.UnitsPerEm())
		for _, r := range run.Text {
			gid, ok := font.GlyphIndex(r)
			if !ok {
				continue
			}
			w += float64(font.GlyphAdvance(gid)) * sizePx / upem
		}
	}
	return w
}

// Run is a maximal stretch of a text set in one face: the family's own, or
// — Fallback — the last-resort family's, because the family has no glyph
// for those characters.
type Run struct {
	Text     string
	Fallback bool
}

// Runs splits s into the runs the family's face and the fallback face set
// between them: a character goes to the fallback exactly when the family
// has no glyph for it and the fallback has; one neither covers stays with
// the family. A space or other whitespace joins whichever run it is in,
// so "① ②" is one fallback run rather than three. Measure and the painter
// walk these runs, and a consumer that sets the same text elsewhere (a PDF
// exporter embedding both fonts) must walk them too, so its glyphs come
// from the faces the layout measured with.
func (f *Fonts) Runs(s string, fam css.FontFamily, weight int, italic bool) []Run {
	prim := f.font(styleKey{fam, weight >= 600, italic})
	fb := f.fallbackFont(weight >= 600, italic)
	var runs []Run
	var b strings.Builder
	cur := -1 // 0 family, 1 fallback, -1 none yet
	flush := func() {
		if b.Len() > 0 {
			runs = append(runs, Run{Text: b.String(), Fallback: cur == 1})
			b.Reset()
		}
	}
	for _, r := range s {
		use := 0
		switch {
		case r == ' ' || r == '\t' || r == '\n' || r == '\u00a0':
			use = cur // whitespace never starts a run of its own
			if use < 0 {
				use = 0
			}
		default:
			if _, ok := prim.GlyphIndex(r); !ok {
				if _, ok := fb.GlyphIndex(r); ok {
					use = 1
				}
			}
		}
		if use != cur {
			flush()
			cur = use
		}
		b.WriteRune(r)
	}
	flush()
	return runs
}

// fallbackFont returns the last-resort font for a weight/slant.
func (f *Fonts) fallbackFont(bold, italic bool) *opentype.Font {
	return f.fallback[faceStyle{bold, italic}]
}

// fallbackFace returns the last-resort face at sizePx, cached.
func (f *Fonts) fallbackFace(sizePx float64, bold, italic bool) *opentype.Face {
	size := int(sizePx + 0.5)
	if size < 1 {
		size = 1
	}
	key := fallbackKey{faceStyle{bold, italic}, size}
	if fc, ok := f.fallbackFaces[key]; ok {
		return fc
	}
	fc := f.fallbackFont(bold, italic).NewFace(size)
	f.fallbackFaces[key] = fc
	return fc
}

// runFace is the face a run is set in.
func (f *Fonts) runFace(run Run, fam css.FontFamily, sizePx float64, weight int, italic bool) *opentype.Face {
	if run.Fallback {
		return f.fallbackFace(sizePx, weight >= 600, italic)
	}
	return f.styleFace(fam, sizePx, weight, italic)
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
