// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package pdfcanvas

import (
	"strconv"
	"strings"

	"github.com/go-pdfkit/pdfkit"
	"github.com/go-webengine/engine/dom"
	"github.com/go-webengine/engine/paint"
)

// Tagged PDF. With Tag, the canvas keeps the page's logical structure (ISO
// 32000-1:2008, 14.7-14.8) through go-pdfkit's structure tree: each drawing
// PaintVector marks (paint.Mark) is either content of an element, drawn in
// a marked-content sequence of that element, or an artifact.
//
// The elements come from the DOM, as a browser's accessibility tree does,
// and Chromium's printed PDFs with it (Skia's SkPDFTag): a heading is an H1
// to H6, a paragraph a P, a list an L of LI each with an Lbl (its marker)
// and an LBody, a table a Table of TR, TH and TD, an <a href> a Link, an
// image a Figure with its alt text, an element with role="math" (or MathML)
// a Formula with its label, a <section> a Sect. A plain container (a div, a
// span) is transparent: its content belongs to the nearest element that has
// a role. An element is made when its first content is drawn, after the
// elements above it, so the structure follows the drawing, which for a page
// in normal flow is the reading order.

// Tag makes the canvas write tagged content under parent, an element of the
// document's structure (typically pdfkit.Document.Structure(), or an element
// per page under it). Call it before PaintVector.
func (c *Canvas) Tag(parent *pdfkit.StructElem) {
	c.root = parent
	c.elems = map[*dom.Node]*pdfkit.StructElem{}
	c.lists = map[*dom.Node]*listItem{}
	c.cur = paint.Mark{Artifact: true}
	c.open = false
}

// listItem is a list item's two children, made as needed.
type listItem struct{ lbl, body *pdfkit.StructElem }

var _ paint.StructuredCanvas = (*Canvas)(nil)

// Mark implements paint.StructuredCanvas: what is drawn next stands for m.
// The marked-content sequence opens at the next drawing.
func (c *Canvas) Mark(m paint.Mark) {
	if c.root == nil {
		return
	}
	c.next = m
}

// begin opens the sequence the pending mark asks for, if it differs from
// the open one. Every drawing call starts with it.
func (c *Canvas) begin() {
	if c.root == nil {
		return
	}
	m := c.next
	if c.open && m == c.cur {
		return
	}
	e, artifact := c.target(m)
	if c.open && artifact && c.curArtifact {
		c.cur = m
		return // one artifact sequence for successive decoration
	}
	if c.open && !artifact && !c.curArtifact && e == c.curElem {
		c.cur = m
		return // the same element: one sequence
	}
	if c.open {
		c.p.EndMarkedContent()
	}
	if artifact {
		c.p.BeginArtifact(pdfkit.Artifact{})
	} else {
		c.p.BeginTag(e)
	}
	c.open, c.cur, c.curElem, c.curArtifact = true, m, e, artifact
}

// Finish closes the open sequence. Call it after PaintVector, before drawing
// anything else on the page.
func (c *Canvas) Finish() {
	if c.open {
		c.p.EndMarkedContent()
		c.open = false
	}
}

// target is the element a mark's content goes to, or artifact.
func (c *Canvas) target(m paint.Mark) (e *pdfkit.StructElem, artifact bool) {
	if m.Artifact || m.Node == nil {
		return nil, true
	}
	n, role, artifact := c.semantic(m.Node)
	if artifact {
		return nil, true
	}
	if n == nil {
		return c.root, false
	}
	e = c.element(n, role)
	if role.typ == pdfkit.StructLI {
		li := c.lists[n]
		if m.Label {
			if li.lbl == nil {
				li.lbl = e.Add(pdfkit.StructLbl)
			}
			return li.lbl, false
		}
		return c.body(n, e), false
	}
	return e, false
}

// Element returns the structure element of a DOM node that has a role (an
// <a href>, a heading…), making it if no content of it was drawn yet: what
// a caller ties an annotation to (pdfkit.Page.AddTaggedLink). It returns nil
// for a node with no role of its own, or an artifact, or without Tag.
func (c *Canvas) Element(n *dom.Node) *pdfkit.StructElem {
	if c.root == nil || n == nil {
		return nil
	}
	sn, role, artifact := c.semantic(n)
	if artifact || sn != n {
		return nil
	}
	return c.element(n, role)
}

// body is a list item's LBody, where its content and its nested elements go.
func (c *Canvas) body(n *dom.Node, e *pdfkit.StructElem) *pdfkit.StructElem {
	li := c.lists[n]
	if li.body == nil {
		li.body = e.Add(pdfkit.StructLBody)
	}
	return li.body
}

// element returns n's element, making it, and the elements above it, as
// needed.
func (c *Canvas) element(n *dom.Node, r role) *pdfkit.StructElem {
	if e, ok := c.elems[n]; ok {
		return e
	}
	parent := c.root
	if pn, pr, _ := c.semantic(n.Parent); pn != nil {
		parent = c.element(pn, pr)
		if pr.typ == pdfkit.StructLI {
			parent = c.body(pn, parent)
		}
	}
	e := parent.Add(r.typ)
	e.Alt, e.Lang = r.alt, r.lang
	if r.typ == pdfkit.StructTH {
		// PDF/UA: a header cell says what it heads when no Headers/ID
		// pairs do (ISO 32000-1:2008, 14.8.5.7, Table 349)
		e.Attributes = []pdfkit.Attribute{{Owner: "Table", Key: "Scope", Value: pdfkit.Name(thScope(n))}}
	}
	c.elems[n] = e
	if r.typ == pdfkit.StructLI {
		c.lists[n] = &listItem{}
	}
	return e
}

// role is what an element is in the structure.
type role struct {
	typ, alt, lang string
}

// semantic finds the element n's content belongs to: n itself or its
// nearest ancestor with a role, and that role. artifact reports content no
// reader needs: under aria-hidden, generated by the style sheet, or an
// image marked decorative (an empty alt). A nil node with no artifact means
// the content belongs to the canvas's root element.
func (c *Canvas) semantic(n *dom.Node) (*dom.Node, role, bool) {
	for ; n != nil; n = n.Parent {
		if n.Type != dom.Element {
			continue
		}
		if n.Tag == "#pseudo" {
			return nil, role{}, true
		}
		if h, _ := n.Attribute("aria-hidden"); h == "true" {
			return nil, role{}, true
		}
		r, ok, artifact := roleOf(n)
		if artifact {
			return nil, role{}, true
		}
		if ok {
			return n, r, false
		}
	}
	return nil, role{}, false
}

// roleOf is an element's role: its ARIA role, else its tag's (HTML-AAM's
// mappings, the structure types ISO 32000-1:2008, 14.8.4 names for them).
// ok is false for a transparent element; artifact for a decorative image.
func roleOf(n *dom.Node) (r role, ok, artifact bool) {
	attr := func(k string) string { v, _ := n.Attribute(k); return strings.TrimSpace(v) }
	label := func() string {
		for _, k := range []string{"aria-label", "alt", "alttext", "title"} {
			if v := attr(k); v != "" {
				return v
			}
		}
		for _, ch := range n.Children { // an SVG's <title>
			if ch.Type == dom.Element && ch.Tag == "title" {
				return strings.TrimSpace(textOf(ch))
			}
		}
		return ""
	}
	r.lang = attr("lang")
	typ := ""
	switch attr("role") {
	case "presentation", "none":
		return r, r.lang != "", false
	case "heading":
		lv, err := strconv.Atoi(attr("aria-level"))
		if err != nil || lv < 1 || lv > 6 {
			lv = 2 // ARIA's default level
		}
		typ = "H" + strconv.Itoa(lv)
	case "paragraph":
		typ = pdfkit.StructP
	case "list":
		typ = pdfkit.StructL
	case "listitem":
		typ = pdfkit.StructLI
	case "link":
		typ = pdfkit.StructLink
	case "table", "grid":
		typ = pdfkit.StructTable
	case "row":
		typ = pdfkit.StructTR
	case "cell", "gridcell":
		typ = pdfkit.StructTD
	case "columnheader", "rowheader":
		typ = pdfkit.StructTH
	case "blockquote":
		typ = pdfkit.StructBlockQuote
	case "code":
		typ = pdfkit.StructCode
	case "note":
		typ = pdfkit.StructNote
	case "region", "article", "section":
		typ = pdfkit.StructSect
	case "img", "image", "figure":
		r.alt = label()
		if r.alt == "" {
			return r, false, true
		}
		typ = pdfkit.StructFigure
	case "math":
		r.alt = label()
		typ = pdfkit.StructFormula
	}
	if typ == "" {
		switch n.Tag {
		case "h1", "h2", "h3", "h4", "h5", "h6":
			typ = "H" + n.Tag[1:]
		case "p", "pre":
			typ = pdfkit.StructP
		case "ul", "ol", "menu":
			typ = pdfkit.StructL
		case "li":
			typ = pdfkit.StructLI
		case "a":
			if attr("href") != "" {
				typ = pdfkit.StructLink
			}
		case "table":
			// A table's rows go straight under it: thead, tbody and tfoot
			// are transparent. As THead and TBody they would leave a THead
			// alone when the body is not drawn (clipped by the page, empty),
			// which PDF/UA forbids, and a TR may be a Table's child.
			typ = pdfkit.StructTable
		case "tr":
			typ = pdfkit.StructTR
		case "th":
			typ = pdfkit.StructTH
		case "td":
			typ = pdfkit.StructTD
		case "caption", "figcaption":
			typ = pdfkit.StructCaption
		case "blockquote":
			typ = pdfkit.StructBlockQuote
		case "q":
			typ = pdfkit.StructQuote
		case "code", "kbd", "samp":
			typ = pdfkit.StructCode
		case "section", "nav", "aside":
			typ = pdfkit.StructSect
		case "article":
			typ = "Art"
		case "main":
			typ = pdfkit.StructPart
		case "math":
			r.alt = label()
			typ = pdfkit.StructFormula
		case "img":
			alt, has := n.Attribute("alt")
			if has && strings.TrimSpace(alt) == "" {
				return r, false, true // decorative (HTML: alt="")
			}
			r.alt = strings.TrimSpace(alt)
			typ = pdfkit.StructFigure
		case "svg":
			// an inline SVG that says what it is; one that does not is
			// decoration, as an <img alt=""> is
			if r.alt = label(); r.alt == "" {
				return r, false, true
			}
			typ = pdfkit.StructFigure
		}
	}
	if r.lang != "" && (n.Tag == "html" || r.lang == langOf(n.Parent)) {
		// the document's language is the catalog's (pdfkit.Options.Lang);
		// an element repeats its parent's for nothing
		r.lang = ""
	}
	if typ == "" && r.lang != "" {
		typ = pdfkit.StructSpan // a change of language
	}
	r.typ = typ
	return r, typ != "", false
}

// thScope is a header cell's Scope: its scope attribute (HTML's col, row,
// colgroup, rowgroup); else Column for a header in a row of headers, or in a
// <thead>, and Row for one beside data cells.
func thScope(th *dom.Node) string {
	switch s, _ := th.Attribute("scope"); strings.ToLower(strings.TrimSpace(s)) {
	case "col", "colgroup":
		return "Column"
	case "row", "rowgroup":
		return "Row"
	}
	tr := th.Parent
	if tr == nil {
		return "Column"
	}
	if tr.Parent != nil && tr.Parent.Type == dom.Element && tr.Parent.Tag == "thead" {
		return "Column"
	}
	for _, c := range tr.Children {
		if c.Type == dom.Element && c.Tag == "td" {
			return "Row"
		}
	}
	return "Column"
}

// langOf is the language n is in: its own lang, or its nearest ancestor's.
func langOf(n *dom.Node) string {
	for ; n != nil; n = n.Parent {
		if n.Type == dom.Element {
			if l, ok := n.Attribute("lang"); ok {
				return strings.TrimSpace(l)
			}
		}
	}
	return ""
}

// textOf is the text under n.
func textOf(n *dom.Node) string {
	var b strings.Builder
	var walk func(*dom.Node)
	walk = func(n *dom.Node) {
		if n.Type == dom.Text {
			b.WriteString(n.Text)
		}
		for _, ch := range n.Children {
			walk(ch)
		}
	}
	walk(n)
	return b.String()
}

// TextOf is the text under a DOM node, its words separated by single
// spaces: an accessible name for an annotation's /Contents.
func TextOf(n *dom.Node) string {
	if n == nil {
		return ""
	}
	return strings.Join(strings.Fields(textOf(n)), " ")
}
