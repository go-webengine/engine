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
}

// LayoutDocument lays out the element subtree at root within a viewport of
// width viewportW (CSS pixels), returning the root Box and the total content
// height. imgSize provides intrinsic image dimensions keyed by <img> node; it
// may be nil (images then fall back to width/height attributes).
func LayoutDocument(root *dom.Node, sm css.StyleMap, viewportW float64, m Measurer, imgSize map[*dom.Node][2]float64) (*Box, float64) {
	l := &layouter{sm: sm, m: m, imgSize: imgSize}
	// Find the outermost block element to lay out (html, else body, else the
	// first element child of the document).
	start := firstElement(root)
	if start == nil {
		return &Box{}, 0
	}
	box, bottom := l.block(start, l.sm[start], 0, viewportW, 0)
	return box, bottom
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

// block lays out a block box within a containing block whose content edge is at
// x-origin cx and content width cw, starting at vertical cursor y. It returns
// the box and the y after the box (including its bottom margin).
func (l *layouter) block(node *dom.Node, st *css.Style, cx, cw, y float64) (*Box, float64) {
	if st == nil {
		s := css.Style{Display: css.DisplayBlock, Width: css.Length{Auto: true}}
		st = &s
	}
	box := &Box{Node: node, Style: st}

	var contentW float64
	if st.Width.Auto {
		contentW = cw - st.Margin.Left - st.Margin.Right - st.Padding.Left - st.Padding.Right
	} else {
		contentW = st.Width.Resolve(cw)
	}
	if contentW < 0 {
		contentW = 0
	}

	boxX := cx + st.Margin.Left
	boxY := y + st.Margin.Top
	contentX := boxX + st.Padding.Left
	contentY := boxY + st.Padding.Top

	box.ContentX, box.ContentY, box.ContentW = contentX, contentY, contentW

	contentBottom := l.children(node, st, box, contentX, contentW, contentY)
	contentH := contentBottom - contentY
	if contentH < 0 {
		contentH = 0
	}

	box.X, box.Y = boxX, boxY
	box.W = st.Padding.Left + contentW + st.Padding.Right
	box.H = st.Padding.Top + contentH + st.Padding.Bottom

	return box, box.Y + box.H + st.Margin.Bottom
}

// children lays out a block's children, generating anonymous inline boxes for
// runs of inline content between block children. It returns the content bottom.
func (l *layouter) children(node *dom.Node, st *css.Style, box *Box, cx, cw, y float64) float64 {
	pre := st.WhiteSpace == css.WSPre
	if !l.hasBlockChild(node) {
		// Pure inline formatting context: lay text directly into this box.
		items := l.collectInline(node, st, pre)
		lines, bottom := l.layoutInline(items, st, cx, cw, y, pre)
		box.Lines = lines
		return bottom
	}

	cursor := y
	var run []*dom.Node
	flush := func() {
		if len(run) == 0 {
			return
		}
		items := l.collectInlineFrom(run, st, pre)
		if len(items) > 0 {
			anon := &Box{Anonymous: true, Style: st, ContentX: cx, ContentY: cursor, ContentW: cw}
			lines, bottom := l.layoutInline(items, st, cx, cw, cursor, pre)
			anon.Lines = lines
			anon.X, anon.Y, anon.W, anon.H = cx, cursor, cw, bottom-cursor
			box.Children = append(box.Children, anon)
			cursor = bottom
		}
		run = nil
	}

	for _, c := range node.Children {
		switch {
		case c.Type == dom.Text:
			if strings.TrimSpace(c.Text) == "" {
				continue // collapsible whitespace between blocks
			}
			run = append(run, c)
		case c.Type == dom.Element:
			cs := l.sm[c]
			if cs != nil && cs.Display == css.DisplayNone {
				continue
			}
			if cs != nil && cs.Display == css.DisplayBlock {
				flush()
				child, bottom := l.block(c, cs, cx, cw, cursor)
				box.Children = append(box.Children, child)
				cursor = bottom
			} else {
				run = append(run, c)
			}
		}
	}
	flush()
	return cursor
}

func (l *layouter) hasBlockChild(node *dom.Node) bool {
	for _, c := range node.Children {
		if c.Type == dom.Element {
			if cs := l.sm[c]; cs != nil && cs.Display == css.DisplayBlock {
				return true
			}
		}
	}
	return false
}

// collectInline gathers inline items from a node's descendants.
func (l *layouter) collectInline(node *dom.Node, st *css.Style, pre bool) []*InlineItem {
	var items []*InlineItem
	l.appendInline(node, st, &items, pre)
	return items
}

// collectInlineFrom gathers inline items from a slice of sibling nodes.
func (l *layouter) collectInlineFrom(nodes []*dom.Node, st *css.Style, pre bool) []*InlineItem {
	var items []*InlineItem
	for _, n := range nodes {
		if n.Type == dom.Text {
			l.appendWords(n.Text, st, &items, pre)
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
			l.appendWords(c.Text, st, items, pre)
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
		*items = append(*items, &InlineItem{LineBreak: true, Style: cs})
	case "img":
		w, h := l.imageSize(el)
		if w > 0 && h > 0 {
			asc := h
			*items = append(*items, &InlineItem{
				Style: cs, Image: el, ImgW: w, ImgH: h,
				Width: w, Ascent: asc, LineHeight: h,
				SpaceBefore: l.m.Measure(" ", cs.FontFamily, cs.FontSize, cs.FontWeight),
			})
		}
	default:
		l.appendInline(el, cs, items, pre || cs.WhiteSpace == css.WSPre)
	}
}

// appendWords tokenises text into inline items. In normal mode runs of
// whitespace collapse and each word becomes an item; in pre mode spaces are
// preserved and each newline becomes a forced break.
func (l *layouter) appendWords(text string, st *css.Style, items *[]*InlineItem, pre bool) {
	asc, lh := l.m.Metrics(st.FontFamily, st.FontSize, st.FontWeight)
	if pre {
		for i, seg := range strings.Split(text, "\n") {
			if i > 0 {
				*items = append(*items, &InlineItem{LineBreak: true, Style: st})
			}
			if seg == "" {
				continue
			}
			*items = append(*items, &InlineItem{
				Text:       seg,
				Style:      st,
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
			Width:       l.m.Measure(w, st.FontFamily, st.FontSize, st.FontWeight),
			SpaceBefore: space,
			Ascent:      asc,
			LineHeight:  lh,
		})
	}
}

func (l *layouter) imageSize(el *dom.Node) (float64, float64) {
	if l.imgSize != nil {
		if wh, ok := l.imgSize[el]; ok {
			return wh[0], wh[1]
		}
	}
	w := attrFloat(el, "width")
	h := attrFloat(el, "height")
	return w, h
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

// layoutInline breaks items into lines and positions them, honouring
// text-align. It returns the lines and the bottom y after the last line.
func (l *layouter) layoutInline(items []*InlineItem, st *css.Style, cx, cw, y float64, pre bool) ([]*LineBox, float64) {
	wrapW := cw
	if pre {
		wrapW = math.MaxFloat32 // pre never soft-wraps; only <br>/\n break lines
	}
	lines := WrapItems(items, wrapW)
	cursor := y
	// Fallback metrics for empty lines (e.g. a trailing <br>).
	fbAsc, fbH := l.m.Metrics(st.FontFamily, st.FontSize, st.FontWeight)
	for _, line := range lines {
		lineH, baseline, used := lineMetrics(line, fbH, fbAsc)
		offset := alignOffset(st.TextAlign, cw, used)
		x := cx + offset
		for i, it := range line.Items {
			if i > 0 {
				x += it.SpaceBefore
			}
			it.X = x
			it.Y = cursor + (baseline - it.Ascent)
			x += it.Width
		}
		line.X, line.Y, line.W, line.H = cx, cursor, cw, lineH
		cursor += lineH
	}
	return lines, cursor
}

// lineMetrics returns the line height, baseline offset and used inline width.
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
