// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

// Package layout is a block-and-inline flow engine. Block boxes stack
// vertically honouring margins, padding and width; inline content (text runs
// and inline elements) is broken into lines within the containing block at a
// given viewport width using measured word advances supplied through a
// Measurer. The geometry is deterministic and, given a fake Measurer, exactly
// testable without any font machinery.
package layout

import (
	"github.com/go-webengine/engine/css"
	"github.com/go-webengine/engine/dom"
)

// Measurer supplies text metrics to the layout engine. The real implementation
// lives in the paint package (backed by go-opentype); tests inject a fake.
type Measurer interface {
	// Measure returns the advance width in pixels of text in the given family,
	// size, weight and style (italic).
	Measure(text string, fam css.FontFamily, sizePx float64, weight int, italic bool) float64
	// Metrics returns the ascent (baseline offset from the top) and the line
	// height in pixels for a family/size/weight/style.
	Metrics(fam css.FontFamily, sizePx float64, weight int, italic bool) (ascent, lineHeight float64)
}

// Box is a laid-out block box. X,Y,W,H describe the border box (which, with no
// borders in Phase 0, equals the padding box that a background fills). Content*
// describe the content box. A box either has block Children or holds inline
// Lines (an anonymous box may hold Lines and carry no Node).
type Box struct {
	Node       *dom.Node
	Style      *css.Style
	X, Y, W, H float64

	ContentX, ContentY, ContentW, ContentH float64

	Children  []*Box
	Lines     []*LineBox
	Anonymous bool

	// Float marks a box taken out of normal flow (float:left/right). Floated
	// boxes are painted like blocks but positioned by the float algorithm.
	Float css.Float

	// Position records the box's CSS position. Relative/sticky boxes remain in
	// flow but are shifted at the end of layout; absolute/fixed boxes are laid
	// out out of flow and appended to the root box (painted after in-flow
	// content) with their coordinates already resolved to document space.
	Position css.Position
}

// InlineItem is one atom of inline content: a word, an image, or a forced line
// break. Positions (X,Y = top-left) are filled during line layout.
type InlineItem struct {
	Text  string
	Style *css.Style

	// Node is the element that directly produced this item — the immediate
	// parent element of a text run, or the <img> element of an image item. It
	// lets a consumer walk up the DOM (e.g. to the nearest <a href> ancestor)
	// without re-deriving document structure from geometry. It is nil only for
	// synthetic items with no originating element (a forced <br> carries its
	// own element; anonymous runs never occur here).
	Node *dom.Node

	Width       float64 // advance of the word / width of the image
	SpaceBefore float64 // width of a space in this item's font
	Ascent      float64
	LineHeight  float64

	Image      *dom.Node // non-nil when this item is an <img>
	ImgW, ImgH float64

	LineBreak bool // a <br>: forces the current line to end

	X, Y float64
}

// LineBox is one line of inline content with its positioned items.
type LineBox struct {
	X, Y, W, H float64
	Items      []*InlineItem
}
