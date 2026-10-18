// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package css

import (
	"math"
	"strconv"
	"strings"

	gfxcolor "github.com/go-gfx/gfx/color"
)

// mixSpace is a colour interpolation space color-mix() can mix in.
type mixSpace uint8

const (
	mixSRGB mixSpace = iota
	mixSRGBLinear
	mixOKLab
	mixOKLCH
	mixHSL
)

// hueMethod is a <hue-interpolation-method> (CSS Color 4 §12.4).
type hueMethod uint8

const (
	hueShorter hueMethod = iota
	hueLonger
	hueIncreasing
	hueDecreasing
)

// mixColor is one colour expressed in a mixSpace: c holds the three
// components in the space's own order (hue LAST for the polar spaces: oklch
// is L C H, hsl is S L H), a the alpha in 0..1, and hueMissing marks an
// achromatic colour whose hue is powerless (CSS Color 4 §4.4), which takes
// the other colour's hue when interpolating.
type mixColor struct {
	c          [3]float64
	a          float64
	hueMissing bool
}

// achromaticEps is the chroma (oklch) / saturation (hsl) below which a hue
// is treated as powerless: a grey converted from 8-bit sRGB lands a few
// 1e-8 off zero, never exactly on it.
const achromaticEps = 1e-4

func (s mixSpace) polar() bool { return s == mixOKLCH || s == mixHSL }

// toMix converts an 8-bit sRGB colour into space s.
func toMix(col Color, s mixSpace) mixColor {
	r, g, b := float64(col.R)/255, float64(col.G)/255, float64(col.B)/255
	m := mixColor{a: float64(col.A) / 255}
	switch s {
	case mixSRGBLinear:
		m.c = [3]float64{gfxcolor.SRGBToLinear(r), gfxcolor.SRGBToLinear(g), gfxcolor.SRGBToLinear(b)}
	case mixOKLab:
		o := gfxcolor.SRGBToOKLab(r, g, b)
		m.c = [3]float64{o.L, o.A, o.B}
	case mixOKLCH:
		o := gfxcolor.SRGBToOKLCH(r, g, b)
		m.c = [3]float64{o.L, o.C, o.H}
		m.hueMissing = o.C < achromaticEps
	case mixHSL:
		h := gfxcolor.SRGBToHSL(r, g, b)
		m.c = [3]float64{h.S, h.L, h.H}
		m.hueMissing = h.S < achromaticEps
	default: // mixSRGB
		m.c = [3]float64{r, g, b}
	}
	return m
}

// fromMix converts a colour in space s back to 8-bit sRGB, clamping to gamut.
func fromMix(m mixColor, s mixSpace) Color {
	var r, g, b float64
	switch s {
	case mixSRGBLinear:
		r, g, b = gfxcolor.LinearToSRGB(m.c[0]), gfxcolor.LinearToSRGB(m.c[1]), gfxcolor.LinearToSRGB(m.c[2])
	case mixOKLab:
		r, g, b = gfxcolor.OKLabToSRGB(gfxcolor.OKLab{L: m.c[0], A: m.c[1], B: m.c[2]})
	case mixOKLCH:
		r, g, b = gfxcolor.OKLCHToSRGB(gfxcolor.OKLCH{L: m.c[0], C: m.c[1], H: m.c[2]})
	case mixHSL:
		r, g, b = gfxcolor.HSLToSRGB(gfxcolor.HSL{H: m.c[2], S: m.c[0], L: m.c[1]})
	default:
		r, g, b = m.c[0], m.c[1], m.c[2]
	}
	return Color{clampByte(r * 255), clampByte(g * 255), clampByte(b * 255), clampByte(m.a * 255)}
}

// interpolateMix interpolates from x to y at progress p (0 → x, 1 → y) in
// space s, in premultiplied alpha (CSS Color 4 §12.3: every component but
// the hue is premultiplied, interpolated, then divided by the interpolated
// alpha), with polar hues interpolated per hm (§12.4).
func interpolateMix(x, y mixColor, p float64, s mixSpace, hm hueMethod) mixColor {
	out := mixColor{a: x.a + (y.a-x.a)*p}
	n := 3
	if s.polar() {
		n = 2
		h1, h2 := x.c[2], y.c[2]
		switch {
		case x.hueMissing && y.hueMissing:
			h1, h2 = 0, 0
			out.hueMissing = true
		case x.hueMissing:
			h1 = h2
		case y.hueMissing:
			h2 = h1
		}
		h1, h2 = fixupHues(h1, h2, hm)
		out.c[2] = gfxcolor.NormalizeHue(h1 + (h2-h1)*p)
	}
	for i := 0; i < n; i++ {
		v := x.c[i]*x.a + (y.c[i]*y.a-x.c[i]*x.a)*p
		if out.a > 0 {
			v /= out.a
		}
		out.c[i] = v
	}
	return out
}

// fixupHues adjusts two hue angles (degrees) so that linear interpolation
// between them follows the requested <hue-interpolation-method> (CSS Color 4
// §12.4.1–12.4.4).
func fixupHues(h1, h2 float64, hm hueMethod) (float64, float64) {
	h1, h2 = gfxcolor.NormalizeHue(h1), gfxcolor.NormalizeHue(h2)
	d := h2 - h1
	switch hm {
	case hueLonger:
		if d > 0 && d < 180 {
			h1 += 360
		} else if d > -180 && d <= 0 {
			h2 += 360
		}
	case hueIncreasing:
		if d < 0 {
			h2 += 360
		}
	case hueDecreasing:
		if d > 0 {
			h1 += 360
		}
	default: // shorter
		if d > 180 {
			h1 += 360
		} else if d < -180 {
			h2 += 360
		}
	}
	return h1, h2
}

// parseMixMethod parses a <color-interpolation-method> ("in <space>
// [<hue-method> hue]"), reporting false for a space this engine cannot mix in.
func parseMixMethod(v string) (mixSpace, hueMethod, bool) {
	f := strings.Fields(v)
	if len(f) < 2 || f[0] != "in" {
		return 0, 0, false
	}
	var s mixSpace
	switch f[1] {
	case "srgb":
		s = mixSRGB
	case "srgb-linear":
		s = mixSRGBLinear
	case "oklab":
		s = mixOKLab
	case "oklch":
		s = mixOKLCH
	case "hsl":
		s = mixHSL
	default:
		return 0, 0, false
	}
	switch len(f) {
	case 2:
		return s, hueShorter, true
	case 4:
		if !s.polar() || f[3] != "hue" {
			return 0, 0, false
		}
		switch f[2] {
		case "shorter":
			return s, hueShorter, true
		case "longer":
			return s, hueLonger, true
		case "increasing":
			return s, hueIncreasing, true
		case "decreasing":
			return s, hueDecreasing, true
		}
	}
	return 0, 0, false
}

// mixItem is one `<color> && <percentage [0,100]>?` argument.
type mixItem struct {
	col    mixColor
	pct    float64
	hasPct bool
}

// parseColorMix parses a color-mix() function (CSS Color 5 §3): an optional
// <color-interpolation-method> (Oklab when omitted, §3.1) and one or more
// comma-separated colours, each with an optional percentage. s is lower-case
// and trimmed. The supported spaces are srgb, srgb-linear, oklab, oklch and
// hsl; any other space, a percentage outside 0..100 or a malformed argument
// makes the whole value invalid.
//
// The result follows §3.3: the percentages are normalised per CSS Values 5
// §6.1 "normalize mix percentages" with forced normalisation, any leftover
// below 100% becomes an alpha multiplier, and the items are interpolated
// pairwise, each next item at progress (its percentage) / (combined
// percentage so far), or 0.5 when that combined percentage is 0.
func parseColorMix(s string) (Color, bool) {
	const name = "color-mix"
	if !strings.HasPrefix(s, name+"(") {
		return Color{}, false
	}
	closeIdx, ok := matchParen(s, len(name))
	if !ok || closeIdx != len(s)-1 {
		return Color{}, false
	}
	args := splitTopLevelComma(s[len(name)+1 : closeIdx])
	space, hm := mixOKLab, hueShorter
	if strings.HasPrefix(args[0], "in ") {
		sp, h, ok := parseMixMethod(args[0])
		if !ok {
			return Color{}, false
		}
		space, hm = sp, h
		args = args[1:]
	}
	if len(args) == 0 {
		return Color{}, false
	}
	items := make([]mixItem, 0, len(args))
	for _, a := range args {
		it, ok := parseMixItem(a, space)
		if !ok {
			return Color{}, false
		}
		items = append(items, it)
	}
	leftover := normalizeMixPercentages(items)

	acc := items[0].col
	accPct := items[0].pct
	for _, it := range items[1:] {
		combined := accPct + it.pct
		p := 0.5
		if combined > 0 {
			p = it.pct / combined
		}
		acc = interpolateMix(acc, it.col, p, space, hm)
		accPct = combined
	}
	acc.a *= 1 - leftover/100
	return fromMix(acc, space), true
}

// parseMixItem parses one color-mix() argument: a colour and an optional
// percentage, in either order.
func parseMixItem(a string, space mixSpace) (mixItem, bool) {
	toks := splitTopLevel(a)
	if len(toks) == 0 || len(toks) > 2 {
		return mixItem{}, false
	}
	var it mixItem
	haveColor := false
	for _, tok := range toks {
		if strings.HasSuffix(tok, "%") && !it.hasPct {
			f, err := strconv.ParseFloat(tok[:len(tok)-1], 64)
			if err != nil || f < 0 || f > 100 || math.IsNaN(f) {
				return mixItem{}, false
			}
			it.pct, it.hasPct = f, true
			continue
		}
		if haveColor {
			return mixItem{}, false
		}
		c, ok := parseColor(tok)
		if !ok {
			return mixItem{}, false
		}
		it.col, haveColor = toMix(c, space), true
	}
	return it, haveColor
}

// normalizeMixPercentages implements CSS Values 5 §6.1 "normalize mix
// percentages" with the force normalization flag set (as color-mix() §3.3
// step 1 calls it), updating items in place and returning the leftover
// percentage.
func normalizeMixPercentages(items []mixItem) float64 {
	var specified float64
	omitted := 0
	for _, it := range items {
		if it.hasPct {
			specified += it.pct
		} else {
			omitted++
		}
	}
	specified = math.Min(specified, 100)
	for i := range items {
		if !items[i].hasPct {
			items[i].pct = (100 - specified) / float64(omitted)
		}
	}
	var total float64
	for _, it := range items {
		total += it.pct
	}
	if total > 0 {
		for i := range items {
			items[i].pct *= 100 / total
		}
	}
	if total < 100 {
		return 100 - total
	}
	return 0
}
