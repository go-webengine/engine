// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package layout

import (
	"math"
	"sort"

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
	cross   float64 // outer cross size after layout
}

// flexLine is one flex line (a row of items in a row container) with its
// resolved cross size.
type flexLine struct {
	items []*flexItem
	cross float64
}

// flex lays out a flex container. It supports both directions, wrapping
// (flex-wrap), order, gaps, grow/shrink/basis with min/max clamping,
// justify-content on the main axis, align-items/align-self on the cross axis and
// align-content across the flex lines. Returns the content bottom y.
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

// flexItems collects the element children that act as flex items, ordered by the
// CSS order property (stable within equal order, preserving document order).
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
	sort.SliceStable(out, func(i, j int) bool { return out[i].st.Order < out[j].st.Order })
	return out
}

// mainBaseRow returns an item's hypothetical main content size along a row (cw is
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

// clampMainRow clamps a row item's main (width) content size to its
// min-width/max-width, resolved against the container width cw.
func (it *flexItem) clampMainRow(v, cw float64) float64 {
	return clampWidth(v, it.st, cw, it.hEdges)
}

// clampCrossHeight clamps a row item's cross (height) border-box size to its
// min-height/max-height. outer is the item's outer cross size (border box +
// margin); containerH is the definite container height (or 0). At this fidelity
// min/max-height bound the border box directly (box-sizing is not distinguished
// on the cross axis, a documented simplification).
func (it *flexItem) clampCrossHeight(outer, containerH float64) float64 {
	inner := outer - it.vMargin
	if v, ok := heightBound(it.st.MaxHeight, containerH); ok && inner > v {
		inner = v
	}
	if v, ok := heightBound(it.st.MinHeight, containerH); ok && inner < v {
		inner = v
	}
	return inner + it.vMargin
}

// heightBound resolves a min/max-height length to a border-box height bound.
// Percentages resolve against containerH; auto, unset, an unresolved percentage
// or a negative value report false.
func heightBound(l css.Length, containerH float64) (float64, bool) {
	if l.Auto {
		return 0, false
	}
	if l.IsPercent && containerH <= 0 {
		return 0, false
	}
	v := l.Resolve(containerH)
	if v < 0 {
		return 0, false
	}
	return v, true
}

// breakLines partitions items into flex lines. With wrapping off, all items
// share one line. Otherwise a line is filled greedily until the next item's
// outer main size (plus the inter-item main gap) would overflow mainSize; an
// item that overflows an empty line still occupies it alone.
func breakLines(items []*flexItem, mainSize, mainGap float64, wrap bool) [][]*flexItem {
	if !wrap {
		return [][]*flexItem{items}
	}
	var lines [][]*flexItem
	var cur []*flexItem
	var used float64
	for _, it := range items {
		outer := it.base + it.hEdges + it.hMargin
		add := outer
		if len(cur) > 0 {
			add += mainGap
		}
		if len(cur) > 0 && used+add > mainSize+1e-6 {
			lines = append(lines, cur)
			cur = nil
			used = 0
			add = outer
		}
		cur = append(cur, it)
		used += add
	}
	if len(cur) > 0 {
		lines = append(lines, cur)
	}
	return lines
}

func (l *layouter) flexRow(box *Box, items []*flexItem, st *css.Style, cx, cw, top float64) float64 {
	mainGap := gapLen(st.ColumnGap)
	crossGap := gapLen(st.RowGap)
	containerH, crossDefinite := flexCrossHeight(st, cw)

	for _, it := range items {
		it.base = it.clampMainRow(it.mainBaseRow(l, cw), cw)
	}
	lines := breakLines(items, cw, mainGap, st.FlexWrap != css.FlexNoWrap)

	// Resolve flexible lengths per line, then lay each item out to get its cross.
	var totalCross float64
	fls := make([]*flexLine, 0, len(lines))
	for _, line := range lines {
		resolveMainRow(line, cw, mainGap)
		var lineCross float64
		for _, it := range line {
			it.box = l.layoutIsolated(it.node, it.st, it.main)
			it.cross = it.clampCrossHeight(it.box.H+it.vMargin, containerH)
			if it.cross > lineCross {
				lineCross = it.cross
			}
		}
		fls = append(fls, &flexLine{items: line, cross: lineCross})
		totalCross += lineCross
	}
	totalCross += crossGap * float64(len(fls)-1)

	// Cross container size: a definite height wins, else it fits the lines.
	containerCross := totalCross
	if crossDefinite && containerH > totalCross {
		containerCross = containerH
	}
	lineOff, lineGap := alignContentDistribute(st.AlignContent, containerCross-totalCross, len(fls))
	if st.FlexWrap == css.FlexWrapReverse {
		reverseLines(fls)
	}

	cyLine := top + lineOff
	for _, fl := range fls {
		// Main-axis positioning with justify-content over the leftover space.
		var mainUsed float64
		for _, it := range fl.items {
			mainUsed += it.main + it.hEdges + it.hMargin
		}
		mainUsed += mainGap * float64(len(fl.items)-1)
		leftover := math.Max(cw-mainUsed, 0)
		offset, jgap := distribute(st.JustifyContent, leftover, len(fl.items))

		x := cx + offset
		for _, it := range fl.items {
			ai := it.st.AlignSelf.Resolve(st.AlignItems)
			cyOff := crossOffset(ai, fl.cross, it.cross)
			if ai == css.AlignStretch && it.st.Height.Auto {
				stretched := it.clampCrossHeight(fl.cross, containerH)
				it.box.H = stretched - it.vMargin
				it.box.ContentH = it.box.H - it.vEdges
			}
			translateBox(it.box, (x+it.st.Margin.Left)-it.box.X, (cyLine+cyOff+it.st.Margin.Top)-it.box.Y)
			box.Children = append(box.Children, it.box)
			x += it.main + it.hEdges + it.hMargin + mainGap + jgap
		}
		cyLine += fl.cross + crossGap + lineGap
	}
	return top + math.Max(totalCross, containerCross)
}

// resolveMainRow distributes free main space across a line via grow/shrink,
// clamping each item's resolved main size to its min/max-width.
func resolveMainRow(line []*flexItem, cw, mainGap float64) {
	var sumOuter, sumGrow, sumShrink float64
	for _, it := range line {
		it.main = it.base
		sumOuter += it.base + it.hEdges + it.hMargin
		sumGrow += it.st.FlexGrow
		sumShrink += it.st.FlexShrink
	}
	free := cw - sumOuter - mainGap*float64(len(line)-1)
	switch {
	case free > 0 && sumGrow > 0:
		for _, it := range line {
			it.main = it.clampMainRow(it.base+free*it.st.FlexGrow/sumGrow, cw)
		}
	case free < 0 && sumShrink > 0:
		for _, it := range line {
			it.main = it.clampMainRow(math.Max(it.base+free*it.st.FlexShrink/sumShrink, 0), cw)
		}
	}
}

func gapLen(l css.Length) float64 {
	if l.Auto || l.IsPercent {
		return 0 // percentage gaps against an auto container size resolve to 0
	}
	return math.Max(l.Px, 0)
}

// flexCrossHeight returns the flex container's definite cross height (row) and
// whether it is definite. cw is used only for box-sizing.
func flexCrossHeight(st *css.Style, cw float64) (float64, bool) {
	if h, ok := usedHeight(st, st.Border.Widths(), cw); ok {
		return h, true
	}
	return 0, false
}

// alignContentDistribute returns the leading cross offset and extra inter-line
// gap for an align-content value over free cross space (added on top of the base
// row-gap already applied between lines).
func alignContentDistribute(a css.AlignContent, free float64, n int) (offset, extraGap float64) {
	if n <= 0 || free <= 0 {
		return 0, 0
	}
	switch a {
	case css.AlignContentEnd:
		return free, 0
	case css.AlignContentCenter:
		return free / 2, 0
	case css.AlignContentSpaceBetween:
		if n > 1 {
			return 0, free / float64(n-1)
		}
		return 0, 0
	case css.AlignContentSpaceAround:
		g := free / float64(n)
		return g / 2, g
	case css.AlignContentSpaceEvenly:
		g := free / float64(n+1)
		return g, g
	default: // start and stretch pack at the cross-start edge
		return 0, 0
	}
}

func reverseLines(fls []*flexLine) {
	for i, j := 0, len(fls)-1; i < j; i, j = i+1, j-1 {
		fls[i], fls[j] = fls[j], fls[i]
	}
}

func (l *layouter) flexColumn(box *Box, items []*flexItem, st *css.Style, cx, cw, top float64) float64 {
	rowGap := gapLen(st.RowGap) // main-axis (vertical) gap in a column container
	// Cross axis is horizontal. Determine each item's cross (width) and lay it
	// out to get its main (height); stack along the vertical main axis.
	y := top
	for i, it := range items {
		if i > 0 {
			y += rowGap
		}
		crossW := cw - it.hMargin
		ai := it.st.AlignSelf.Resolve(st.AlignItems)
		stretch := ai == css.AlignStretch && it.st.Width.Auto
		if !stretch {
			natural := l.preferredWidth(it.node, it.st) - it.hEdges
			if !it.st.Width.Auto {
				natural = it.st.Width.Resolve(cw)
				if it.st.BoxSizing == css.BorderBox {
					natural -= it.hEdges
				}
			}
			natural = it.clampMainRow(natural, cw)
			crossW = math.Min(math.Max(natural, 0), cw-it.hMargin)
		}
		it.box = l.layoutIsolated(it.node, it.st, crossW)
		outerCrossW := it.box.W + it.hMargin
		cxOff := crossOffset(ai, cw, outerCrossW)
		translateBox(it.box, (cx+cxOff+it.st.Margin.Left)-it.box.X, (y+it.st.Margin.Top)-it.box.Y)
		box.Children = append(box.Children, it.box)
		y += it.box.H + it.vMargin
	}
	return y
}

// layoutIsolated lays out a node as a fixed-content-width block in its own
// (discarded) float context, returning a box positioned at a local origin with
// zero outer margins. Used by flex/grid/table to size and then translate
// children.
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
