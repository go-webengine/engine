// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package paint

import (
	"github.com/go-webengine/engine/css"
	"github.com/go-webengine/engine/dom"
)

// Mark says what the drawings that follow stand for, for a canvas that keeps
// a document's logical structure: a tagged PDF, which a screen reader reads
// in the order and with the roles the structure gives (ISO 32000-1:2008,
// 14.7-14.8).
type Mark struct {
	// Node is the DOM node the content belongs to: the element a word comes
	// from, the <img> or <svg> of an image, the list item of a marker. The
	// canvas finds the element's role from it (its tag, its ARIA role, its
	// alt text, its lang). Nil with Artifact.
	Node *dom.Node
	// Label marks a list item's marker: the item's label, not its body.
	Label bool
	// Artifact marks drawing no reader needs, the PDF's artifacts:
	// backgrounds, borders and shadows; text the style sheet generates
	// (::before, ::after) or keeps out of selection (user-select:none);
	// what is under aria-hidden="true"; and the bitmap of an element drawn
	// as one, whose text is drawn again over it as content.
	Artifact bool
}

// StructuredCanvas is a Canvas that keeps a document's structure.
// PaintVector calls Mark before the drawings each mark applies to, in
// painting order, which for a page in normal flow is the reading order. A
// mark may be followed by no drawing at all: the canvas opens its marked
// content only at the next drawing.
type StructuredCanvas interface {
	Canvas
	Mark(Mark)
}

// artifactMark is the mark of decoration.
var artifactMark = Mark{Artifact: true}

// contentMark is the mark of content drawn for n: an artifact when n is
// generated content or hidden from accessibility, or when the style keeps
// it out of selection.
func contentMark(n *dom.Node, st *css.Style) Mark {
	if st != nil && st.UserSelectNone {
		return artifactMark
	}
	for a := n; a != nil; a = a.Parent {
		if a.Type != dom.Element {
			continue
		}
		if a.Tag == "#pseudo" {
			return artifactMark
		}
		if h, ok := a.Attribute("aria-hidden"); ok && h == "true" {
			return artifactMark
		}
	}
	return Mark{Node: n}
}

// mark tells a structured canvas what follows.
func (v *vpainter) mark(m Mark) {
	if v.sc != nil {
		v.sc.Mark(m)
	}
}
