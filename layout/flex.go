// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package layout

import (
	"math"

	"github.com/go-webengine/engine/css"
	"github.com/go-webengine/engine/dom"
)

// flexItem is one participant in a flex container's layout.
type flexItem struct {
	node    *dom.Node
	st      *css.Style
	hEdges  float64 // border+padding left+right
	vEdges  float64 // border+padding top+bottom
	hMargin float64 // margin left+right
	vMargin float64 // margin top+bottom
	base    float64 // hypothetical main content size
	main    float64 // resolved main content size
	box     *Box
}

// flex lays out a flex container's children along the main axis with
// grow/shrink/basis, justify-content on the main axis and align-items on the
// cross axis, for the row and column directions. Returns the content bottom y.
func (l *layouter) flex(box *Box, node *dom.Node, st *css.Style, cx, cw, top float64, b *bfc) float64 {
	items := l.flexItems(node)
	if len(items) == 0 {
		return top
	}
	if st.FlexDirection == css.FlexColumn {
		return l.flexColumn(box, items, st, cx, cw, top)
	}
	return l.flexRow(box, items, st, cx, cw, top)
}

// flexItems collects the element children that act as flex items.
func (l *layouter) flexItems(node *dom.Node) []*flexItem {
	var out []*flexItem
	for _, c := range node.Children {
		if c.Type != dom.Element {
			continue
		}
		cs := l.sm[c]
		if cs == nil || cs.Display == css.DisplayNone {
			continue
		}
		bw := cs.Border.Widths()
		out = append(out, &flexItem{
			node:    c,
			st:      cs,
			hEdges:  bw.Left + bw.Right + cs.Padding.Left + cs.Padding.Right,
			vEdges:  bw.Top + bw.Bottom + cs.Padding.Top + cs.Padding.Bottom,
			hMargin: cs.Margin.Left + cs.Margin.Right,
			vMargin: cs.Margin.Top + cs.Margin.Bottom,
		})
	}
	return out
}

// mainBase returns an item's hypothetical main content size along a row (cw is
// the container content width, used to resolve percentages).
func (it *flexItem) mainBaseRow(l *layouter, cw float64) float64 {
	switch {
	case !it.st.FlexBasis.Auto:
		v := it.st.FlexBasis.Resolve(cw)
		if it.st.BoxSizing == css.BorderBox {
			v -= it.hEdges
		}
		return math.Max(v, 0)
	case !it.st.Width.Auto:
		v := it.st.Width.Resolve(cw)
		if it.st.BoxSizing == css.BorderBox {
			v -= it.hEdges
		}
		return math.Max(v, 0)
	default:
		return math.Max(l.preferredWidth(it.node, it.st)-it.hEdges, 0)
	}
}

func (l *layouter) flexRow(box *Box, items []*flexItem, st *css.Style, cx, cw, top float64) float64 {
	var sumOuter, sumGrow, sumShrink float64
	for _, it := range items {
		it.base = it.mainBaseRow(l, cw)
		it.main = it.base
		sumOuter += it.base + it.hEdges + it.hMargin
		sumGrow += it.st.FlexGrow
		sumShrink += it.st.FlexShrink
	}
	free := cw - sumOuter
	switch {
	case free > 0 && sumGrow > 0:
		for _, it := range items {
			it.main = it.base + free*it.st.FlexGrow/sumGrow
		}
	case free < 0 && sumShrink > 0:
		for _, it := range items {
			it.main = math.Max(it.base+free*it.st.FlexShrink/sumShrink, 0)
		}
	}

	// Lay out each item at its resolved main size to obtain its cross (height).
	var containerCross float64
	for _, it := range items {
		it.box = l.layoutIsolated(it.node, it.st, it.main)
		cross := it.box.H + it.vMargin
		if cross > containerCross {
			containerCross = cross
		}
	}

	// Main-axis positioning with justify-content over any leftover space.
	var totalOuter float64
	for _, it := range items {
		totalOuter += it.main + it.hEdges + it.hMargin
	}
	leftover := math.Max(cw-totalOuter, 0)
	offset, gap := distribute(st.JustifyContent, leftover, len(items))

	x := cx + offset
	for _, it := range items {
		outerCross := it.box.H + it.vMargin
		cy := crossOffset(st.AlignItems, containerCross, outerCross)
		if st.AlignItems == css.AlignStretch && it.st.Height.Auto {
			it.box.H = containerCross - it.vMargin
			it.box.ContentH = it.box.H - it.vEdges
		}
		translateBox(it.box, (x+it.st.Margin.Left)-it.box.X, (top+cy+it.st.Margin.Top)-it.box.Y)
		box.Children = append(box.Children, it.box)
		x += it.main + it.hEdges + it.hMargin + gap
	}
	return top + containerCross
}

func (l *layouter) flexColumn(box *Box, items []*flexItem, st *css.Style, cx, cw, top float64) float64 {
	// Cross axis is horizontal. Determine each item's cross (width) and lay it
	// out to get its main (height); stack along the vertical main axis.
	y := top
	for _, it := range items {
		crossW := cw - it.hMargin
		stretch := st.AlignItems == css.AlignStretch && it.st.Width.Auto
		if !stretch {
			natural := l.preferredWidth(it.node, it.st) - it.hEdges
			if !it.st.Width.Auto {
				natural = it.st.Width.Resolve(cw)
				if it.st.BoxSizing == css.BorderBox {
					natural -= it.hEdges
				}
			}
			crossW = math.Min(math.Max(natural, 0), cw-it.hMargin)
		}
		it.box = l.layoutIsolated(it.node, it.st, crossW)
		outerCrossW := it.box.W + it.hMargin
		cxOff := crossOffset(st.AlignItems, cw, outerCrossW)
		translateBox(it.box, (cx+cxOff+it.st.Margin.Left)-it.box.X, (y+it.st.Margin.Top)-it.box.Y)
		box.Children = append(box.Children, it.box)
		y += it.box.H + it.vMargin
	}
	return y
}

// layoutIsolated lays out a node as a fixed-content-width block in its own
// (discarded) float context, returning a box positioned at a local origin with
// zero outer margins. Used by flex/table to size and then translate children.
func (l *layouter) layoutIsolated(node *dom.Node, st *css.Style, contentW float64) *Box {
	if contentW < 0 {
		contentW = 0
	}
	saved := l.floats
	l.floats = &floatCtx{}
	clone := *st
	clone.Width = css.Length{Px: contentW}
	clone.MinWidth = css.Length{Auto: true}
	clone.MaxWidth = css.Length{Auto: true}
	clone.BoxSizing = css.ContentBox
	clone.Float = css.FloatNone
	clone.Margin = css.Edges{}
	clone.MarginLeftAuto, clone.MarginRightAuto = false, false
	fb := &bfc{}
	box := l.place(node, &clone, 0, contentW, fb)
	fb.commit()
	l.floats = saved
	return box
}

// distribute returns the leading offset and inter-item gap for a justify-content
// value given free space and item count.
func distribute(j css.Justify, free float64, n int) (offset, gap float64) {
	if n <= 0 {
		return 0, 0
	}
	switch j {
	case css.JustifyEnd:
		return free, 0
	case css.JustifyCenter:
		return free / 2, 0
	case css.JustifySpaceBetween:
		if n > 1 {
			return 0, free / float64(n-1)
		}
		return 0, 0
	case css.JustifySpaceAround:
		g := free / float64(n)
		return g / 2, g
	case css.JustifySpaceEvenly:
		g := free / float64(n+1)
		return g, g
	default: // JustifyStart
		return 0, 0
	}
}

// crossOffset returns the cross-axis offset of an item's outer box within the
// container's cross size for an align-items value.
func crossOffset(a css.AlignItems, containerCross, outerCross float64) float64 {
	switch a {
	case css.AlignFlexEnd:
		return math.Max(containerCross-outerCross, 0)
	case css.AlignCenterItems:
		return math.Max(containerCross-outerCross, 0) / 2
	default: // stretch or flex-start start at the cross-start edge
		return 0
	}
}
