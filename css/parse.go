// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package css

import (
	"regexp"
	"strconv"
	"strings"
)

// mediaWidthRe captures min-width/max-width features in a media query, in
// either px or rem — Tailwind v4's default breakpoints (sm/md/lg/xl/2xl) are
// all expressed in rem ("min-width:80rem"), not px, and rejecting the unit
// entirely (rather than just failing to convert it) used to make mediaMatches
// silently ignore every one of them, treating every responsive breakpoint as
// permanently active — observed live as tailwindcss.com's hero headline
// rendering far larger than any real viewport width should select, because
// its "xl:text-8xl" breakpoint (min-width:80rem = 1280px) matched even at a
// 1024px viewport.
var mediaWidthRe = regexp.MustCompile(`(min|max)-width\s*:\s*([0-9.]+)(px|rem)`)

// mediaWidthCmpRe captures the CSS Media Queries Level 4 range-comparison
// syntax for width ("width<=48rem", "width>=48rem", or the value-first order
// "48rem<=width"), in px or rem. GitHub's Primer design system uses exactly
// this form for its responsive PageLayout breakpoints; it was as invisible to
// the old min-width:/max-width: colon-only matcher as the missing "rem" unit
// was, for the same reason — falling through to "unknown feature, assume it
// matches" made every one of these breakpoints permanently active regardless
// of viewport width.
var mediaWidthCmpRe = regexp.MustCompile(
	`width\s*(<=|>=|<|>)\s*([0-9.]+)(px|rem)|([0-9.]+)(px|rem)\s*(<=|>=|<|>)\s*width`)

// mediaCalcRe evaluates a simple two-term "calc(A ± B)" expression appearing
// in a media feature value — e.g. GitHub's `calc(48rem - .02px)`, used to open
// a hair's-width gap just below the next breakpoint up so two adjacent ranges
// never both match the same viewport width. Both terms may be px or rem; more
// complex calc() expressions (products, nesting, more than two terms) are left
// as unparsed text, same as any other value this simplified matcher cannot
// handle — mediaWidthRe/mediaWidthCmpRe then simply find no match there.
var mediaCalcRe = regexp.MustCompile(`calc\(\s*([0-9.]+)(px|rem)\s*([+-])\s*([0-9.]+)(px|rem)\s*\)`)

// Declaration is a single property: value pair. Important marks a trailing
// `!important` on the original declaration; it is consulted by the cascade,
// never by a property's value parser (Value never carries the marker).
type Declaration struct {
	Property  string
	Value     string
	Important bool
}

// Rule is a parsed style rule: a list of selectors sharing a declaration block.
type Rule struct {
	Selectors    []Selector
	Declarations []Declaration

	// Container is non-nil when this rule came from (directly, or via nested
	// @media/@layer) an `@container` at-rule: its selectors only take effect
	// for a matched element when the condition holds against that element's
	// nearest qualifying ancestor container, evaluated at cascade time (see
	// container.go — this is deferred, unlike @media, because it depends on
	// per-element ancestor geometry rather than a single known viewport
	// width).
	Container *ContainerCondition
}

// stripComments removes /* ... */ comment spans.
func stripComments(s string) string {
	var b strings.Builder
	for {
		i := strings.Index(s, "/*")
		if i < 0 {
			b.WriteString(s)
			break
		}
		b.WriteString(s[:i])
		j := strings.Index(s[i+2:], "*/")
		if j < 0 {
			break // unterminated comment: drop the rest
		}
		s = s[i+2+j+2:]
	}
	return b.String()
}

// DefaultViewportWidth is the viewport width (CSS px) used to evaluate @media
// width queries when no explicit width is supplied. It matches a desktop render.
const DefaultViewportWidth = 1024

// ParseStylesheet parses a full stylesheet into rules at the default viewport
// width. See ParseStylesheetVW to control @media evaluation.
func ParseStylesheet(src string) []Rule {
	return ParseStylesheetVW(src, DefaultViewportWidth)
}

// ParseStylesheetVW parses a full stylesheet into rules, evaluating @media
// blocks against viewport width vw: a matching @media block's inner rules are
// included, a non-matching one is skipped. @layer blocks are unwrapped (their
// rules included as if the layer boundary were not there — this engine does
// not model cross-layer cascade priority, but that is far less wrong than
// dropping the content: Tailwind v4's default output, among many other
// frameworks, puts nearly all of its CSS inside @layer). @container blocks are
// always included, with their condition attached to each inner rule for the
// cascade to evaluate per-element once real layout geometry is available (see
// container.go) — @container style(...) queries are not supported and are
// skipped wholesale, like any other unrecognised at-rule (@font-face,
// @keyframes, @supports, @import, ...). Malformed rules are skipped
// defensively.
func ParseStylesheetVW(src string, vw float64) []Rule {
	return parseRules(stripComments(src), vw)
}

func parseRules(src string, vw float64) []Rule {
	var rules []Rule
	i := 0
	for i < len(src) {
		brace := strings.IndexByte(src[i:], '{')
		if brace < 0 {
			break
		}
		prelude := strings.TrimSpace(src[i : i+brace])
		blockStart := i + brace
		blockEnd, ok := matchBrace(src, blockStart)
		if !ok {
			break
		}
		body := src[blockStart+1 : blockEnd]
		i = blockEnd + 1

		// A bare at-rule statement ending in ';' (e.g. "@layer theme, base,
		// utilities;" declaring layer order, or "@import url(x);") has no '{' of
		// its own, so it rides along in the SAME prelude text as whatever
		// construct actually owns this brace. Only the text after the last such
		// ';' describes that construct.
		if semi := strings.LastIndexByte(prelude, ';'); semi >= 0 {
			prelude = strings.TrimSpace(prelude[semi+1:])
		}

		if strings.HasPrefix(prelude, "@") {
			lower := strings.ToLower(prelude)
			switch {
			case strings.HasPrefix(lower, "@media"):
				// Honour @media blocks whose width query matches the viewport; the
				// inner body is itself a list of rules.
				if mediaMatches(lower[len("@media"):], vw) {
					rules = append(rules, parseRules(body, vw)...)
				}
			case strings.HasPrefix(lower, "@layer"):
				// A named layer's body is itself a list of rules; a bare "@layer
				// name{...}" (anonymous or named) always applies — there is no
				// width/media condition to test, only a cascade PRIORITY this
				// engine does not model (see the doc comment above).
				rules = append(rules, parseRules(body, vw)...)
			case strings.HasPrefix(lower, "@container"):
				// Unlike @media/@layer, an @container condition cannot be resolved
				// here: it depends on an ANCESTOR ELEMENT's actual laid-out size,
				// which is per-matched-element and only known once layout has run
				// at least once. So the body is always parsed and included, with
				// the condition attached to every rule that comes out of it (see
				// container.go); the cascade evaluates it per-element, per-pass. A
				// condition this engine cannot represent at all (@container
				// style(...), a different, newer part of the spec) makes
				// parseContainerCondition report ok=false, and the body is then
				// dropped wholesale, like any other unrecognised at-rule.
				if cond, ok := parseContainerCondition(prelude); ok {
					inner := parseRules(body, vw)
					for i := range inner {
						inner[i].Container = mergeContainerCondition(inner[i].Container, cond)
					}
					rules = append(rules, inner...)
				}
			}
			// Every other at-rule (@font-face, @keyframes, @supports, ...) is
			// skipped wholesale, as before.
			continue
		}
		sels := ParseSelectorList(prelude)
		if len(sels) > 0 {
			decls := ParseDeclarations(body)
			if len(decls) > 0 {
				rules = append(rules, Rule{Selectors: sels, Declarations: decls})
			}
		}
	}
	return rules
}

// mediaMatches evaluates a simplified @media condition against viewport width
// vw. print media never matches; min-width/max-width pixel features (colon
// syntax and the Level 4 comparison syntax) are honoured (all must hold);
// anything else (screen/all/unknown features) matches optimistically so
// desktop layout rules are applied.
func mediaMatches(cond string, vw float64) bool {
	if strings.Contains(cond, "print") {
		return false
	}
	cond = mediaCalcRe.ReplaceAllStringFunc(cond, func(m string) string {
		g := mediaCalcRe.FindStringSubmatch(m)
		a, b := lengthToPx(g[1], g[2]), lengthToPx(g[4], g[5])
		v := a + b
		if g[3] == "-" {
			v = a - b
		}
		return strconv.FormatFloat(v, 'f', -1, 64) + "px"
	})
	for _, m := range mediaWidthRe.FindAllStringSubmatch(cond, -1) {
		if _, err := strconv.ParseFloat(m[2], 64); err != nil {
			continue
		}
		n := lengthToPx(m[2], m[3])
		if m[1] == "min" && vw < n {
			return false
		}
		if m[1] == "max" && vw > n {
			return false
		}
	}
	for _, m := range mediaWidthCmpRe.FindAllStringSubmatch(cond, -1) {
		var op, numStr, unit string
		if m[1] != "" {
			op, numStr, unit = m[1], m[2], m[3]
		} else {
			numStr, unit, op = m[4], m[5], flipCmp(m[6])
		}
		if _, err := strconv.ParseFloat(numStr, 64); err != nil {
			continue
		}
		n := lengthToPx(numStr, unit)
		switch op {
		case "<":
			if vw >= n {
				return false
			}
		case "<=":
			if vw > n {
				return false
			}
		case ">":
			if vw <= n {
				return false
			}
		case ">=":
			if vw < n {
				return false
			}
		}
	}
	return true
}

// lengthToPx converts a numeric media-feature length to px, at the same 16px
// root font-size approximation length parsing uses elsewhere (see the "rem"
// case in parseLength) — kept consistent so a page's rem-based breakpoints and
// its rem-based element sizes agree. numStr is assumed already validated by
// the caller (via strconv.ParseFloat); a residual error here just yields 0,
// which cannot spuriously satisfy either a min or a max comparison in a way
// that hides content — it only ever narrows which viewports the condition
// covers, never widens it.
func lengthToPx(numStr, unit string) float64 {
	n, _ := strconv.ParseFloat(numStr, 64)
	if unit == "rem" {
		n *= 16
	}
	return n
}

// flipCmp reverses a comparison operator, for normalising the value-first
// media range form ("48rem<=width") to the width-first form mediaMatches
// evaluates ("width>=48rem" — the two say the same thing).
func flipCmp(op string) string {
	switch op {
	case "<":
		return ">"
	case "<=":
		return ">="
	case ">":
		return "<"
	case ">=":
		return "<="
	}
	return op
}

// matchBrace returns the index of the '}' matching the '{' at open.
func matchBrace(s string, open int) (int, bool) {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i, true
			}
		}
	}
	return 0, false
}

// ParseDeclarations parses a declaration block body ("a: b; c: d") into
// declarations, lowercasing property names and trimming values.
func ParseDeclarations(body string) []Declaration {
	var out []Declaration
	for _, chunk := range splitDeclChunks(body) {
		chunk = strings.TrimSpace(chunk)
		if chunk == "" {
			continue
		}
		colon := strings.IndexByte(chunk, ':')
		if colon < 0 {
			continue
		}
		prop := strings.TrimSpace(chunk[:colon])
		// Custom-property names (--foo) are case-sensitive; normal property names
		// are case-insensitive and canonicalised to lower case.
		if !isCustomProperty(prop) {
			prop = strings.ToLower(prop)
		}
		val := strings.TrimSpace(chunk[colon+1:])
		// A trailing !important is not part of the value — strip it here, once,
		// so no property parser downstream ever has to know about it.
		important := false
		if idx := strings.LastIndex(strings.ToLower(val), "!important"); idx >= 0 {
			val = strings.TrimSpace(val[:idx])
			important = true
		}
		// An empty VALUE is meaningless for an ordinary property (there is
		// nothing to apply) but is a real, spec-valid, and load-bearing state
		// for a CUSTOM property: `--foo: ;` explicitly sets --foo to the empty
		// token stream, which is how the widespread "CSS toggle" pattern
		// (postcss-preset-env's light-dark() polyfill, seen live on
		// developer.mozilla.org) switches themes — one selector sets a guard
		// variable to `initial`, another (e.g. inside `@media
		// (prefers-color-scheme:dark)`) sets the SAME variable to empty to
		// flip every var() chain that reads it. Dropping the empty
		// declaration here left the guard stuck at its non-empty branch
		// forever, regardless of which media/attribute condition actually
		// applied — collapsing the page's entire colour system to its
		// pre-JS/light default.
		if prop == "" || (val == "" && !isCustomProperty(prop)) {
			continue
		}
		out = append(out, Declaration{Property: prop, Value: val, Important: important})
	}
	return out
}

// splitDeclChunks splits a declaration block on top-level semicolons, ignoring
// semicolons nested inside parentheses (e.g. a `url(data:…;base64,…)` value) or
// inside single/double quotes. This keeps a data-URI or function argument that
// contains ';' from being torn across two declarations.
func splitDeclChunks(body string) []string {
	var out []string
	depth := 0
	var quote byte
	start := 0
	for i := 0; i < len(body); i++ {
		c := body[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '(':
			depth++
		case c == ')':
			if depth > 0 {
				depth--
			}
		case c == ';' && depth == 0:
			out = append(out, body[start:i])
			start = i + 1
		}
	}
	out = append(out, body[start:])
	return out
}

// apply mutates s by applying one declaration. Unknown properties and
// unparseable values are ignored. emRef is the font-size used to resolve em
// lengths (the element's own computed font-size).
func (s *Style) apply(d Declaration, emRef float64) {
	v := strings.TrimSpace(d.Value)
	lv := strings.ToLower(v)
	switch d.Property {
	case "display":
		// A non-list display clears the list-item flag; `list-item` sets it (it is
		// a block-level box that additionally generates a marker).
		s.ListItem = false
		switch lv {
		case "list-item":
			s.Display, s.ListItem = DisplayBlock, true
		case "block", "flow-root":
			s.Display = DisplayBlock
		case "grid", "inline-grid":
			s.Display = DisplayGrid
		case "flex", "inline-flex":
			s.Display = DisplayFlex
		case "table", "inline-table":
			s.Display = DisplayTable
		case "table-row":
			s.Display = DisplayTableRow
		case "table-cell":
			s.Display = DisplayTableCell
		case "table-row-group", "table-header-group", "table-footer-group":
			s.Display = DisplayTableRowGroup
		case "inline-block":
			s.Display = DisplayInlineBlock
		case "inline":
			s.Display = DisplayInline
		case "none":
			s.Display = DisplayNone
		}
	case "visibility":
		switch lv {
		case "visible":
			s.Visibility = VisibilityVisible
		case "hidden":
			s.Visibility = VisibilityHidden
		case "collapse":
			s.Visibility = VisibilityCollapse
		}
	case "color":
		if c, ok := parseColor(v); ok {
			s.Color = c
		}
	case "background-color":
		// The whole value is the colour (may itself contain spaces, e.g. the
		// modern `rgb(22 24 29 / 1)` syntax), so parse it directly.
		if c, ok := parseColor(v); ok {
			s.Background = c
		}
	case "background":
		// The shorthand may carry image/position/repeat layers; pick up a leading
		// colour token, which may be a function with internal spaces.
		if c, ok := parseColor(backgroundColorToken(v)); ok {
			s.Background = c
		}
		// Also pick up gradient / url() image layers from the shorthand so a
		// `background: linear-gradient(...)` (no separate background-image) paints.
		if imgs, ok := parseBackgroundImage(v, emRef); ok {
			s.BackgroundImages = imgs
		}
	case "background-image":
		if imgs, ok := parseBackgroundImage(v, emRef); ok {
			s.BackgroundImages = imgs
		} else if strings.EqualFold(lv, "none") {
			s.BackgroundImages = nil
		}
	case "background-size":
		if sz, ok := parseBackgroundSizeList(v, emRef); ok {
			s.BackgroundSize = sz
		}
	case "background-position":
		if p, ok := parseBackgroundPositionList(v, emRef); ok {
			s.BackgroundPosition = p
		}
	case "background-repeat":
		if r, ok := parseBackgroundRepeat(v); ok {
			s.BackgroundRepeat = []BgRepeat{r}
		}
	case "box-shadow":
		if sh, ok := parseBoxShadow(v, emRef); ok {
			s.BoxShadows = sh
		}
	case "opacity":
		if f, err := strconv.ParseFloat(lv, 64); err == nil {
			if f < 0 {
				f = 0
			} else if f > 1 {
				f = 1
			}
			s.Opacity, s.HasOpacity = f, true
		}
	case "filter":
		if fs, ok := parseFilterList(v, emRef); ok {
			s.Filters = fs
		}
	case "font-size":
		if l, ok := parseLength(v, emRef); ok && !l.Auto {
			if l.IsPercent {
				s.FontSize = l.Percent * emRef
			} else {
				s.FontSize = l.Px
			}
		}
	case "font-weight":
		switch lv {
		case "bold", "bolder":
			s.FontWeight = 700
		case "normal", "lighter":
			s.FontWeight = 400
		default:
			if n, ok := atoiClamp(lv); ok {
				s.FontWeight = n
			}
		}
	case "font-style":
		// italic and oblique both select the slanted face; normal resets it.
		switch lv {
		case "italic", "oblique":
			s.Italic = true
		case "normal":
			s.Italic = false
		}
	case "font-family":
		s.FontFamily = parseFontFamily(lv)
	case "text-align":
		switch lv {
		case "left", "start":
			s.TextAlign = AlignLeft
		case "center":
			s.TextAlign = AlignCenter
		case "right", "end":
			s.TextAlign = AlignRight
		case "-webkit-center", "-moz-center":
			// Legacy centre-including-blocks, emitted by the UA rule for <center>
			// and by the align="center" presentational hint.
			s.TextAlign = AlignCenterBlocks
		}
	case "white-space":
		switch lv {
		case "pre", "pre-wrap", "pre-line":
			s.WhiteSpace = WSPre
		case "normal", "nowrap":
			s.WhiteSpace = WSNormal
		}
	case "image-rendering":
		switch lv {
		case "pixelated", "crisp-edges", "-webkit-optimize-contrast":
			s.ImageRendering = IRPixelated
		case "auto", "smooth", "high-quality", "optimizequality":
			s.ImageRendering = IRAuto
		}
	case "list-style-type":
		if t, ok := parseListStyleType(lv); ok {
			s.ListStyleType = t
		}
	case "list-style-position":
		switch lv {
		case "outside":
			s.ListStylePosition = ListOutside
		case "inside":
			s.ListStylePosition = ListInside
		}
	case "list-style":
		applyListStyle(s, lv)
	case "width":
		if l, ok := parseLength(v, emRef); ok {
			s.Width = l
		}
	case "min-width":
		if l, ok := parseLength(v, emRef); ok {
			s.MinWidth = l
		} else if lv == "none" {
			s.MinWidth = Length{Auto: true}
		}
	case "max-width":
		if l, ok := parseLength(v, emRef); ok {
			s.MaxWidth = l
		} else if lv == "none" {
			s.MaxWidth = Length{Auto: true}
		}
	case "height":
		if l, ok := parseLength(v, emRef); ok {
			s.Height = l
		}
	case "min-height":
		if l, ok := parseLength(v, emRef); ok {
			s.MinHeight = l
		} else if lv == "none" {
			s.MinHeight = Length{Auto: true}
		}
	case "max-height":
		if l, ok := parseLength(v, emRef); ok {
			s.MaxHeight = l
		} else if lv == "none" {
			s.MaxHeight = Length{Auto: true}
		}
	case "box-sizing":
		switch lv {
		case "border-box":
			s.BoxSizing = BorderBox
		case "content-box":
			s.BoxSizing = ContentBox
		}
	case "overflow":
		// One value sets both axes; two values are `overflow-x overflow-y`.
		f := strings.Fields(lv)
		if len(f) == 1 {
			if o, ok := parseOverflowKeyword(f[0]); ok {
				s.OverflowX, s.OverflowY = o, o
			}
		} else if len(f) >= 2 {
			if o, ok := parseOverflowKeyword(f[0]); ok {
				s.OverflowX = o
			}
			if o, ok := parseOverflowKeyword(f[1]); ok {
				s.OverflowY = o
			}
		}
	case "overflow-x":
		if o, ok := parseOverflowKeyword(lv); ok {
			s.OverflowX = o
		}
	case "overflow-y":
		if o, ok := parseOverflowKeyword(lv); ok {
			s.OverflowY = o
		}
	case "line-height":
		if lh, ok := parseLineHeight(v, emRef); ok {
			s.LineHeight = lh
		}
	case "float":
		switch lv {
		case "left":
			s.Float = FloatLeft
		case "right":
			s.Float = FloatRight
		case "none":
			s.Float = FloatNone
		}
	case "clear":
		switch lv {
		case "left":
			s.Clear = ClearLeft
		case "right":
			s.Clear = ClearRight
		case "both":
			s.Clear = ClearBoth
		case "none":
			s.Clear = ClearNone
		}
	case "position":
		switch lv {
		case "static":
			s.Position = PositionStatic
		case "relative":
			s.Position = PositionRelative
		case "absolute":
			s.Position = PositionAbsolute
		case "fixed":
			s.Position = PositionFixed
		case "sticky":
			s.Position = PositionSticky
		}
	case "top":
		if l, ok := parseLength(v, emRef); ok {
			s.Top = l
		}
	case "right":
		if l, ok := parseLength(v, emRef); ok {
			s.Right = l
		}
	case "bottom":
		if l, ok := parseLength(v, emRef); ok {
			s.Bottom = l
		}
	case "left":
		if l, ok := parseLength(v, emRef); ok {
			s.Left = l
		}
	case "z-index":
		if lv == "auto" {
			s.ZIndexAuto = true
		} else if n, err := strconv.Atoi(lv); err == nil {
			s.ZIndex, s.ZIndexAuto = n, false
		}
	case "flex-direction":
		switch lv {
		case "row", "row-reverse":
			s.FlexDirection = FlexRow
		case "column", "column-reverse":
			s.FlexDirection = FlexColumn
		}
	case "justify-content":
		if j, ok := parseJustify(lv); ok {
			s.JustifyContent = j
		}
	case "align-items":
		if a, ok := parseAlignItems(lv); ok {
			s.AlignItems = a
		}
	case "flex-grow":
		if f, err := strconv.ParseFloat(lv, 64); err == nil && f >= 0 {
			s.FlexGrow = f
		}
	case "flex-shrink":
		if f, err := strconv.ParseFloat(lv, 64); err == nil && f >= 0 {
			s.FlexShrink = f
		}
	case "flex-basis":
		if l, ok := parseLength(v, emRef); ok {
			s.FlexBasis = l
		} else if lv == "content" {
			s.FlexBasis = Length{Auto: true}
		}
	case "flex":
		applyFlexShorthand(s, v, emRef)
	case "flex-wrap":
		if w, ok := parseFlexWrap(lv); ok {
			s.FlexWrap = w
		}
	case "flex-flow":
		applyFlexFlow(s, v)
	case "align-content":
		if a, ok := parseAlignContent(lv); ok {
			s.AlignContent = a
		}
	case "align-self":
		if a, ok := parseAlignSelf(lv); ok {
			s.AlignSelf = a
		}
	case "justify-self":
		if a, ok := parseAlignSelf(lv); ok {
			s.JustifySelf = a
		}
	case "justify-items":
		if a, ok := parseAlignItems(lv); ok {
			s.JustifyItems = a
		}
	case "order":
		if n, err := strconv.Atoi(lv); err == nil {
			s.Order = n
		}
	case "gap", "grid-gap":
		applyGap(s, v, emRef)
	case "row-gap", "grid-row-gap":
		if l, ok := parseLength(v, emRef); ok && !l.Auto {
			s.RowGap = l
		}
	case "column-gap", "grid-column-gap":
		if l, ok := parseLength(v, emRef); ok && !l.Auto {
			s.ColumnGap = l
		}
	case "place-items":
		applyPlaceItems(s, v)
	case "place-content":
		applyPlaceContent(s, lv)
	case "place-self":
		applyPlaceSelf(s, v)
	case "grid-template-columns":
		if t, ok := parseTrackList(v, emRef); ok {
			s.GridTemplateColumns = t
		}
	case "grid-template-rows":
		if t, ok := parseTrackList(v, emRef); ok {
			s.GridTemplateRows = t
		}
	case "grid-auto-columns":
		if t, ok := parseTrackSize(v, emRef); ok {
			s.GridAutoColumns = t
		}
	case "grid-auto-rows":
		if t, ok := parseTrackSize(v, emRef); ok {
			s.GridAutoRows = t
		}
	case "grid-auto-flow":
		if strings.Contains(lv, "column") {
			s.GridAutoFlow = GridFlowColumn
		} else if strings.Contains(lv, "row") {
			s.GridAutoFlow = GridFlowRow
		}
	case "grid-template-areas":
		if a, ok := parseGridTemplateAreas(v); ok {
			s.GridTemplateAreas = a
		}
	case "grid-column":
		s.GridColumnStart, s.GridColumnEnd = parseGridPlacement(v)
	case "grid-row":
		s.GridRowStart, s.GridRowEnd = parseGridPlacement(v)
	case "grid-column-start":
		s.GridColumnStart = parseGridLine(v)
	case "grid-column-end":
		s.GridColumnEnd = parseGridLine(v)
	case "grid-row-start":
		s.GridRowStart = parseGridLine(v)
	case "grid-row-end":
		s.GridRowEnd = parseGridLine(v)
	case "grid-area":
		applyGridArea(s, v)
	case "border-radius":
		if l, ok := parseBorderRadius(v, emRef); ok {
			s.BorderRadius = l
		}
	case "border-top-left-radius", "border-top-right-radius",
		"border-bottom-left-radius", "border-bottom-right-radius":
		// Per-corner radii collapse to the single uniform radius (last wins).
		if l, ok := parseBorderRadius(v, emRef); ok {
			s.BorderRadius = l
		}
	case "border":
		applyBorderShorthand(&s.Border, v, emRef, s.Color)
	case "border-top":
		applyBorderSideShorthand(&s.Border.Top, v, emRef, s.Color)
	case "border-right":
		applyBorderSideShorthand(&s.Border.Right, v, emRef, s.Color)
	case "border-bottom":
		applyBorderSideShorthand(&s.Border.Bottom, v, emRef, s.Color)
	case "border-left":
		applyBorderSideShorthand(&s.Border.Left, v, emRef, s.Color)
	case "border-width":
		applyBorderWidth(&s.Border, v, emRef)
	case "border-style":
		applyBorderStyle(&s.Border, v)
	case "border-color":
		applyBorderColor(&s.Border, v)
	case "border-top-width":
		applyBorderEdgeWidth(&s.Border.Top, v, emRef)
	case "border-right-width":
		applyBorderEdgeWidth(&s.Border.Right, v, emRef)
	case "border-bottom-width":
		applyBorderEdgeWidth(&s.Border.Bottom, v, emRef)
	case "border-left-width":
		applyBorderEdgeWidth(&s.Border.Left, v, emRef)
	case "margin":
		applyMarginShorthand(s, v, emRef)
	case "margin-top":
		applyEdge(&s.Margin.Top, v, emRef)
	case "margin-right":
		applyMarginSide(&s.Margin.Right, &s.MarginRightAuto, v, emRef)
	case "margin-bottom":
		applyEdge(&s.Margin.Bottom, v, emRef)
	case "margin-left":
		applyMarginSide(&s.Margin.Left, &s.MarginLeftAuto, v, emRef)
	case "padding":
		if e, ok := parseEdges(v, emRef); ok {
			s.Padding = e
		}
	case "padding-top":
		applyEdge(&s.Padding.Top, v, emRef)
	case "padding-right":
		applyEdge(&s.Padding.Right, v, emRef)
	case "padding-bottom":
		applyEdge(&s.Padding.Bottom, v, emRef)
	case "padding-left":
		applyEdge(&s.Padding.Left, v, emRef)
	case "container-type":
		if ct, ok := containerTypeKeyword(lv); ok {
			s.ContainerType = ct
		}
	case "container-name":
		// container-name is a case-sensitive custom-ident (or a space-separated
		// list of them, though this engine only ever compares against a single
		// name — see ContainerCondition.Name); "none" clears it.
		if lv == "none" {
			s.ContainerName = ""
		} else {
			s.ContainerName = v
		}
	case "container":
		applyContainerShorthand(s, v)
	}
}

func applyEdge(dst *float64, v string, emRef float64) {
	if l, ok := parseLength(v, emRef); ok && !l.Auto && !l.IsPercent {
		*dst = l.Px
	}
}

// parseEdges parses the 1-to-4 value shorthand for margin/padding. Percentages
// and auto collapse to 0 (Phase 0 does not resolve them for the shorthand).
func parseEdges(v string, emRef float64) (Edges, bool) {
	fields := strings.Fields(v)
	px := make([]float64, 0, len(fields))
	for _, f := range fields {
		l, ok := parseLength(f, emRef)
		if !ok {
			return Edges{}, false
		}
		if l.Auto || l.IsPercent {
			px = append(px, 0)
		} else {
			px = append(px, l.Px)
		}
	}
	switch len(px) {
	case 1:
		return Edges{px[0], px[0], px[0], px[0]}, true
	case 2:
		return Edges{px[0], px[1], px[0], px[1]}, true
	case 3:
		return Edges{px[0], px[1], px[2], px[1]}, true
	case 4:
		return Edges{px[0], px[1], px[2], px[3]}, true
	}
	return Edges{}, false
}

func parseFontFamily(lv string) FontFamily {
	lv = strings.ToLower(lv)
	// Inspect each comma-separated family; the first recognised generic wins.
	for _, fam := range strings.Split(lv, ",") {
		fam = strings.TrimSpace(strings.Trim(strings.TrimSpace(fam), `"'`))
		switch {
		case fam == "monospace" || strings.Contains(fam, "mono") ||
			strings.Contains(fam, "courier") || strings.Contains(fam, "consolas"):
			return Mono
		case fam == "serif" || strings.Contains(fam, "times") ||
			strings.Contains(fam, "georgia") || strings.Contains(fam, "lora"):
			return Serif
		case fam == "sans-serif" || strings.Contains(fam, "sans") ||
			strings.Contains(fam, "arial") || strings.Contains(fam, "helvetica") ||
			strings.Contains(fam, "inter") || strings.Contains(fam, "roboto"):
			return Sans
		}
	}
	return Sans
}

// backgroundColorToken extracts a leading colour token from a `background`
// shorthand value. A functional colour (rgb()/rgba()/hsl()/hsla()) is returned
// whole (including internal spaces and its parenthesised argument list);
// otherwise the first whitespace-delimited token is returned. Gradient and
// url() image layers are left for the (unimplemented) background-image path and
// simply fail to parse as a colour.
func backgroundColorToken(v string) string {
	v = strings.TrimSpace(v)
	lv := strings.ToLower(v)
	for _, fn := range []string{"rgb(", "rgba(", "hsl(", "hsla("} {
		if strings.HasPrefix(lv, fn) {
			if close := strings.IndexByte(v, ')'); close >= 0 {
				return v[:close+1]
			}
		}
	}
	return firstToken(v)
}

func firstToken(v string) string {
	f := strings.Fields(v)
	if len(f) == 0 {
		return ""
	}
	return f[0]
}

func atoiClamp(s string) (int, bool) {
	n := 0
	if s == "" {
		return 0, false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, true
}
