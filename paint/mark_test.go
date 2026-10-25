// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package paint

import (
	"image"
	"strings"
	"testing"

	"github.com/go-webengine/engine/css"
	"github.com/go-webengine/engine/dom"
	"github.com/go-webengine/engine/layout"
)

// markCanvas is a refCanvas that keeps the structure: it records, for each
// drawing, the mark it was drawn under.
type markCanvas struct {
	*refCanvas
	cur  Mark
	seen []string
}

func (c *markCanvas) Mark(m Mark) { c.cur = m }

func (c *markCanvas) note(op string) {
	switch {
	case c.cur.Artifact:
		c.seen = append(c.seen, op+":artifact")
	case c.cur.Label:
		c.seen = append(c.seen, op+":label("+c.cur.Node.Tag+")")
	case c.cur.Node == nil:
		c.seen = append(c.seen, op+":none")
	default:
		n := c.cur.Node
		for n.Type != dom.Element { // a word's text node: its element
			n = n.Parent
		}
		c.seen = append(c.seen, op+":"+n.Tag)
	}
}

func (c *markCanvas) Fill(p *Path, col css.Color, evenOdd bool) {
	c.note("fill")
	c.refCanvas.Fill(p, col, evenOdd)
}

func (c *markCanvas) Image(img image.Image, dst Rect) {
	c.note("image")
	c.refCanvas.Image(img, dst)
}

func (c *markCanvas) Glyphs(run *GlyphRun) {
	c.note("text")
	c.refCanvas.Glyphs(run)
}

func markElem(tag string, parent *dom.Node, attr map[string]string) *dom.Node {
	n := &dom.Node{Type: dom.Element, Tag: tag, Parent: parent, Attr: attr}
	if parent != nil {
		parent.Children = append(parent.Children, n)
	}
	return n
}

// PaintVector tells a structured canvas what each drawing stands for: the
// box's background and borders, generated text (::before), text under
// aria-hidden or out of selection and a bitmap fallback are artifacts; a
// word is content of its element (the nearest one, through a text node); a
// list marker, decimal or bullet, drawn or as the hidden text over a
// bitmap, is its item's label; an image is content of its <img>.
func TestPaintVectorMarks(t *testing.T) {
	st := func() *css.Style {
		return &css.Style{FontSize: 16, Color: rgb(0, 0, 0), Visibility: css.VisibilityVisible}
	}
	root := markElem("section", nil, nil)
	p := markElem("p", root, nil)
	txt := &dom.Node{Type: dom.Text, Text: "word", Parent: p}
	pseudo := markElem("#pseudo", p, nil)
	hidden := markElem("span", markElem("span", p, map[string]string{"aria-hidden": "true"}), nil)
	img := markElem("img", p, nil)
	li := markElem("li", root, nil)
	ul := markElem("li", root, nil)
	unsel := st()
	unsel.UserSelectNone = true
	at := func(n *dom.Node, s *css.Style, x float64) *layout.InlineItem {
		return &layout.InlineItem{Text: "ab", Style: s, Node: n, X: x, Y: 10, Ascent: 12, Width: 20, LineHeight: 16}
	}
	bg := st()
	bg.Background = rgb(200, 200, 255)
	box := &layout.Box{Node: root, Style: bg, W: 300, H: 200, Children: []*layout.Box{
		{Node: p, Style: st(), W: 300, H: 40, Lines: []*layout.LineBox{{Items: []*layout.InlineItem{
			at(txt, st(), 0), at(pseudo, st(), 30), at(hidden, st(), 60), at(p, unsel, 90),
			{Image: img, Style: st(), X: 120, Y: 0, Width: 10, LineHeight: 10},
		}}}},
		{Node: li, Style: st(), Y: 50, W: 300, H: 20, Marker: &layout.Marker{Type: css.ListDecimal, Text: "1.", Style: st(), X: 0, Y: 50, Ascent: 12}},
		{Node: ul, Style: st(), Y: 80, W: 300, H: 20, Marker: &layout.Marker{Type: css.ListDisc, Style: st(), X: 0, Y: 80, W: 6, H: 6}},
		// a faded list item: drawn as a bitmap, its marker and words drawn
		// again as hidden text over it
		{Node: li, Style: &css.Style{FontSize: 16, Color: rgb(0, 0, 0), Visibility: css.VisibilityVisible, HasOpacity: true, Opacity: 0.5, Background: rgb(1, 2, 3)},
			Y: 110, W: 300, H: 20, Marker: &layout.Marker{Type: css.ListDecimal, Text: "2.", Style: st(), X: 0, Y: 110, Ascent: 12},
			Lines: []*layout.LineBox{{Items: []*layout.InlineItem{at(txt, st(), 30)}}}},
	}}
	c := &markCanvas{refCanvas: newRefCanvas(300, 200)}
	PaintVector(c, box, NewFonts(), map[*dom.Node]image.Image{img: image.NewRGBA(image.Rect(0, 0, 2, 2))}, nil, image.Rect(0, 0, 300, 200))
	got := strings.Join(c.seen, " ")
	want := "fill:artifact text:p text:artifact text:artifact text:artifact image:img " +
		"text:label(li) fill:label(li) image:artifact text:label(li) text:p"
	if got != want {
		t.Errorf("marks\n got %s\nwant %s", got, want)
	}
}
