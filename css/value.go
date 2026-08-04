// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

// Package css implements a deliberately small but real subset of CSS: a value
// model, a tokenizer/parser for stylesheets and declaration blocks, tag/class/
// id selectors with specificity, and a cascade with inheritance over a dom
// tree. It targets the handful of properties Phase 0 needs — display, color,
// background-color, font-size, font-weight, font-family, margin, padding,
// width, text-align — plus a user-agent default stylesheet for common tags.
package css

import (
	"strconv"
	"strings"
)

// Color is an 8-bit-per-channel RGBA colour. A==0 is treated as transparent.
type Color struct{ R, G, B, A uint8 }

// Transparent is the fully-transparent colour (the initial background-color).
var Transparent = Color{}

// Display is the subset of the display property Phase 0 understands.
type Display uint8

const (
	// DisplayInline is the default for unknown/inline elements.
	DisplayInline Display = iota
	// DisplayBlock stacks the box vertically in block flow.
	DisplayBlock
	// DisplayNone removes the element (and subtree) from layout.
	DisplayNone
)

// FontFamily is the generic font family a run is rendered with.
type FontFamily uint8

const (
	// Sans is the default sans-serif family.
	Sans FontFamily = iota
	// Serif is the serif family.
	Serif
	// Mono is the monospace family.
	Mono
)

// WhiteSpace controls collapsing of whitespace and wrapping.
type WhiteSpace uint8

const (
	// WSNormal collapses runs of whitespace and wraps at the block width.
	WSNormal WhiteSpace = iota
	// WSPre preserves spaces and newlines and does not wrap (as in <pre>).
	WSPre
)

// TextAlign is the horizontal alignment of inline content in a block.
type TextAlign uint8

const (
	// AlignLeft is the initial value.
	AlignLeft TextAlign = iota
	// AlignCenter centres each line.
	AlignCenter
	// AlignRight right-aligns each line.
	AlignRight
)

// Edges holds the four sides of a margin or padding box, in pixels.
type Edges struct{ Top, Right, Bottom, Left float64 }

// Length is a resolved CSS length. Percentages are kept separately so the
// layout can resolve them against the containing block's width.
type Length struct {
	Px        float64
	Percent   float64 // 0..1; only meaningful when IsPercent
	IsPercent bool
	Auto      bool
}

// Resolve returns the length in pixels against a containing-block width.
func (l Length) Resolve(containing float64) float64 {
	if l.IsPercent {
		return l.Percent * containing
	}
	return l.Px
}

// Style is the fully-computed style of an element, after cascade + inheritance.
type Style struct {
	Display    Display
	Color      Color
	Background Color
	FontSize   float64 // px
	FontWeight int     // 400 = normal, 700 = bold
	FontFamily FontFamily
	Margin     Edges
	Padding    Edges
	Width      Length // Auto by default
	TextAlign  TextAlign
	WhiteSpace WhiteSpace
}

// Bold reports whether the weight renders as bold.
func (s *Style) Bold() bool { return s.FontWeight >= 600 }

// initialStyle is the root's starting style (CSS initial values for the
// inherited properties, block display for the viewport root).
func initialStyle() Style {
	return Style{
		Display:    DisplayBlock,
		Color:      Color{0, 0, 0, 255},
		Background: Transparent,
		FontSize:   16,
		FontWeight: 400,
		FontFamily: Serif, // UA default document font is serif
		Width:      Length{Auto: true},
		TextAlign:  AlignLeft,
	}
}

// inheritFrom returns a fresh style whose inherited properties are copied from
// the parent and whose non-inherited properties are reset to their initial
// values. The inherited set (per CSS): color, font-size, font-weight,
// font-family, text-align.
func inheritFrom(parent Style) Style {
	return Style{
		Display:    DisplayInline, // reset (non-inherited)
		Color:      parent.Color,
		Background: Transparent, // reset
		FontSize:   parent.FontSize,
		FontWeight: parent.FontWeight,
		FontFamily: parent.FontFamily,
		Width:      Length{Auto: true}, // reset
		TextAlign:  parent.TextAlign,
		WhiteSpace: parent.WhiteSpace, // white-space is inherited
	}
}

// parseColor parses a colour value: named colours, #rgb, #rrggbb, rgb()/rgba().
// It returns the colour and whether parsing succeeded.
func parseColor(s string) (Color, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "transparent" {
		return Transparent, true
	}
	if c, ok := namedColors[s]; ok {
		return c, true
	}
	if strings.HasPrefix(s, "#") {
		return parseHexColor(s[1:])
	}
	if strings.HasPrefix(s, "rgb") {
		return parseRGBFunc(s)
	}
	return Color{}, false
}

func parseHexColor(h string) (Color, bool) {
	switch len(h) {
	case 3: // #rgb
		r, ok1 := hexNibblePair(h[0], h[0])
		g, ok2 := hexNibblePair(h[1], h[1])
		b, ok3 := hexNibblePair(h[2], h[2])
		if ok1 && ok2 && ok3 {
			return Color{r, g, b, 255}, true
		}
	case 6: // #rrggbb
		r, ok1 := hexNibblePair(h[0], h[1])
		g, ok2 := hexNibblePair(h[2], h[3])
		b, ok3 := hexNibblePair(h[4], h[5])
		if ok1 && ok2 && ok3 {
			return Color{r, g, b, 255}, true
		}
	}
	return Color{}, false
}

func hexNibblePair(hi, lo byte) (uint8, bool) {
	h, ok1 := hexNibble(hi)
	l, ok2 := hexNibble(lo)
	if !ok1 || !ok2 {
		return 0, false
	}
	return h<<4 | l, true
}

func hexNibble(c byte) (uint8, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

func parseRGBFunc(s string) (Color, bool) {
	open := strings.IndexByte(s, '(')
	close := strings.IndexByte(s, ')')
	if open < 0 || close < open {
		return Color{}, false
	}
	parts := strings.Split(s[open+1:close], ",")
	if len(parts) != 3 && len(parts) != 4 {
		return Color{}, false
	}
	chan8 := func(p string) (uint8, bool) {
		f, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			return 0, false
		}
		return clampByte(f), true
	}
	r, ok1 := chan8(parts[0])
	g, ok2 := chan8(parts[1])
	b, ok3 := chan8(parts[2])
	if !ok1 || !ok2 || !ok3 {
		return Color{}, false
	}
	a := uint8(255)
	if len(parts) == 4 {
		f, err := strconv.ParseFloat(strings.TrimSpace(parts[3]), 64)
		if err != nil {
			return Color{}, false
		}
		a = clampByte(f * 255)
	}
	return Color{r, g, b, a}, true
}

func clampByte(f float64) uint8 {
	if f < 0 {
		return 0
	}
	if f > 255 {
		return 255
	}
	return uint8(f + 0.5)
}

// parseLength parses a length value against a reference font-size (for em).
// It understands px, em, %, the keyword auto, and a bare 0.
func parseLength(s string, emRef float64) (Length, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "auto" {
		return Length{Auto: true}, true
	}
	if s == "0" {
		return Length{Px: 0}, true
	}
	switch {
	case strings.HasSuffix(s, "px"):
		f, err := strconv.ParseFloat(strings.TrimSpace(s[:len(s)-2]), 64)
		if err != nil {
			return Length{}, false
		}
		return Length{Px: f}, true
	case strings.HasSuffix(s, "em"):
		f, err := strconv.ParseFloat(strings.TrimSpace(s[:len(s)-2]), 64)
		if err != nil {
			return Length{}, false
		}
		return Length{Px: f * emRef}, true
	case strings.HasSuffix(s, "%"):
		f, err := strconv.ParseFloat(strings.TrimSpace(s[:len(s)-1]), 64)
		if err != nil {
			return Length{}, false
		}
		return Length{Percent: f / 100, IsPercent: true}, true
	}
	return Length{}, false
}
