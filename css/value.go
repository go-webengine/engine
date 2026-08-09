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

// Display is the subset of the display property the engine understands.
type Display uint8

const (
	// DisplayInline is the default for unknown/inline elements.
	DisplayInline Display = iota
	// DisplayBlock stacks the box vertically in block flow.
	DisplayBlock
	// DisplayNone removes the element (and subtree) from layout.
	DisplayNone
	// DisplayInlineBlock is an atomic inline-level block (laid out as a block
	// but participating inline; Phase 1 treats it as block for simplicity).
	DisplayInlineBlock
	// DisplayFlex is a block-level flex container.
	DisplayFlex
	// DisplayTable is a block-level table box.
	DisplayTable
	// DisplayTableRow is a table row box.
	DisplayTableRow
	// DisplayTableCell is a table cell box.
	DisplayTableCell
	// DisplayTableRowGroup is a thead/tbody/tfoot grouping box (transparent to
	// the table's row collection).
	DisplayTableRowGroup
	// DisplayGrid is a block-level CSS grid container.
	DisplayGrid
)

// Position is the subset of the position property the engine understands.
type Position uint8

const (
	// PositionStatic is the initial value: the box is in normal flow with no
	// top/right/bottom/left offset applied.
	PositionStatic Position = iota
	// PositionRelative keeps the box in normal flow (it still reserves space) but
	// paints it shifted by its top/left/right/bottom offset.
	PositionRelative
	// PositionAbsolute removes the box from normal flow and positions it against
	// the padding box of the nearest positioned ancestor (else the initial
	// containing block).
	PositionAbsolute
	// PositionFixed removes the box from normal flow and positions it against the
	// initial containing block (the viewport). For a full-page static render it is
	// resolved to document coordinates so it paints once at its place.
	PositionFixed
	// PositionSticky is approximated as relative for a full-page static shot.
	PositionSticky
)

// OutOfFlow reports whether a position value takes the box out of normal flow
// (so it reserves no space in its parent's block/inline formatting context).
func (p Position) OutOfFlow() bool { return p == PositionAbsolute || p == PositionFixed }

// Positioned reports whether a position value makes the box a containing block
// for absolutely-positioned descendants (anything other than static).
func (p Position) Positioned() bool { return p != PositionStatic }

// Float is the subset of the float property the engine understands.
type Float uint8

const (
	// FloatNone is the initial value (no float).
	FloatNone Float = iota
	// FloatLeft floats the box to the left of its container.
	FloatLeft
	// FloatRight floats the box to the right of its container.
	FloatRight
)

// Clear is the subset of the clear property the engine understands.
type Clear uint8

const (
	// ClearNone is the initial value.
	ClearNone Clear = iota
	// ClearLeft clears past left floats.
	ClearLeft
	// ClearRight clears past right floats.
	ClearRight
	// ClearBoth clears past floats on both sides.
	ClearBoth
)

// BoxSizing selects whether width/height apply to the content box or the
// border box.
type BoxSizing uint8

const (
	// ContentBox is the initial value: width is the content width.
	ContentBox BoxSizing = iota
	// BorderBox: width includes padding and border.
	BorderBox
)

// Overflow is the computed overflow of one axis. For the engine's headless
// paint every non-visible value (hidden/clip/scroll/auto) clips descendant
// painting to the box's padding box — there is no interactive scrolling, so a
// scroll/auto container renders exactly its visible window, matching the first
// paint a user would see. This is what keeps the universal `sr-only` /
// visually-hidden pattern (position:absolute;width:1px;height:1px;
// overflow:hidden;clip) from painting its screen-reader text at full size.
type Overflow uint8

const (
	// OverflowVisible is the initial value: content is not clipped.
	OverflowVisible Overflow = iota
	OverflowHidden
	OverflowClip
	OverflowScroll
	OverflowAuto
)

// Clips reports whether this overflow value clips descendant painting.
func (o Overflow) Clips() bool { return o != OverflowVisible }

// parseOverflowKeyword maps a single overflow keyword to its value, reporting
// whether it was recognised.
func parseOverflowKeyword(s string) (Overflow, bool) {
	switch s {
	case "visible":
		return OverflowVisible, true
	case "hidden":
		return OverflowHidden, true
	case "clip":
		return OverflowClip, true
	case "scroll":
		return OverflowScroll, true
	case "auto":
		return OverflowAuto, true
	}
	return OverflowVisible, false
}

// BorderStyle is the subset of border-style the engine paints. Any non-none,
// non-hidden line style renders as a solid line (dashed/dotted/etc. collapse to
// solid at this fidelity).
type BorderStyle uint8

const (
	// BorderNone is the initial value: no border line (even if width > 0).
	BorderNone BorderStyle = iota
	// BorderSolid renders a solid line of the border colour.
	BorderSolid
)

// BorderSide is one edge's border: its width, line style and colour.
type BorderSide struct {
	Width float64
	Style BorderStyle
	Color Color
}

// paints reports whether the side draws a visible line.
func (b BorderSide) paints() bool { return b.Width > 0 && b.Style != BorderNone && b.Color.A > 0 }

// Borders is the four border edges of a box.
type Borders struct{ Top, Right, Bottom, Left BorderSide }

// Widths returns the four border widths as Edges (0 when the style is none, so
// layout only reserves space for painted borders — matching a border-style:none
// edge contributing no width even if border-width is set).
func (b Borders) Widths() Edges {
	w := func(s BorderSide) float64 {
		if s.Style == BorderNone {
			return 0
		}
		return s.Width
	}
	return Edges{Top: w(b.Top), Right: w(b.Right), Bottom: w(b.Bottom), Left: w(b.Left)}
}

// FlexDirection is the main-axis direction of a flex container.
type FlexDirection uint8

const (
	// FlexRow lays items along the inline (horizontal) axis.
	FlexRow FlexDirection = iota
	// FlexColumn lays items along the block (vertical) axis.
	FlexColumn
)

// Justify is the main-axis distribution (justify-content).
type Justify uint8

const (
	// JustifyStart packs items at the main-start edge (initial value).
	JustifyStart Justify = iota
	// JustifyEnd packs items at the main-end edge.
	JustifyEnd
	// JustifyCenter centres items on the main axis.
	JustifyCenter
	// JustifySpaceBetween distributes free space between items.
	JustifySpaceBetween
	// JustifySpaceAround distributes free space around items.
	JustifySpaceAround
	// JustifySpaceEvenly distributes free space evenly incl. the ends.
	JustifySpaceEvenly
)

// AlignItems is the cross-axis alignment (align-items). In a grid container it
// is also reused for the block-axis (align-items) and, via JustifyItems, the
// inline-axis (justify-items) alignment of items within their cells.
type AlignItems uint8

const (
	// AlignStretch stretches items to fill the cross axis (initial value).
	AlignStretch AlignItems = iota
	// AlignFlexStart aligns items at the cross-start edge.
	AlignFlexStart
	// AlignFlexEnd aligns items at the cross-end edge.
	AlignFlexEnd
	// AlignCenterItems centres items on the cross axis.
	AlignCenterItems
)

// FlexWrap controls whether flex items wrap onto multiple lines.
type FlexWrap uint8

const (
	// FlexNoWrap keeps all items on a single line (initial value).
	FlexNoWrap FlexWrap = iota
	// FlexWrapOn wraps items onto new lines toward the cross-end.
	FlexWrapOn
	// FlexWrapReverse wraps items with the cross axis reversed.
	FlexWrapReverse
)

// AlignContent distributes flex lines (or grid tracks) along the cross axis
// when there is spare cross-axis space (multi-line flex / align-content).
type AlignContent uint8

const (
	// AlignContentStretch stretches lines to fill the cross axis (initial).
	AlignContentStretch AlignContent = iota
	// AlignContentStart packs lines at the cross-start edge.
	AlignContentStart
	// AlignContentEnd packs lines at the cross-end edge.
	AlignContentEnd
	// AlignContentCenter centres the lines as a group.
	AlignContentCenter
	// AlignContentSpaceBetween spreads lines with the ends flush.
	AlignContentSpaceBetween
	// AlignContentSpaceAround spreads lines with half-gaps at the ends.
	AlignContentSpaceAround
	// AlignContentSpaceEvenly spreads lines with equal gaps incl. the ends.
	AlignContentSpaceEvenly
)

// AlignSelf overrides a single item's cross-axis alignment. Auto (the initial
// value) defers to the container's align-items.
type AlignSelf uint8

const (
	// AlignSelfAuto uses the container's align-items value (initial).
	AlignSelfAuto AlignSelf = iota
	// AlignSelfStretch stretches this item on the cross axis.
	AlignSelfStretch
	// AlignSelfStart aligns this item at the cross-start edge.
	AlignSelfStart
	// AlignSelfEnd aligns this item at the cross-end edge.
	AlignSelfEnd
	// AlignSelfCenter centres this item on the cross axis.
	AlignSelfCenter
)

// resolve returns the effective AlignItems for an item, falling back to the
// container's align-items when the item's align-self is auto.
func (a AlignSelf) Resolve(container AlignItems) AlignItems {
	switch a {
	case AlignSelfStretch:
		return AlignStretch
	case AlignSelfStart:
		return AlignFlexStart
	case AlignSelfEnd:
		return AlignFlexEnd
	case AlignSelfCenter:
		return AlignCenterItems
	default:
		return container
	}
}

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

// ListStyleType is the marker style of a display:list-item box. It inherits.
type ListStyleType uint8

const (
	// ListDisc is a filled circle (the initial value / <ul> default).
	ListDisc ListStyleType = iota
	// ListCircle is a hollow (stroked) circle.
	ListCircle
	// ListSquare is a filled square.
	ListSquare
	// ListDecimal is an ascending decimal number ("1.", "2.", …) — the <ol> default.
	ListDecimal
	// ListNone paints no marker.
	ListNone
)

// ListStylePosition is where the marker sits relative to the item's content. It
// inherits.
type ListStylePosition uint8

const (
	// ListOutside places the marker in the indent to the left of the content box
	// (the initial value).
	ListOutside ListStylePosition = iota
	// ListInside places the marker inside the content box, before the first line.
	ListInside
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
	// AlignCenterBlocks centres inline content like AlignCenter and additionally
	// centres a definite-width block/table child within its container — the legacy
	// behaviour of the <center> element and the align="center" attribute (browsers'
	// -moz-center / -webkit-center). A plain text-align:center author rule never
	// produces it, so standards-mode blocks are unaffected.
	AlignCenterBlocks
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
	Italic     bool // font-style: italic|oblique (inherited)
	Margin     Edges
	Padding    Edges
	Border     Borders
	Width      Length // Auto by default
	MinWidth   Length // Auto (== none) by default
	MaxWidth   Length // Auto (== none) by default
	Height     Length // Auto by default
	MinHeight  Length // Auto (== none) by default
	MaxHeight  Length // Auto (== none) by default
	BoxSizing  BoxSizing
	TextAlign  TextAlign
	WhiteSpace WhiteSpace
	LineHeight LineHeight

	// ListItem marks a display:list-item box (it generates a marker). It is not
	// inherited. ListStyleType and ListStylePosition are inherited and select the
	// marker glyph and its placement.
	ListItem          bool
	ListStyleType     ListStyleType
	ListStylePosition ListStylePosition

	// BorderRadius is the corner radius applied to the border box when painting
	// the background and border. It is a single (uniform) radius: the common real
	// case (Tailwind `rounded-*`, pills, circles) sets all four corners equal.
	// Differing per-corner radii are approximated by the last-applied value
	// (documented in FIDELITY.md). A px length is used as-is; a percentage
	// resolves against the box's smaller side at paint time. Zero (the initial
	// value) means square corners.
	BorderRadius Length

	// Auto-margin flags: a margin explicitly set to `auto` centres or pushes the
	// box; distinct from a 0 margin.
	MarginLeftAuto  bool
	MarginRightAuto bool

	// Overflow per axis (not inherited; initial value visible). Any non-visible
	// value clips descendant painting to this box's padding box.
	OverflowX Overflow
	OverflowY Overflow

	// Positioning that affects normal flow.
	Float Float
	Clear Clear

	// CSS position and the box-offset properties. Top/Right/Bottom/Left are Auto
	// by default (the initial value of each offset). ZIndex is meaningful only
	// when ZIndexAuto is false; the initial value is auto (paint in tree order).
	Position   Position
	Top        Length // Auto by default
	Right      Length // Auto by default
	Bottom     Length // Auto by default
	Left       Length // Auto by default
	ZIndex     int
	ZIndexAuto bool // true == "auto" (the initial value)

	// Flex container properties (meaningful when Display == DisplayFlex).
	FlexDirection  FlexDirection
	FlexWrap       FlexWrap
	JustifyContent Justify
	AlignItems     AlignItems
	AlignContent   AlignContent

	// Gaps between flex lines/items and between grid tracks. RowGap is the
	// cross-line / block-axis gap; ColumnGap is the main-axis / inline-axis gap.
	RowGap    Length
	ColumnGap Length

	// Flex/grid item properties (meaningful for a child of a flex/grid container).
	FlexGrow   float64
	FlexShrink float64
	FlexBasis  Length // Auto == "auto" (use the item's width/content)
	Order      int    // reorders items within a line (ascending)
	AlignSelf  AlignSelf

	// Grid container properties (meaningful when Display == DisplayGrid).
	GridTemplateColumns []TrackSize
	GridTemplateRows    []TrackSize
	GridAutoRows        TrackSize
	GridAutoColumns     TrackSize
	GridAutoFlow        GridFlow
	GridTemplateAreas   [][]string // row-major grid of area names ("" == empty)
	JustifyItems        AlignItems // inline-axis alignment of items in their cell

	// Grid item placement (meaningful for a child of a grid container).
	GridColumnStart GridLine
	GridColumnEnd   GridLine
	GridRowStart    GridLine
	GridRowEnd      GridLine
	GridArea        string // named area this item is placed into (via grid-area)
	JustifySelf     AlignSelf

	// CustomProps holds the element's resolved CSS custom properties (--name ->
	// raw value). It is inherited from the parent and overridden by matched
	// rules; var() references consult it at computed-value time. Nil until an
	// element (or an ancestor) defines a custom property.
	CustomProps map[string]string

	// Background image layers (gradients and url() bitmaps) and their paint
	// parameters. Each list is indexed per layer (first-listed paints on top);
	// a shorter size/position/repeat list repeats its last value. All nil == no
	// background image (only the solid Background colour paints).
	BackgroundImages   []BgImage
	BackgroundSize     []BgSize
	BackgroundPosition []BgPosition
	BackgroundRepeat   []BgRepeat

	// BoxShadows are the element's box-shadow layers (first-listed paints on top).
	BoxShadows []BoxShadow

	// Opacity is the element's group opacity in [0,1]; HasOpacity distinguishes a
	// genuine opacity from the zero value (an unset Style is fully opaque).
	Opacity    float64
	HasOpacity bool
}

// BoxShadow is one box-shadow layer.
type BoxShadow struct {
	OffsetX, OffsetY float64
	Blur, Spread     float64
	Color            Color
	Inset            bool
}

// LineHeight is a computed line-height. Normal means "use the font's own line
// height". Otherwise it is EITHER a fixed pixel height (Px, from a length or
// percentage value) OR a unitless Factor of the element's own font-size.
//
// The distinction is load-bearing for inheritance (CSS 2.1 §10.8.1): a length
// or percentage line-height computes to a fixed pixel value that descendants
// inherit unchanged, whereas a unitless number computes to the number itself
// and inherits AS the number, so each descendant re-multiplies by its OWN
// font-size. Collapsing a unitless line-height to pixels at the declaring
// element (as an earlier version did) leaks the ancestor's font-size onto
// larger/smaller descendants, making their line boxes too short and letting
// glyphs from adjacent lines overlap.
type LineHeight struct {
	Px     float64
	Factor float64 // unitless multiplier of the element's own font-size (0 = none)
	Normal bool
}

// Resolve returns the used line-height in pixels for an element whose computed
// font-size is fontSize, and whether an explicit height applies. It returns
// (0, false) for `normal` (defer to the font's natural metrics) and for a
// non-positive height. A unitless Factor is multiplied by fontSize; a fixed Px
// is returned unchanged.
func (h LineHeight) Resolve(fontSize float64) (float64, bool) {
	if h.Normal {
		return 0, false
	}
	if h.Factor > 0 {
		px := h.Factor * fontSize
		return px, px > 0
	}
	if h.Px <= 0 {
		return 0, false
	}
	return h.Px, true
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
		MinWidth:   Length{Auto: true},
		MaxWidth:   Length{Auto: true},
		Height:     Length{Auto: true},
		MinHeight:  Length{Auto: true},
		MaxHeight:  Length{Auto: true},
		FlexBasis:  Length{Auto: true},
		FlexShrink: 1, // CSS initial flex-shrink is 1
		TextAlign:  AlignLeft,
		LineHeight: LineHeight{Normal: true},

		Top:        Length{Auto: true},
		Right:      Length{Auto: true},
		Bottom:     Length{Auto: true},
		Left:       Length{Auto: true},
		ZIndexAuto: true,

		GridColumnStart: GridLine{Auto: true},
		GridColumnEnd:   GridLine{Auto: true},
		GridRowStart:    GridLine{Auto: true},
		GridRowEnd:      GridLine{Auto: true},
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
		Italic:     parent.Italic,      // inherited
		Width:      Length{Auto: true}, // reset
		MinWidth:   Length{Auto: true},
		MaxWidth:   Length{Auto: true},
		Height:     Length{Auto: true},
		MinHeight:  Length{Auto: true},
		MaxHeight:  Length{Auto: true},
		FlexBasis:  Length{Auto: true},
		FlexShrink: 1,
		TextAlign:  parent.TextAlign,  // inherited
		WhiteSpace: parent.WhiteSpace, // inherited
		LineHeight: parent.LineHeight, // inherited
		// list-style-type / list-style-position inherit; list-item does not.
		ListStyleType:     parent.ListStyleType,
		ListStylePosition: parent.ListStylePosition,
		CustomProps:       parent.CustomProps, // inherited (shared until copy-on-write)

		// position and the offsets are not inherited: reset to static / auto.
		Top:        Length{Auto: true},
		Right:      Length{Auto: true},
		Bottom:     Length{Auto: true},
		Left:       Length{Auto: true},
		ZIndexAuto: true,

		GridColumnStart: GridLine{Auto: true},
		GridColumnEnd:   GridLine{Auto: true},
		GridRowStart:    GridLine{Auto: true},
		GridRowEnd:      GridLine{Auto: true},
	}
}

// parseColor parses a colour value: named colours, #rgb / #rgba / #rrggbb /
// #rrggbbaa, and the rgb()/rgba()/hsl()/hsla() functional notations in BOTH the
// legacy comma syntax and the modern space-separated `R G B / A` syntax (the
// form modern Tailwind and most current design systems emit). It returns the
// colour and whether parsing succeeded.
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
	if strings.HasPrefix(s, "rgb") || strings.HasPrefix(s, "hsl") {
		return parseColorFunc(s)
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
	case 4: // #rgba
		r, ok1 := hexNibblePair(h[0], h[0])
		g, ok2 := hexNibblePair(h[1], h[1])
		b, ok3 := hexNibblePair(h[2], h[2])
		a, ok4 := hexNibblePair(h[3], h[3])
		if ok1 && ok2 && ok3 && ok4 {
			return Color{r, g, b, a}, true
		}
	case 6: // #rrggbb
		r, ok1 := hexNibblePair(h[0], h[1])
		g, ok2 := hexNibblePair(h[2], h[3])
		b, ok3 := hexNibblePair(h[4], h[5])
		if ok1 && ok2 && ok3 {
			return Color{r, g, b, 255}, true
		}
	case 8: // #rrggbbaa
		r, ok1 := hexNibblePair(h[0], h[1])
		g, ok2 := hexNibblePair(h[2], h[3])
		b, ok3 := hexNibblePair(h[4], h[5])
		a, ok4 := hexNibblePair(h[6], h[7])
		if ok1 && ok2 && ok3 && ok4 {
			return Color{r, g, b, a}, true
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

// parseColorFunc parses rgb()/rgba()/hsl()/hsla() in either the legacy
// comma-separated syntax (`rgb(1, 2, 3, .5)`) or the modern space-separated
// syntax with an optional slash alpha (`rgb(1 2 3 / .5)`). Channel values may be
// numbers or percentages; the alpha may be a number 0..1 or a percentage.
func parseColorFunc(s string) (Color, bool) {
	isHSL := strings.HasPrefix(s, "hsl")
	open := strings.IndexByte(s, '(')
	close := strings.LastIndexByte(s, ')')
	if open < 0 || close < open {
		return Color{}, false
	}
	inside := s[open+1 : close]
	// Separate an explicit slash alpha (modern syntax) from the channels.
	alphaStr := ""
	haveAlpha := false
	if i := strings.IndexByte(inside, '/'); i >= 0 {
		alphaStr = strings.TrimSpace(inside[i+1:])
		inside = inside[:i]
		haveAlpha = true
	}
	// Channels are separated by commas and/or whitespace (both are accepted).
	fields := strings.FieldsFunc(inside, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	if len(fields) < 3 {
		return Color{}, false
	}
	// A legacy 4th comma field is the alpha when no slash form was used.
	if !haveAlpha && len(fields) >= 4 {
		alphaStr, haveAlpha = fields[3], true
	}

	a := uint8(255)
	if haveAlpha {
		av, ok := parseAlpha(alphaStr)
		if !ok {
			return Color{}, false
		}
		a = av
	}
	if isHSL {
		return parseHSLChannels(fields[0], fields[1], fields[2], a)
	}
	r, ok1 := parseRGBChannel(fields[0])
	g, ok2 := parseRGBChannel(fields[1])
	b, ok3 := parseRGBChannel(fields[2])
	if !ok1 || !ok2 || !ok3 {
		return Color{}, false
	}
	return Color{r, g, b, a}, true
}

// parseRGBChannel parses one rgb() channel: a 0..255 number or a percentage.
func parseRGBChannel(p string) (uint8, bool) {
	p = strings.TrimSpace(p)
	if strings.HasSuffix(p, "%") {
		f, err := strconv.ParseFloat(strings.TrimSpace(p[:len(p)-1]), 64)
		if err != nil {
			return 0, false
		}
		return clampByte(f / 100 * 255), true
	}
	f, err := strconv.ParseFloat(p, 64)
	if err != nil {
		return 0, false
	}
	return clampByte(f), true
}

// parseAlpha parses an alpha value: a number in 0..1 or a percentage.
func parseAlpha(p string) (uint8, bool) {
	p = strings.TrimSpace(p)
	if strings.HasSuffix(p, "%") {
		f, err := strconv.ParseFloat(strings.TrimSpace(p[:len(p)-1]), 64)
		if err != nil {
			return 0, false
		}
		return clampByte(f / 100 * 255), true
	}
	f, err := strconv.ParseFloat(p, 64)
	if err != nil {
		return 0, false
	}
	return clampByte(f * 255), true
}

// parseHSLChannels converts hue/saturation/lightness (h in degrees, s and l as
// percentages) plus an 8-bit alpha into an RGBA colour.
func parseHSLChannels(hs, ss, ls string, a uint8) (Color, bool) {
	h, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(hs, "deg")), 64)
	if err != nil {
		return Color{}, false
	}
	sPerc, ok1 := parsePercentUnit(ss)
	lPerc, ok2 := parsePercentUnit(ls)
	if !ok1 || !ok2 {
		return Color{}, false
	}
	r, g, b := hslToRGB(h, sPerc, lPerc)
	return Color{r, g, b, a}, true
}

// parsePercentUnit parses a "50%" (or bare "0.5"→treated as fraction*100 not
// applied; here we require the percent form used by hsl()).
func parsePercentUnit(p string) (float64, bool) {
	p = strings.TrimSpace(p)
	p = strings.TrimSuffix(p, "%")
	f, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
	if err != nil {
		return 0, false
	}
	return f / 100, true
}

// hslToRGB converts HSL (h degrees, s and l in 0..1) to 8-bit RGB.
func hslToRGB(h, s, l float64) (uint8, uint8, uint8) {
	h = h - 360*mathFloor(h/360) // normalise into [0,360)
	c := (1 - absF(2*l-1)) * s
	hp := h / 60
	x := c * (1 - absF(hp-2*mathFloor(hp/2)-1))
	var r1, g1, b1 float64
	switch {
	case hp < 1:
		r1, g1, b1 = c, x, 0
	case hp < 2:
		r1, g1, b1 = x, c, 0
	case hp < 3:
		r1, g1, b1 = 0, c, x
	case hp < 4:
		r1, g1, b1 = 0, x, c
	case hp < 5:
		r1, g1, b1 = x, 0, c
	default:
		r1, g1, b1 = c, 0, x
	}
	m := l - c/2
	return clampByte((r1 + m) * 255), clampByte((g1 + m) * 255), clampByte((b1 + m) * 255)
}

func absF(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

func mathFloor(f float64) float64 {
	i := float64(int64(f))
	if f < 0 && i != f {
		i--
	}
	return i
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
	case strings.HasSuffix(s, "rem"):
		// rem is relative to the root font-size; approximated as 16px (the
		// Phase-0 root size, matching most pages). Checked before "em".
		f, err := strconv.ParseFloat(strings.TrimSpace(s[:len(s)-3]), 64)
		if err != nil {
			return Length{}, false
		}
		return Length{Px: f * 16}, true
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
	case strings.HasSuffix(s, "vw"), strings.HasSuffix(s, "vh"):
		// Viewport units are approximated as a percentage of the containing
		// block width. For the root/body (whose containing block is the viewport)
		// vw is exact; vh and nested vw are best-effort at this fidelity.
		f, err := strconv.ParseFloat(strings.TrimSpace(s[:len(s)-2]), 64)
		if err != nil {
			return Length{}, false
		}
		return Length{Percent: f / 100, IsPercent: true}, true
	}
	return Length{}, false
}
