// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package layout

import (
	"math"
	"strconv"
	"strings"

	"github.com/go-webengine/engine/css"
	"github.com/go-webengine/engine/dom"
)

// layouter carries the shared state for one layout pass.
type layouter struct {
	sm      css.StyleMap
	m       Measurer
	imgSize map[*dom.Node][2]float64 // intrinsic sizes for <img>, may be nil
	floats  *floatCtx
	// outOfFlow collects absolutely/fixed-positioned elements encountered during
	// the in-flow pass (and while laying out other positioned subtrees). They are
	// placed against their containing block after the in-flow layout.
	outOfFlow []outOfFlowItem

	// Inline whitespace-collapsing state, valid only while collecting one inline
	// formatting context (reset at each collectInline / collectInlineFrom entry).
	// wsPending records that the content emitted so far ends with collapsible
	// whitespace, so the next word takes a leading space; wsEmitted records that
	// at least one inline item has been emitted, so leading whitespace at the very
	// start of the context collapses away (no spurious indent). CSS collapses
	// whitespace ACROSS inline-element boundaries, which is why this is layouter
	// state threaded through the recursive collection rather than per text node.
	wsPending bool
	wsEmitted bool
}

// beginInlineContext resets the whitespace-collapsing state at the top of an
// inline formatting context.
func (l *layouter) beginInlineContext() { l.wsPending, l.wsEmitted = false, false }

// outOfFlowItem is a queued out-of-flow box plus the approximate static position
// (the normal-flow cursor at the point it was skipped) used to resolve the box's
// auto insets, matching CSS's static-position rule far better than dumping it at
// the containing block origin.
type outOfFlowItem struct {
	node             *dom.Node
	staticX, staticY float64
	hasStatic        bool
}

// bfc is the mutable cursor of a block formatting context: y is the committed
// bottom of the content laid out so far, and carry is the collapsible margin
// still pending below y (not yet materialised).
type bfc struct {
	y     float64
	carry float64
}

// commit materialises the pending margin, advancing y past it, returning new y.
func (b *bfc) commit() float64 {
	b.y += b.carry
	b.carry = 0
	return b.y
}

// collapse combines two adjacent margins per CSS: the larger positive plus the
// smaller (most negative) negative.
func collapse(a, c float64) float64 {
	return math.Max(math.Max(a, 0), math.Max(c, 0)) + math.Min(math.Min(a, 0), math.Min(c, 0))
}

// LayoutDocument lays out the element subtree at root within a viewport of
// width viewportW (CSS pixels), returning the root Box and the total content
// height. imgSize provides intrinsic image dimensions keyed by <img> node; it
// may be nil (images then fall back to width/height attributes).
func LayoutDocument(root *dom.Node, sm css.StyleMap, viewportW float64, m Measurer, imgSize map[*dom.Node][2]float64) (*Box, float64) {
	l := &layouter{sm: sm, m: m, imgSize: imgSize, floats: &floatCtx{}}
	start := firstElement(root)
	if start == nil {
		return &Box{}, 0
	}
	b := &bfc{}
	box := l.place(start, l.sm[start], 0, viewportW, b)
	total := b.commit() // materialise any trailing margin into the page height
	// The page must also cover any float that extends past the flow content.
	if fb := l.floats.bottom(); fb > total {
		total = fb
	}
	// Positioned pass: apply relative offsets, then place out-of-flow
	// (absolute/fixed) boxes against their containing blocks. May grow the page
	// height to cover absolutely-positioned content.
	total = l.positioned(box, viewportW, total)
	return box, total
}

func firstElement(n *dom.Node) *dom.Node {
	if n.Type == dom.Element {
		return n
	}
	for _, c := range n.Children {
		if e := firstElement(c); e != nil {
			return e
		}
	}
	return nil
}

// place lays out one block-level box within a containing block whose content
// origin x is cx and content width cw, advancing the block formatting context b.
func (l *layouter) place(node *dom.Node, st *css.Style, cx, cw float64, b *bfc) *Box {
	if st == nil {
		s := css.Style{Display: css.DisplayBlock, Width: css.Length{Auto: true},
			MinWidth: css.Length{Auto: true}, MaxWidth: css.Length{Auto: true},
			Height: css.Length{Auto: true}}
		st = &s
	}
	box := &Box{Node: node, Style: st}

	bw := st.Border.Widths()
	contentW, ml, mr := resolveWidths(st, cw)
	_ = mr
	boxLeft := cx + ml
	contentX := boxLeft + bw.Left + st.Padding.Left

	b.carry = collapse(b.carry, st.Margin.Top)

	topSep := bw.Top + st.Padding.Top
	botSep := bw.Bottom + st.Padding.Bottom
	establishes := st.Display == css.DisplayFlex || st.Display == css.DisplayTable ||
		st.Display == css.DisplayGrid

	var borderTopY, contentTopY float64
	sep := topSep > 0 || establishes
	if sep {
		borderTopY = b.commit()
		contentTopY = borderTopY + topSep
		b.y = contentTopY
	} else {
		contentTopY = b.y // provisional; carry still pending, corrected below
	}

	contentBottom := l.contents(box, node, st, contentX, contentW, contentTopY, b, sep)

	if h, ok := usedHeight(st, bw, cw); ok {
		// An explicit height fixes the content box height; taller content
		// overflows (overflow:visible) rather than growing the box.
		contentBottom = contentTopY + h
		b.y = contentBottom
	}

	if !sep {
		borderTopY = firstContentTop(box, contentTopY)
	}

	if botSep > 0 || establishes {
		b.commit()
		b.y += botSep
	}
	borderBottomY := b.y
	if !sep && borderBottomY < borderTopY {
		borderBottomY = borderTopY
	}

	b.carry = collapse(b.carry, st.Margin.Bottom)

	box.X = boxLeft
	box.Y = borderTopY
	box.W = bw.Left + st.Padding.Left + contentW + st.Padding.Right + bw.Right
	// borderBottomY >= borderTopY always (sep boxes grow downward; non-sep boxes
	// are clamped above), so the height is non-negative.
	box.H = borderBottomY - borderTopY
	box.ContentX = contentX
	box.ContentY = contentTopY
	box.ContentW = contentW
	box.ContentH = contentBottom - contentTopY
	if box.ContentH < 0 {
		box.ContentH = 0
	}
	box.Float = st.Float
	box.Position = st.Position
	return box
}

// firstContentTop returns the top y of the first placed content (the border-top
// of a box with no top border/padding), or the fallback when the box is empty.
func firstContentTop(box *Box, fallback float64) float64 {
	if len(box.Children) > 0 {
		return box.Children[0].Y
	}
	if len(box.Lines) > 0 {
		return box.Lines[0].Y
	}
	return fallback
}

// contents lays out a box's children, dispatching flex and table containers to
// their own algorithms; otherwise an inline formatting context (lines) when it
// has no block-level children, else a sequence of block boxes with anonymous
// inline boxes between runs of inline content. Returns the content bottom y.
func (l *layouter) contents(box *Box, node *dom.Node, st *css.Style, cx, cw, top float64, b *bfc, sep bool) float64 {
	// A replaced element (img / inline svg) renders at its intrinsic size as an
	// atomic box, whatever its `display` value — an <svg display:flex> is still an
	// image, not a flex container over its SVG primitives. This check therefore
	// precedes the flex/grid/table display dispatch.
	if node.Type == dom.Element && isReplacedTag(node.Tag) {
		if w, h := l.imageSize(node); w > 0 && h > 0 {
			b.commit()
			item := &InlineItem{Image: node, Style: st, ImgW: w, ImgH: h,
				Width: w, Ascent: h, LineHeight: h, X: cx, Y: b.y}
			box.Lines = []*LineBox{{X: cx, Y: b.y, W: cw, H: h, Items: []*InlineItem{item}}}
			b.y += h
			return b.y
		}
	}

	// A form control (input/button/select/textarea) is likewise an atomic
	// box — real content, not a container laid out from its DOM children
	// (an <option>'s text is never itself rendered inline; a <button>'s
	// children become its LABEL, not child boxes) — sized explicitly since
	// unlike an image it has no intrinsic bitmap to measure. A hidden input
	// takes no box at all, matching real UA behavior.
	if node.Type == dom.Element && isFormControlTag(node.Tag) {
		if node.Tag == "input" && strings.EqualFold(node.Attr["type"], "hidden") {
			return b.y
		}
		w, h := l.formControlSize(node, st, cw)
		b.commit()
		item := &InlineItem{Node: node, FormControl: node, Style: st,
			Width: w, Ascent: h, LineHeight: h, X: cx, Y: b.y}
		box.Lines = []*LineBox{{X: cx, Y: b.y, W: cw, H: h, Items: []*InlineItem{item}}}
		b.y += h
		return b.y
	}

	switch st.Display {
	case css.DisplayFlex:
		bottom := l.flex(box, node, st, cx, cw, top, b)
		b.y = bottom // flex is out-of-band; advance the block cursor to its bottom
		return bottom
	case css.DisplayGrid:
		bottom := l.grid(box, node, st, cx, cw, top, b)
		b.y = bottom
		return bottom
	case css.DisplayTable:
		bottom := l.table(box, node, st, cx, cw, top, b)
		b.y = bottom
		return bottom
	}

	pre := st.WhiteSpace == css.WSPre
	if !l.hasBlockLevelChild(node) {
		b.commit()
		items := l.collectInline(node, st, pre)
		lines, bottom := l.layoutInline(items, st, cx, cw, b.y, pre)
		box.Lines = lines
		b.y = bottom
		return bottom
	}

	// List-item counter for this block's direct list-item children. It seeds from
	// an <ol start> and honours a per-item <li value>; nested lists get a fresh
	// counter because each list container runs its own contents() pass.
	counter := listStart(node)

	var run []*dom.Node
	flush := func() {
		if len(run) == 0 {
			return
		}
		items := l.collectInlineFrom(run, st, pre)
		run = nil
		if len(items) == 0 {
			return
		}
		b.commit()
		anonTop := b.y
		lines, bottom := l.layoutInline(items, st, cx, cw, anonTop, pre)
		anon := &Box{Anonymous: true, Style: st, ContentX: cx, ContentY: anonTop, ContentW: cw}
		anon.Lines = lines
		anon.X, anon.Y, anon.W, anon.H = cx, anonTop, cw, bottom-anonTop
		box.Children = append(box.Children, anon)
		b.y = bottom
	}

	for _, c := range renderedChildren(node) {
		switch {
		case c.Type == dom.Text:
			if strings.TrimSpace(c.Text) == "" {
				continue
			}
			run = append(run, c)
		case c.Type == dom.Element:
			cs := l.sm[c]
			if cs != nil && cs.Display == css.DisplayNone {
				continue
			}
			if cs != nil && cs.Position.OutOfFlow() {
				// Out of flow: reserve no space here, place later. Do not flush the
				// pending inline run — an out-of-flow box does not break the line.
				// Record the current flow cursor as the box's approximate static
				// position (used when its insets are auto).
				l.outOfFlow = append(l.outOfFlow, outOfFlowItem{
					node: c, staticX: cx, staticY: b.y + math.Max(b.carry, 0), hasStatic: true,
				})
				continue
			}
			if cs != nil && cs.Float != css.FloatNone {
				flush()
				l.placeFloat(box, c, cs, cx, cw, b)
				continue
			}
			if cs != nil && isBlockLevel(cs.Display) {
				flush()
				l.handleClear(cs, b)
				child := l.place(c, cs, cx, cw, b)
				box.Children = append(box.Children, child)
				if cs.ListItem {
					if val, ok := attrInt(c, "value"); ok {
						counter = val
					}
					l.attachMarker(child, cs, counter)
					counter++
				}
			} else {
				run = append(run, c)
			}
		}
	}
	flush()
	return b.y
}

// handleClear moves the cursor below the relevant floats for a clear value.
func (l *layouter) handleClear(st *css.Style, b *bfc) {
	if st.Clear == css.ClearNone {
		return
	}
	y := b.y + b.carry
	if cy := l.floats.clearY(st.Clear, y); cy > y {
		b.commit()
		b.y = cy
	}
}

// isBlockLevel reports whether a display value generates a block-level box in
// the parent's flow (breaking the line).
func isBlockLevel(d css.Display) bool {
	switch d {
	case css.DisplayBlock, css.DisplayFlex, css.DisplayGrid, css.DisplayTable,
		css.DisplayTableRowGroup, css.DisplayTableRow, css.DisplayTableCell:
		return true
	}
	return false
}

func (l *layouter) hasBlockLevelChild(node *dom.Node) bool {
	for _, c := range renderedChildren(node) {
		if c.Type != dom.Element {
			continue
		}
		cs := l.sm[c]
		if cs == nil {
			continue
		}
		if cs.Position.OutOfFlow() {
			continue // out-of-flow children do not establish a block context
		}
		if cs.Float != css.FloatNone || isBlockLevel(cs.Display) {
			return true
		}
	}
	return false
}

// resolveWidths computes the used content width and left/right margins of a
// block box in its containing block of content width cw, honouring width,
// min/max-width, box-sizing and auto-margin centring (CSS 10.3.3).
func resolveWidths(st *css.Style, cw float64) (contentW, ml, mr float64) {
	bw := st.Border.Widths()
	extra := bw.Left + bw.Right + st.Padding.Left + st.Padding.Right
	mlFixed, mrFixed := st.Margin.Left, st.Margin.Right
	if st.MarginLeftAuto {
		mlFixed = 0
	}
	if st.MarginRightAuto {
		mrFixed = 0
	}

	widthAuto := st.Width.Auto
	if widthAuto {
		contentW = cw - mlFixed - mrFixed - extra
	} else {
		contentW = st.Width.Resolve(cw)
		if st.BoxSizing == css.BorderBox {
			contentW -= extra
		}
	}

	clamped := clampWidth(contentW, st, cw, extra)
	fixed := !widthAuto || clamped != contentW
	contentW = clamped
	if contentW < 0 {
		contentW = 0
	}

	if !fixed {
		return contentW, mlFixed, mrFixed
	}
	leftover := cw - contentW - extra - mlFixed - mrFixed
	switch {
	case st.MarginLeftAuto && st.MarginRightAuto:
		if leftover > 0 {
			ml = leftover / 2
			mr = leftover - ml
		}
	case st.MarginLeftAuto:
		ml, mr = leftover, mrFixed
	case st.MarginRightAuto:
		ml, mr = mlFixed, leftover
	case st.TextAlign == css.AlignCenterBlocks && mlFixed == 0 && mrFixed == 0:
		// Legacy <center> / align="center": centre a definite-width block within
		// its container when no explicit margins constrain it.
		if leftover > 0 {
			ml = leftover / 2
			mr = leftover - ml
		}
	default:
		ml, mr = mlFixed, mrFixed+leftover
	}
	return contentW, ml, mr
}

// clampWidth applies min-width/max-width (box-sizing aware) to a content width.
func clampWidth(contentW float64, st *css.Style, cw, extra float64) float64 {
	if v, ok := widthBound(st.MaxWidth, cw, extra, st.BoxSizing); ok && contentW > v {
		contentW = v
	}
	if v, ok := widthBound(st.MinWidth, cw, extra, st.BoxSizing); ok && contentW < v {
		contentW = v
	}
	return contentW
}

func widthBound(l css.Length, cw, extra float64, bs css.BoxSizing) (float64, bool) {
	if l.Auto {
		return 0, false
	}
	v := l.Resolve(cw)
	if bs == css.BorderBox {
		v -= extra
	}
	if v < 0 {
		v = 0
	}
	return v, true
}

// usedHeight returns an explicit content height when height is set (box-sizing
// aware), else (0,false). Percentage heights are skipped (no definite basis).
func usedHeight(st *css.Style, bw css.Edges, cw float64) (float64, bool) {
	if st.Height.Auto || st.Height.IsPercent {
		return 0, false
	}
	h := st.Height.Px
	if st.BoxSizing == css.BorderBox {
		h -= bw.Top + bw.Bottom + st.Padding.Top + st.Padding.Bottom
	}
	if h < 0 {
		h = 0
	}
	return h, true
}

// ---- inline collection -----------------------------------------------------

func (l *layouter) collectInline(node *dom.Node, st *css.Style, pre bool) []*InlineItem {
	var items []*InlineItem
	l.beginInlineContext()
	l.appendInline(node, st, &items, pre)
	return items
}

func (l *layouter) collectInlineFrom(nodes []*dom.Node, st *css.Style, pre bool) []*InlineItem {
	var items []*InlineItem
	l.beginInlineContext()
	for _, n := range nodes {
		if n.Type == dom.Text {
			l.appendWords(n.Text, st, &items, pre, n.Parent)
		} else {
			cs := l.sm[n]
			if cs == nil {
				cs = st
			}
			l.appendElementInline(n, cs, &items, pre)
		}
	}
	return items
}

func (l *layouter) appendInline(node *dom.Node, st *css.Style, items *[]*InlineItem, pre bool) {
	for _, c := range renderedChildren(node) {
		if c.Type == dom.Text {
			l.appendWords(c.Text, st, items, pre, node)
			continue
		}
		cs := l.sm[c]
		if cs == nil {
			cs = st
		}
		l.appendElementInline(c, cs, items, pre)
	}
}

func (l *layouter) appendElementInline(el *dom.Node, cs *css.Style, items *[]*InlineItem, pre bool) {
	if cs.Display == css.DisplayNone {
		return
	}
	if es := l.sm[el]; es != nil && es.Position.OutOfFlow() {
		// Out-of-flow inline-level box: contributes no inline item, placed later.
		// No simple flow cursor here, so its static position falls back to the
		// containing block origin.
		l.outOfFlow = append(l.outOfFlow, outOfFlowItem{node: el})
		return
	}
	switch el.Tag {
	case "br":
		*items = append(*items, &InlineItem{LineBreak: true, Style: cs, Node: el})
		// A forced break starts a new line: leading whitespace after it collapses.
		l.wsPending, l.wsEmitted = false, false
	case "img", "svg":
		w, h := l.imageSize(el)
		if w > 0 && h > 0 {
			sb := 0.0
			if l.wsEmitted && l.wsPending {
				sb = l.m.Measure(" ", cs.FontFamily, cs.FontSize, cs.FontWeight, cs.Italic)
			}
			*items = append(*items, &InlineItem{
				Style: cs, Image: el, Node: el, ImgW: w, ImgH: h,
				Width: w, Ascent: h, LineHeight: h,
				SpaceBefore: sb,
			})
			l.wsEmitted, l.wsPending = true, false
		}
	default:
		// A form control defaults to display:inline (see css/ua.go) and so is
		// laid out HERE, as a child of whatever block contains it — the
		// common case (an <input> inside a <div>/<label>/<form>). It is
		// still an atomic box like img/svg, just sized differently (no
		// intrinsic bitmap — formControlSize resolves explicit CSS or a
		// UA-shaped default; cw is unavailable in this inline-collection
		// context so a percentage width/height degrades to the default
		// rather than resolving against the containing block, a known,
		// narrow limitation). The contents() branch in this file covers
		// the far rarer case of a control given `display:block` (or
		// similar) by author CSS, which routes through the normal
		// block-box path instead of here.
		if isFormControlTag(el.Tag) {
			if el.Tag == "input" && strings.EqualFold(el.Attr["type"], "hidden") {
				return
			}
			w, h := l.formControlSize(el, cs, 0)
			sb := 0.0
			if l.wsEmitted && l.wsPending {
				sb = l.m.Measure(" ", cs.FontFamily, cs.FontSize, cs.FontWeight, cs.Italic)
			}
			*items = append(*items, &InlineItem{
				Style: cs, FormControl: el, Node: el,
				Width: w, Ascent: h, LineHeight: h,
				SpaceBefore: sb,
			})
			l.wsEmitted, l.wsPending = true, false
			return
		}
		l.appendInline(el, cs, items, pre || cs.WhiteSpace == css.WSPre)
	}
}

func (l *layouter) appendWords(text string, st *css.Style, items *[]*InlineItem, pre bool, origin *dom.Node) {
	asc, lh := l.lineMetricsFor(st)
	if pre {
		for i, seg := range strings.Split(text, "\n") {
			if i > 0 {
				*items = append(*items, &InlineItem{LineBreak: true, Style: st, Node: origin})
			}
			if seg == "" {
				continue
			}
			*items = append(*items, &InlineItem{
				Text:       seg,
				Style:      st,
				Node:       origin,
				Width:      l.m.Measure(seg, st.FontFamily, st.FontSize, st.FontWeight, st.Italic),
				Ascent:     asc,
				LineHeight: lh,
			})
		}
		return
	}
	space := l.m.Measure(" ", st.FontFamily, st.FontSize, st.FontWeight, st.Italic)
	words := strings.Fields(text)
	if len(words) == 0 {
		// A whitespace-only (or empty) text node between inline content carries a
		// single collapsible space to the next word — unless nothing has been
		// emitted yet, in which case leading whitespace collapses to nothing.
		if strings.TrimSpace(text) == "" && text != "" {
			l.wsPending = true
		}
		return
	}
	// Leading whitespace of this run is a collapsible space before its first word.
	if isSpace(rune(text[0])) {
		l.wsPending = true
	}
	for i, w := range words {
		sb := 0.0
		if i > 0 {
			sb = space // whitespace between words within a run collapses to one space
		} else if l.wsEmitted && l.wsPending {
			sb = space // a boundary space — but never a leading indent on the first item
		}
		*items = append(*items, &InlineItem{
			Text:        w,
			Style:       st,
			Node:        origin,
			Width:       l.m.Measure(w, st.FontFamily, st.FontSize, st.FontWeight, st.Italic),
			SpaceBefore: sb,
			Ascent:      asc,
			LineHeight:  lh,
		})
		l.wsEmitted = true
		l.wsPending = false
	}
	// Trailing whitespace defers a space to whatever inline content comes next.
	if isSpace(rune(text[len(text)-1])) {
		l.wsPending = true
	}
}

// isSpace reports whether r is ASCII whitespace subject to CSS whitespace
// collapsing (space, tab, newline, carriage return, form feed).
func isSpace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\f'
}

// lineMetricsFor returns the ascent and line height for a style, honouring an
// explicit line-height (which sets the line box height, centring the font's
// natural height within it).
func (l *layouter) lineMetricsFor(st *css.Style) (ascent, lineHeight float64) {
	asc, fh := l.m.Metrics(st.FontFamily, st.FontSize, st.FontWeight, st.Italic)
	lh, ok := st.LineHeight.Resolve(st.FontSize)
	if !ok {
		return asc, fh
	}
	// Distribute the extra (or negative) leading equally above and below the
	// font's natural box (CSS half-leading), so the baseline stays centred.
	return asc + (lh-fh)/2, lh
}

// isReplacedTag reports whether an element is a replaced box laid out at an
// intrinsic size (a raster/SVG <img>, or an inline <svg> rasterised upstream).
func isReplacedTag(tag string) bool {
	return tag == "img" || tag == "svg"
}

// isFormControlTag reports whether an element is a form control laid out as
// its own atomic box (see the contents() branch above) rather than through
// its DOM children.
func isFormControlTag(tag string) bool {
	switch tag {
	case "input", "button", "select", "textarea":
		return true
	}
	return false
}

// formControlSize resolves a form control's used box size: an explicit
// non-auto CSS width/height first (percentages resolved against cw, the
// same containing-block basis an ordinary block uses), else a UA default
// sized to its kind — close enough to a real browser's own defaults for the
// controls a login-shaped form actually uses. A button-like control (an
// <input type=button/submit/reset>, or a <button>) sizes to fit its own
// label text plus padding, the same way a real browser's default
// (intrinsic, content-sized) button does.
func (l *layouter) formControlSize(node *dom.Node, st *css.Style, cw float64) (w, h float64) {
	if !st.Width.Auto {
		w = st.Width.Resolve(cw)
	}
	if !st.Height.Auto {
		h = st.Height.Resolve(cw)
	}
	if w > 0 && h > 0 {
		return w, h
	}
	dw, dh := l.formControlDefaultSize(node, st)
	if w <= 0 {
		w = dw
	}
	if h <= 0 {
		h = dh
	}
	return w, h
}

// formControlPadX/Y are the button-like controls' label padding (matching
// typical UA default button padding closely enough to look intentional).
const formControlPadX, formControlPadY = 12.0, 6.0

// formControlDefaultSize is called only for a tag isFormControlTag already
// accepted (input/button/select/textarea), so its outer switch's every real
// case is covered by construction; w/h are named returns assigned by
// whichever case matches and returned once at the bottom, rather than each
// case returning directly, so that one line — not an unreachable trailing
// fallback — is what a coverage tool sees every call flow through.
func (l *layouter) formControlDefaultSize(node *dom.Node, st *css.Style) (w, h float64) {
	textHeight := st.FontSize + 10 // ~ real UA text-input default height at common font sizes
	switch node.Tag {
	case "input":
		switch strings.ToLower(node.Attr["type"]) {
		case "checkbox", "radio":
			w, h = 13, 13
		case "button", "submit", "reset":
			w, h = l.buttonSize(controlLabel(node), st)
		default: // text, email, password, search, tel, url, number, date, …
			w, h = 170, textHeight
		}
	case "button":
		label := dom.TextContent(node)
		if label == "" {
			label = "Submit"
		}
		w, h = l.buttonSize(label, st)
	case "select":
		w, h = 170, textHeight
	case "textarea":
		w, h = 200, 60
	}
	return w, h
}

// buttonSize sizes a button-like control to fit label at st's font, plus
// padding — an intrinsic, content-sized box, matching how a real browser's
// unstyled <button>/submit input sizes itself (unlike a plain text input,
// which gets a fixed UA default width regardless of content).
func (l *layouter) buttonSize(label string, st *css.Style) (float64, float64) {
	tw := l.m.Measure(label, st.FontFamily, st.FontSize, st.FontWeight, st.Italic)
	return tw + 2*formControlPadX, st.FontSize + 2*formControlPadY
}

// controlLabel returns an <input type=button/submit/reset>'s visible label:
// its value attribute if set, else the type-appropriate UA default text.
func controlLabel(n *dom.Node) string {
	if v, ok := n.Attribute("value"); ok && v != "" {
		return v
	}
	switch strings.ToLower(n.Attr["type"]) {
	case "reset":
		return "Reset"
	default: // submit and button both default to "Submit" in every major UA
		return "Submit"
	}
}

func (l *layouter) imageSize(el *dom.Node) (float64, float64) {
	if l.imgSize != nil {
		if wh, ok := l.imgSize[el]; ok {
			return wh[0], wh[1]
		}
	}
	return attrFloat(el, "width"), attrFloat(el, "height")
}

func attrFloat(el *dom.Node, name string) float64 {
	v, ok := el.Attribute(name)
	if !ok {
		return 0
	}
	v = strings.TrimSuffix(strings.TrimSpace(v), "px")
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0
	}
	return f
}

// layoutInline breaks items into lines and positions them, honouring text-align
// and any floats intruding into each line's vertical band. cx/cw are the content
// origin x and width; y is the top of the first line. Returns lines and the
// bottom y after the last line.
func (l *layouter) layoutInline(items []*InlineItem, st *css.Style, cx, cw, y float64, pre bool) ([]*LineBox, float64) {
	fbAsc, fbH := l.lineMetricsFor(st)
	if pre {
		lines := WrapItems(items, math.MaxFloat32)
		cursor := y
		for _, line := range lines {
			lineH, baseline, used := lineMetrics(line, fbH, fbAsc)
			x := cx + alignOffset(st.TextAlign, cw, used)
			for i, it := range line.Items {
				if i > 0 {
					x += it.SpaceBefore
				}
				it.X, it.Y = x, cursor+(baseline-it.Ascent)
				x += it.Width
			}
			line.X, line.Y, line.W, line.H = cx, cursor, cw, lineH
			cursor += lineH
		}
		return lines, cursor
	}

	var lines []*LineBox
	cursor := y
	rest := items
	guardH := math.Max(fbH, 1)
	for len(rest) > 0 {
		left, right := l.floats.available(cursor, cursor+guardH, cx, cx+cw)
		avail := right - left // available() guarantees right >= left
		line, consumed, brokeAtEnd := wrapOneLine(rest, avail)
		if consumed == 0 {
			if ny := l.floats.nextEdge(cursor, cx, cx+cw); ny > cursor {
				cursor = ny
				continue
			}
			line, consumed = forceOne(rest)
			brokeAtEnd = false
		}
		rest = rest[consumed:]
		lineH, baseline, used := lineMetrics(line, fbH, fbAsc)
		x := alignOffsetIn(st.TextAlign, left, right, used)
		for i, it := range line.Items {
			if i > 0 {
				x += it.SpaceBefore
			}
			it.X, it.Y = x, cursor+(baseline-it.Ascent)
			x += it.Width
		}
		line.X, line.Y, line.W, line.H = left, cursor, right-left, lineH
		lines = append(lines, line)
		cursor += lineH
		if brokeAtEnd && len(rest) == 0 {
			// A trailing forced break opens a final empty line.
			left2, right2 := l.floats.available(cursor, cursor+guardH, cx, cx+cw)
			lines = append(lines, &LineBox{X: left2, Y: cursor, W: right2 - left2, H: fbH})
			cursor += fbH
		}
	}
	// A truly empty inline box (no items) yields no lines and stays zero-height;
	// any items always produce at least one line above.
	return lines, cursor
}

// wrapOneLine greedily fills one line from items within maxW, stopping at a
// forced break. Returns the line and how many items it consumed (incl. a
// consumed LineBreak). A leading item wider than maxW is not taken (consumed==0)
// so the caller can try to drop past a float first.
func wrapOneLine(items []*InlineItem, maxW float64) (line *LineBox, consumed int, brokeAtEnd bool) {
	line = &LineBox{}
	w := 0.0
	i := 0
	for i < len(items) {
		it := items[i]
		if it.LineBreak {
			i++
			return line, i, true
		}
		add := it.Width
		if len(line.Items) > 0 {
			add += it.SpaceBefore
		}
		if len(line.Items) > 0 && w+add > maxW {
			return line, i, false
		}
		if len(line.Items) == 0 && it.Width > maxW {
			return line, i, false
		}
		line.Items = append(line.Items, it)
		w += add
		i++
	}
	return line, i, false
}

// forceOne places exactly the first non-break item (overflowing) on a line.
func forceOne(items []*InlineItem) (*LineBox, int) {
	line := &LineBox{}
	i := 0
	for i < len(items) && items[i].LineBreak {
		i++
	}
	if i < len(items) {
		line.Items = append(line.Items, items[i])
		i++
	}
	return line, i
}

// lineMetrics computes a line box's height, common baseline offset and used
// inline width. All items share one baseline at the tallest ascent; the line
// box must then be tall enough to hold BOTH the tallest ascent above the
// baseline AND the deepest descent below it. Taking these two maxima
// independently (rather than max(item.LineHeight)) is what keeps a line with
// mixed font sizes / line-heights from letting a tall inline's glyphs spill
// into the next line: each item spans [baseline-ascent, baseline+(lineHeight-
// ascent)], so the line height baseline+maxBelow bounds every item exactly and
// successive lines never overlap.
func lineMetrics(line *LineBox, fbH, fbAsc float64) (lineH, baseline, used float64) {
	if len(line.Items) == 0 {
		return fbH, fbAsc, 0
	}
	var maxBelow float64
	for i, it := range line.Items {
		if it.Ascent > baseline {
			baseline = it.Ascent
		}
		if below := it.LineHeight - it.Ascent; below > maxBelow {
			maxBelow = below
		}
		if i > 0 {
			used += it.SpaceBefore
		}
		used += it.Width
	}
	return baseline + maxBelow, baseline, used
}

func alignOffset(a css.TextAlign, cw, used float64) float64 {
	switch a {
	case css.AlignCenter, css.AlignCenterBlocks:
		if cw > used {
			return (cw - used) / 2
		}
	case css.AlignRight:
		if cw > used {
			return cw - used
		}
	}
	return 0
}

// alignOffsetIn returns the absolute x where a line of width used starts within
// [left,right] for a given alignment.
func alignOffsetIn(a css.TextAlign, left, right, used float64) float64 {
	switch a {
	case css.AlignCenter, css.AlignCenterBlocks:
		if right-left > used {
			return left + (right-left-used)/2
		}
	case css.AlignRight:
		if right-left > used {
			return right - used
		}
	}
	return left
}
