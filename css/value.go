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

	gfxcolor "github.com/go-gfx/gfx/color"
)

// Color is an 8-bit-per-channel RGBA colour. A==0 is treated as transparent.
type Color struct{ R, G, B, A uint8 }

// Transparent is the fully-transparent colour (the initial background-color).
var Transparent = Color{}

// Visibility is the visibility property: unlike Display, it is INHERITED, and
// hiding an ancestor still reserves its layout space and does not stop a
// descendant that resets `visibility:visible` from painting. `collapse` (its
// real effect is table-row-specific: the row's space is reclaimed) is treated
// identically to VisibilityHidden — a documented simplification, not a bug.
type Visibility uint8

const (
	// VisibilityVisible is the initial value.
	VisibilityVisible Visibility = iota
	// VisibilityHidden paints nothing for this box (background, border,
	// shadows, inline content) while still occupying its layout space; a
	// descendant box may re-show itself with its own `visibility:visible`.
	VisibilityHidden
	// VisibilityCollapse is treated the same as VisibilityHidden (see above).
	VisibilityCollapse
)

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
	// DisplayContents makes the element generate no box of its own — its
	// children behave as if they were direct children of ITS parent instead
	// (the element itself becomes fully transparent to layout, though it still
	// exists in the DOM/cascade). See layout.renderedChildren, the single
	// chokepoint every layout algorithm (block, inline, flex, grid, table)
	// walks a container's children through, which is where this is resolved —
	// so a DisplayContents element never itself reaches box placement.
	DisplayContents
	// DisplayInlineFlex is a flex container that is itself an INLINE-level
	// box in its parent's flow (unlike DisplayFlex, which is block-level) —
	// distinct from DisplayFlex specifically so isBlockLevel and the inline-
	// collection walk can tell them apart; internally it lays out its
	// children with the identical flex algorithm. Confirmed live on
	// pkg.go.dev: `.go-Breadcrumb li{display:inline-flex}` stacked each
	// breadcrumb item on its own line instead of flowing in a row, because
	// this engine previously parsed `inline-flex` down to the SAME
	// DisplayFlex value as `flex`, losing the "inline" qualifier entirely and
	// making every such element block-level. See
	// layout.appendElementInline's own handling for how the atomic
	// inline-level box is produced.
	DisplayInlineFlex
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

// parseClipRect parses the legacy `clip: rect(top, right, bottom, left)`
// value (CSS2's required comma-separated form, and the later space-separated
// relaxation both accepted). Only the case every real-world use of this
// property this engine has met actually needs — all four edges given as
// explicit lengths — is modelled; a bare `auto` edge, a percentage (the spec
// forbids one anyway), or anything else parseLength does not resolve makes
// the whole value unrecognised (ok=false) rather than guessed at.
func parseClipRect(v string, emRef float64) (Edges, bool) {
	v = strings.TrimSpace(v)
	if !strings.HasPrefix(v, "rect(") || !strings.HasSuffix(v, ")") {
		return Edges{}, false
	}
	inner := v[len("rect(") : len(v)-1]
	fields := strings.FieldsFunc(inner, func(r rune) bool { return r == ',' || r == ' ' })
	if len(fields) != 4 {
		return Edges{}, false
	}
	var vals [4]float64
	for i, f := range fields {
		l, ok := parseLength(f, emRef)
		if !ok || l.Auto || l.IsPercent {
			return Edges{}, false
		}
		vals[i] = l.Px
	}
	return Edges{Top: vals[0], Right: vals[1], Bottom: vals[2], Left: vals[3]}, true
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

// ImageRendering selects the resampling filter used to scale a raster image
// (an <img> or a background-image) to a size other than its intrinsic one. It
// inherits (per CSS Images 3).
type ImageRendering uint8

const (
	// IRAuto lets the engine pick a high-quality (smooth) filter — a bicubic
	// resample. It is the initial value and also covers `smooth` / `high-quality`.
	IRAuto ImageRendering = iota
	// IRPixelated preserves hard pixel edges by nearest-neighbour sampling, the
	// behaviour pixel art wants (`image-rendering: pixelated`). `crisp-edges`
	// maps here too: both ask the engine not to smooth the image.
	IRPixelated
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
	Visibility Visibility // inherited; see the Visibility doc comment
	Color      Color
	Background Color
	FontSize   float64 // px
	FontWeight int     // 400 = normal, 700 = bold
	FontFamily FontFamily
	Italic     bool // font-style: italic|oblique (inherited)

	// Fill/Stroke are the SVG paint properties (inherited, like Color).
	// FillSet/StrokeSet distinguish "CSS resolved a concrete colour" from
	// "left to the SVG document's own presentation attribute" — svg.go's
	// serializeSVG is the only reader: unset changes nothing (the element's
	// own `fill`/`stroke` XML attribute, if any, survives untouched), set
	// overrides it with the CSS value. FillNone/StrokeNone models
	// `fill:none`/`stroke:none` (paint suppressed) separately, since Color{}
	// (transparent black) is also a legitimate real colour value. Confirmed
	// load-bearing live: tailwindcss.com's nav icons (search glyph, version-
	// badge chevron, logo mark) and CTA underline are coloured entirely via
	// Tailwind's `fill-*`/`stroke-*` utility classes, never an XML `fill=`
	// attribute — before this, every such icon rasterised with SVG's initial
	// fill (black), often invisible against a dark background.
	Fill       Color
	FillSet    bool
	FillNone   bool
	Stroke     Color
	StrokeSet  bool
	StrokeNone bool

	Margin Edges
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

	// CenterAsBlock is TextAlign == AlignCenterBlocks for every element,
	// EXCEPT it stays true for a <table> whose PARENT has TextAlign ==
	// AlignCenterBlocks even in quirks mode, where the table's OWN TextAlign
	// is separately reset to AlignLeft (see css/ua.go's quirks-mode table
	// rule) so its cells' text is correctly left-, not centre-, aligned.
	// Layout's "centre a definite-width block within its container" rule
	// (the legacy <center>/align=center effect) reads THIS field rather than
	// TextAlign directly, because real browsers keep the two questions
	// (should MY OWN text be centred vs. should I, as a block, be centred
	// within my parent) independent for exactly this one quirks-mode case —
	// confirmed against the HTML standard, whose quirks-mode reset is scoped
	// to the `table` selector alone. Every other element (including a
	// <center> with its own definite width, which must centre ITSELF using
	// its own AlignCenterBlocks default regardless of its parent) is
	// unaffected: CenterAsBlock and TextAlign==AlignCenterBlocks always
	// agree for it.
	CenterAsBlock bool

	// ImageRendering selects the scaling filter for raster images (inherited).
	// The initial value IRAuto means high-quality (bicubic); IRPixelated asks
	// for nearest-neighbour (pixel art).
	ImageRendering ImageRendering

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

	// HasClip/ClipRect model the legacy `clip: rect(top, right, bottom, left)`
	// property — deprecated in favour of clip-path, but still a live, real-
	// world pattern: confirmed load-bearing on pkg.go.dev's own "skip to main
	// content" link (`clip:rect(0 0 0 0)`, the CSS2-era screen-reader-only
	// idiom, predating the now-common `width:1px;height:1px;overflow:hidden`
	// version this engine already honours via Overflow above — that one has
	// no effect here since this element sets no explicit small width/height,
	// only `clip`). Per spec this only applies when Position is absolute or
	// fixed, and all four edges are distances from the box's own top-left
	// BORDER edge (not the CSS shorthand's usual top/right/bottom/left box
	// edges) — `rect(0,0,0,0)` is therefore a zero-size rectangle at the
	// box's own corner, clipping it and its content to nothing while it
	// still occupies its normal layout position (unlike display:none). Only
	// the common case (all four values explicit lengths) is modelled; a
	// `clip: auto` per edge, or any other value this engine's length parsing
	// does not resolve, leaves HasClip false — clip:rect() with a genuinely
	// variable edge is rare in practice (the sr-only idiom always hard-codes
	// all four to 0), so this is not a real-world loss.
	HasClip  bool
	ClipRect Edges // Top, Right, Bottom, Left offsets from the box's own corner

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

	// TranslateX/TranslateY are the ONE subset of the `transform` property
	// this engine understands: a pure 2D translate (see parseTransformTranslate
	// in parse.go). They are a paint-time offset applied like a relative
	// position shift, not a layout input — the initial/reset value (Length's
	// Go zero value, IsPercent false and Px 0) is exactly "no translation", so
	// unlike most other reset-not-inherited fields there is nothing to list
	// explicitly in inheritFrom. Any OTHER transform function (rotate, scale,
	// skew, matrix, 3D, or a mix including translate) is unsupported and
	// leaves both fields at zero, same as before this property was understood
	// at all — see FIDELITY.md's Known gaps.
	TranslateX Length
	TranslateY Length

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

	// MaskImage is the resolved absolute URL of a `mask-image`/
	// `-webkit-mask-image: url(...)` — empty means no mask (the common case).
	// Unlike background-image, a mask does not paint its own pixels: it is an
	// alpha stencil applied over everything the ELEMENT ITSELF paints (colour,
	// background, borders, text, children), stretched to fill the element's
	// border box (this engine's one deliberate simplification — the real
	// mask-size/mask-position/mask-repeat grammar is not modelled, matching
	// how far this engine narrowly scoped `transform: translate()`). Confirmed
	// load-bearing live: modern MediaWiki (Wikipedia's Vector-2022 skin, and
	// most of its UI icon systems generally) renders EVERY toolbar icon —
	// hamburger menu, search, language switcher — as an empty, solid-coloured
	// <span> cut into shape by exactly this mechanism, so the icon recolours
	// for dark mode without needing a second image asset. A gradient or other
	// non-url() mask value is not modelled and leaves this empty.
	MaskImage string

	// BoxShadows are the element's box-shadow layers (first-listed paints on top).
	BoxShadows []BoxShadow

	// Opacity is the element's group opacity in [0,1]; HasOpacity distinguishes a
	// genuine opacity from the zero value (an unset Style is fully opaque).
	Opacity    float64
	HasOpacity bool

	// Filters is the element's `filter` function chain, applied in order to the
	// element's rendered output (its box plus subtree) as a group. Nil == no
	// filter. Not inherited (`filter` is a non-inherited property).
	Filters []Filter

	// ContainerType and ContainerName are `container-type`/`container-name`
	// (or the `container` shorthand): whether this element establishes a
	// query container for descendant `@container` rules, and under what
	// name. Neither is inherited; the initial value is ContainerNormal / "".
	ContainerType ContainerType
	ContainerName string
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
		Display:        DisplayInline, // reset (non-inherited)
		Visibility:     parent.Visibility,
		Color:          parent.Color,
		Fill:           parent.Fill,
		FillSet:        parent.FillSet,
		FillNone:       parent.FillNone,
		Stroke:         parent.Stroke,
		StrokeSet:      parent.StrokeSet,
		StrokeNone:     parent.StrokeNone,
		Background:     Transparent, // reset
		FontSize:       parent.FontSize,
		FontWeight:     parent.FontWeight,
		FontFamily:     parent.FontFamily,
		Italic:         parent.Italic,      // inherited
		Width:          Length{Auto: true}, // reset
		MinWidth:       Length{Auto: true},
		MaxWidth:       Length{Auto: true},
		Height:         Length{Auto: true},
		MinHeight:      Length{Auto: true},
		MaxHeight:      Length{Auto: true},
		FlexBasis:      Length{Auto: true},
		FlexShrink:     1,
		TextAlign:      parent.TextAlign,      // inherited
		WhiteSpace:     parent.WhiteSpace,     // inherited
		LineHeight:     parent.LineHeight,     // inherited
		ImageRendering: parent.ImageRendering, // inherited
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

		// container-type/container-name are not inherited: every element
		// resets to the initial value (ContainerNormal / "") here, which is
		// also the Go zero value, so this is a no-op left explicit for
		// documentation alongside the rest of this reset list.
		ContainerType: ContainerNormal,
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
	// HSL -> sRGB is a colour-space conversion, not CSS parsing: route it through
	// go-gfx's shared colour layer rather than a local copy. gfxcolor.HSLToSRGB
	// returns gamma-sRGB channels in 0..1; scale and clamp to 8-bit exactly as
	// the previous in-package hslToRGB did (byte-identical).
	rf, gf, bf := gfxcolor.HSLToSRGB(gfxcolor.HSL{H: h, S: sPerc, L: lPerc})
	return Color{clampByte(rf * 255), clampByte(gf * 255), clampByte(bf * 255), a}, true
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
