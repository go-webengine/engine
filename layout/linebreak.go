// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package layout

// WrapItems greedily breaks a sequence of inline items into lines so that each
// line's used width does not exceed maxW, inserting a per-item SpaceBefore
// between adjacent items on the same line. A single item wider than maxW is
// placed alone on its own line (overflow). A LineBreak item ends the current
// line (and is not itself placed). This is the pure, exactly-testable core of
// inline layout; positioning and heights are applied by the caller.
func WrapItems(items []*InlineItem, maxW float64) []*LineBox {
	if len(items) == 0 {
		return nil
	}
	var lines []*LineBox
	cur := &LineBox{}
	curW := 0.0
	for _, it := range items {
		if it.LineBreak {
			lines = append(lines, cur)
			cur = &LineBox{}
			curW = 0
			continue
		}
		// padLead/padTrail are the inline element edges (border+padding) this
		// item reserves; they occupy the line exactly like the word's width.
		add := it.padLead + it.Width + it.padTrail
		if len(cur.Items) > 0 {
			add += it.SpaceBefore
		}
		if len(cur.Items) > 0 && curW+add > maxW {
			lines = append(lines, cur)
			cur = &LineBox{}
			curW = 0
			add = it.padLead + it.Width + it.padTrail
		}
		cur.Items = append(cur.Items, it)
		curW += add
	}
	lines = append(lines, cur)
	return lines
}
