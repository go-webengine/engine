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
	establishes := st.Display == css.DisplayFlex || st.Display == css.DisplayTable

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
	switch st.Display {
	case css.DisplayFlex:
		bottom := l.flex(box, node, st, cx, cw, top, b)
		b.y = bottom // flex is out-of-band; advance the block cursor to its bottom
		return bottom
	case css.DisplayTable:
		bottom := l.table(box, node, st, cx, cw, top, b)
		b.y = bottom
		return bottom
	}

	// A replaced element (img) laid out as its own block/float box renders at its
	// intrinsic size rather than establishing an inline context over children.
	if node.Type == dom.Element && node.Tag == "img" {
		if w, h := l.imageSize(node); w > 0 && h > 0 {
			b.commit()
			item := &InlineItem{Image: node, Style: st, ImgW: w, ImgH: h,
				Width: w, Ascent: h, LineHeight: h, X: cx, Y: b.y}
			box.Lines = []*LineBox{{X: cx, Y: b.y, W: cw, H: h, Items: []*InlineItem{item}}}
			b.y += h
			return b.y
		}
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

	for _, c := range node.Children {
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
	case css.DisplayBlock, css.DisplayFlex, css.DisplayTable,
		css.DisplayTableRowGroup, css.DisplayTableRow, css.DisplayTableCell:
		return true
	}
	return false
}

func (l *layouter) hasBlockLevelChild(node *dom.Node) bool {
	for _, c := range node.Children {
		if c.Type != dom.Element {
			continue
		}
		cs := l.sm[c]
		if cs == nil {
			continue
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
	l.appendInline(node, st, &items, pre)
	return items
}

func (l *layouter) collectInlineFrom(nodes []*dom.Node, st *css.Style, pre bool) []*InlineItem {
	var items []*InlineItem
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
	for _, c := range node.Children {
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
	switch el.Tag {
	case "br":
		*items = append(*items, &InlineItem{LineBreak: true, Style: cs, Node: el})
	case "img":
		w, h := l.imageSize(el)
		if w > 0 && h > 0 {
			*items = append(*items, &InlineItem{
				Style: cs, Image: el, Node: el, ImgW: w, ImgH: h,
				Width: w, Ascent: h, LineHeight: h,
				SpaceBefore: l.m.Measure(" ", cs.FontFamily, cs.FontSize, cs.FontWeight),
			})
		}
	default:
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
				Width:      l.m.Measure(seg, st.FontFamily, st.FontSize, st.FontWeight),
				Ascent:     asc,
				LineHeight: lh,
			})
		}
		return
	}
	space := l.m.Measure(" ", st.FontFamily, st.FontSize, st.FontWeight)
	for _, w := range strings.Fields(text) {
		*items = append(*items, &InlineItem{
			Text:        w,
			Style:       st,
			Node:        origin,
			Width:       l.m.Measure(w, st.FontFamily, st.FontSize, st.FontWeight),
			SpaceBefore: space,
			Ascent:      asc,
			LineHeight:  lh,
		})
	}
}

// lineMetricsFor returns the ascent and line height for a style, honouring an
// explicit line-height (which sets the line box height, centring the font's
// natural height within it).
func (l *layouter) lineMetricsFor(st *css.Style) (ascent, lineHeight float64) {
	asc, fh := l.m.Metrics(st.FontFamily, st.FontSize, st.FontWeight)
	if st.LineHeight.Normal || st.LineHeight.Px <= 0 {
		return asc, fh
	}
	lh := st.LineHeight.Px
	return asc + (lh-fh)/2, lh
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

func lineMetrics(line *LineBox, fbH, fbAsc float64) (lineH, baseline, used float64) {
	if len(line.Items) == 0 {
		return fbH, fbAsc, 0
	}
	for i, it := range line.Items {
		if it.LineHeight > lineH {
			lineH = it.LineHeight
		}
		if it.Ascent > baseline {
			baseline = it.Ascent
		}
		if i > 0 {
			used += it.SpaceBefore
		}
		used += it.Width
	}
	return lineH, baseline, used
}

func alignOffset(a css.TextAlign, cw, used float64) float64 {
	switch a {
	case css.AlignCenter:
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
	case css.AlignCenter:
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
