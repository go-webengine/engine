// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package layout

import (
	"strconv"
	"strings"

	"github.com/go-webengine/engine/css"
	"github.com/go-webengine/engine/dom"
)

// attachMarker builds and attaches the outside list-item marker for an
// already-laid-out box (CSS Lists 3 §3.5: list-style-position: outside, the
// initial value): its text ends a small gap to the LEFT of the box's content
// edge, on the first line's baseline. An inside marker is not attached here:
// it is an inline box at the start of the item's content (see
// renderedChildren), laid out — and painted, by every backend — like text.
//
// The marker's text comes from the cascade's counters pass (css.Style.Marker:
// the list-item counter in list-style-type's counter style, or ::marker
// content). ordinal is the fallback for a style map built without that pass
// (a hand-made css.Style), numbered by this list container's own walk.
func (l *layouter) attachMarker(box *Box, st *css.Style, ordinal int) {
	var text string
	glyph := false
	ms := st
	switch {
	case st.Marker != nil:
		if st.Marker.Inside {
			return
		}
		text = strings.TrimRight(st.Marker.Text, "  \t")
		glyph = st.Marker.Glyph
		if st.Marker.Style != nil {
			ms = st.Marker.Style
		}
	case st.ListStyleType == css.ListNone:
		return
	case st.ListStylePosition == css.ListInside && st.ListStyleType != css.ListNone:
		// No counters pass ran; an inside marker still has no outside box.
		return
	case st.ListStyleType == css.ListString:
		text = st.ListStyleString
	default:
		text = css.FormatCounter(clampInt32(ordinal), css.ListStyleTypeName(st.ListStyleType))
		if st.ListStyleType.Glyph() {
			glyph = true
		} else {
			text += "."
		}
	}
	if text == "" {
		return
	}
	em := ms.FontSize
	ascent, _ := l.lineMetricsFor(ms)
	// The marker aligns to the first line of the item's content; an empty item
	// falls back to the top of its content box.
	lineTop := box.ContentY
	if line := firstLineBox(box); line != nil {
		lineTop = line.Y
		// The item's own first-line ascent, so a marker in a different font
		// size than the text still shares its baseline.
		if itemAscent, _ := l.lineMetricsFor(st); itemAscent > ascent {
			lineTop += itemAscent - ascent
		}
	}
	// Gap between the marker and the content edge (a fraction of the font size).
	gap := 0.5 * em

	m := &Marker{Type: st.ListStyleType, Style: ms, Text: text, Ascent: ascent, Glyph: glyph}
	tw := l.m.Measure(text, ms.FontFamily, ms.FontSize, ms.FontWeight, ms.Italic)
	m.TextX = box.ContentX - gap - tw // right-aligned: right edge at contentX-gap
	m.TextY = lineTop
	if glyph {
		// disc / circle / square: a square glyph box ~0.35em on a side, vertically
		// centred near the middle of the first line's lowercase text.
		size := 0.35 * em
		centerY := lineTop + ascent - 0.32*em
		m.W, m.H = size, size
		m.X = box.ContentX - gap - size
		m.Y = centerY - size/2
	} else {
		m.X, m.Y, m.W = m.TextX, m.TextY, tw
	}
	box.Marker = m
}

func clampInt32(n int) int32 {
	if n > 1<<31-1 {
		return 1<<31 - 1
	}
	if n < -1<<31 {
		return -1 << 31
	}
	return int32(n)
}

// firstLineBox returns the first inline line box in a subtree (the box's own
// lines, else the first line found by descending its children in order), or nil
// when the subtree holds no inline content.
func firstLineBox(box *Box) *LineBox {
	if len(box.Lines) > 0 {
		return box.Lines[0]
	}
	for _, ch := range box.Children {
		if lb := firstLineBox(ch); lb != nil {
			return lb
		}
	}
	return nil
}

// listStart returns the starting ordinal for a list container's items: an
// <ol start="N"> seeds N, everything else starts at 1.
func listStart(node *dom.Node) int {
	if node.Type == dom.Element && node.Tag == "ol" {
		if v, ok := attrInt(node, "start"); ok {
			return v
		}
	}
	return 1
}

// attrInt parses an integer-valued attribute, reporting whether it was present
// and well-formed.
func attrInt(n *dom.Node, name string) (int, bool) {
	v, ok := n.Attribute(name)
	if !ok {
		return 0, false
	}
	i, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return 0, false
	}
	return i, true
}
