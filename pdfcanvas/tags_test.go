// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package pdfcanvas

import (
	"bytes"
	"context"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-pdfkit/pdfkit"
	"github.com/go-pdfkit/reader"

	"github.com/go-webengine/engine"
	"github.com/go-webengine/engine/paint"
)

// taggedPage has a role for each kind of element the canvas maps, and
// content that must stay out of the structure.
const taggedPage = `<!DOCTYPE html><html lang="en"><head><style>
body { margin: 0; font: 16px sans-serif; color: #111 }
section { background: #eef; border: 1px solid #99c }
.disc::before { content: "1" }
.gutter { user-select: none }
</style></head><body><section>
<h1>Title</h1>
<p>See <a href="https://example.com/">the site</a> now.</p>
<ul><li>one</li><li>two <b>bold</b></li></ul>
<img alt="A red square" src="IMG" width="20" height="20">
<img alt="" src="IMG" width="20" height="20">
<p>Area <svg role="math" aria-label="x^2" width="20" height="20" viewBox="0 0 2 2"><rect width="2" height="2"/></svg></p>
<p><span aria-hidden="true">hidden</span><span class="gutter">9</span><span class="disc"></span> end <span lang="fr">fin</span></p>
<table><tr><th>Head</th></tr><tr><td>Cell</td></tr></table>
</section></body></html>`

// taggedPDF lays the page out and draws it, tagged, into a PDF/UA document,
// its link under its Link element as go-slides adds it.
func taggedPDF(t *testing.T) []byte {
	t.Helper()
	e := engine.New()
	e.DisableJS = true
	html := strings.ReplaceAll(taggedPage, "IMG", pngURI(t))
	l, err := e.LayoutHTML(context.Background(), html, "https://example.com/", image.Rect(0, 0, 500, 600))
	if err != nil {
		t.Fatal(err)
	}
	doc := pdfkit.New(pdfkit.Options{Title: "Tagged", Lang: "en", DisplayDocTitle: true, PDFUA: true})
	pg := doc.AddPage(pdfkit.NewPageSize(375, 450))
	c := New(pg, 0.75, nil)
	c.Tag(doc.Structure())
	l.PaintVector(c)
	c.Finish()
	if c.Err != nil {
		t.Fatal(c.Err)
	}
	for _, ln := range l.Links() {
		r := ln.Rect
		pg.AddTaggedLink(c.Element(ln.Node), pdfkit.Rect{
			X: float64(r.Min.X) * 0.75, Y: 450 - float64(r.Max.Y)*0.75,
			Width: float64(r.Dx()) * 0.75, Height: float64(r.Dy()) * 0.75,
		}, ln.Href, TextOf(ln.Node))
	}
	var buf bytes.Buffer
	if err := doc.Write(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// structure reads the structure tree back: each element as its type, with
// its /Alt and /Lang, depth first.
func structure(t *testing.T, b []byte) []string {
	t.Helper()
	doc, err := reader.Open(b)
	if err != nil {
		t.Fatal(err)
	}
	cat, _ := doc.Catalog()
	root, ok := doc.GetDict(cat, "StructTreeRoot")
	if !ok {
		t.Fatal("no structure tree")
	}
	var out []string
	var walk func(o reader.Object)
	walk = func(o reader.Object) {
		ref, ok := o.(reader.Ref)
		if !ok {
			return
		}
		v, _ := doc.Get(ref)
		e, _ := reader.ToDict(v)
		if typ, _ := reader.ToName(e["Type"]); typ != "StructElem" {
			return
		}
		s, _ := reader.ToName(e["S"])
		desc := string(s)
		if alt, ok := reader.TextString(e["Alt"]); ok {
			desc += "[" + alt + "]"
		}
		if lang, ok := reader.TextString(e["Lang"]); ok {
			desc += "{" + lang + "}"
		}
		out = append(out, desc)
		if kids, ok := e["K"].(reader.Array); ok {
			for _, k := range kids {
				walk(k)
			}
		} else {
			walk(e["K"])
		}
	}
	walk(root["K"])
	return out
}

func TestTaggedStructure(t *testing.T) {
	b := taggedPDF(t)
	got := strings.Join(structure(t, b), " ")
	// The section, the heading, the paragraph with its link, the list with
	// its labels (the bullets) and bodies, the figure, the formula, the
	// paragraph whose hidden, unselectable and generated parts are left out
	// and whose French word is a Span, the table. The decorative image, the
	// backgrounds and the borders are artifacts.
	want := "Document Sect H1 P Link L LI Lbl LBody LI Lbl LBody Figure[A red square] P Formula[x^2] P Span{fr} Table TR TH TR TD"
	if got != want {
		t.Errorf("structure\n got %s\nwant %s", got, want)
	}
	for _, s := range []string{"/Artifact BMC", "/Contents (the site)", "/StructParent", "/Tabs /S"} {
		if !bytes.Contains(b, []byte(s)) {
			t.Errorf("no %q", s)
		}
	}
}

// An untagged canvas writes no marked content of its own: what it wrote
// before tagging existed, byte for byte, is still what it writes.
func TestUntaggedCanvasMarksNothing(t *testing.T) {
	b := unselectablePDF(t)
	for _, s := range []string{"MCID", "/Artifact", "StructTreeRoot"} {
		if bytes.Contains(b, []byte(s)) {
			t.Errorf("%s in an untagged page", s)
		}
	}
	c := New(pdfkit.New(pdfkit.Options{}).AddPage(pdfkit.A4), 1, nil)
	c.Mark(paint.Mark{Artifact: true}) // no Tag: ignored
	c.Finish()
	if c.Element(nil) != nil {
		t.Error("Element without Tag")
	}
}

// With veraPDF installed, the document passes its PDF/UA-1 profile.
func TestTaggedPassesVeraPDF(t *testing.T) {
	bin, err := exec.LookPath("verapdf")
	if err != nil {
		t.Skip("verapdf not installed")
	}
	in := filepath.Join(t.TempDir(), "tagged.pdf")
	if err := os.WriteFile(in, taggedPDF(t), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _ := exec.Command(bin, "--flavour", "ua1", "--format", "text", "-v", in).CombinedOutput()
	if !strings.HasPrefix(strings.TrimSpace(string(out)), "PASS") {
		t.Errorf("veraPDF:\n%s", out)
	}
}
