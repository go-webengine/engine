// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package pdfcanvas

import (
	"bytes"
	"context"
	"image"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-pdfkit/pdfkit"
	"rsc.io/pdf"

	"github.com/go-webengine/engine"
)

// A code line followed by a numbered disc drawn by ::before, and a gutter of
// line numbers under user-select:none: drawn, but not the document's text.
const unselectablePage = `<!DOCTYPE html><html><head><style>
body { margin: 0; font: 20px monospace; color: #222 }
.gutter { user-select: none; color: #888 }
.disc::before { content: "7" }
.keep::before { content: "K"; user-select: text }
</style></head><body>
<p><span class="gutter">41</span> code<span class="disc"></span><span class="keep"></span></p>
</body></html>`

func unselectablePDF(t *testing.T) []byte {
	t.Helper()
	e := engine.New()
	e.DisableJS = true
	l, err := e.LayoutHTML(context.Background(), unselectablePage, "", image.Rect(0, 0, 400, 100))
	if err != nil {
		t.Fatal(err)
	}
	doc := pdfkit.New(pdfkit.Options{})
	pg := doc.AddPage(pdfkit.NewPageSize(300, 75))
	c := New(pg, 0.75, nil)
	l.PaintVector(c)
	if c.Err != nil {
		t.Fatal(c.Err)
	}
	var buf bytes.Buffer
	if err := doc.Write(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestUnselectableTextIsWrappedInEmptyActualText(t *testing.T) {
	b := unselectablePDF(t)
	r, err := pdf.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	cs, err := io.ReadAll(r.Page(1).V.Key("Contents").Reader())
	if err != nil {
		t.Fatal(err)
	}
	content := string(cs)
	// the gutter and the ::before digit: two runs, two spans
	if n := strings.Count(content, "/Span <</ActualText ()>> BDC"); n != 2 {
		t.Errorf("%d empty ActualText spans, want 2 (the gutter and the disc)\n%s", n, content)
	}
	if strings.Count(content, "BDC") != strings.Count(content, "EMC") {
		t.Errorf("unbalanced marked content\n%s", content)
	}
}

// With Poppler's pdftotext installed: the code and the selectable generated
// content are extracted, the gutter and the disc are not.
func TestUnselectableTextIsNotExtracted(t *testing.T) {
	bin, err := exec.LookPath("pdftotext")
	if err != nil {
		t.Skip("pdftotext not installed")
	}
	in := filepath.Join(t.TempDir(), "p.pdf")
	if err := os.WriteFile(in, unselectablePDF(t), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(bin, in, "-").Output()
	if err != nil {
		t.Fatal(err)
	}
	text := strings.TrimSpace(string(out))
	if !strings.Contains(text, "code") || !strings.Contains(text, "K") || strings.Contains(text, "41") || strings.Contains(text, "7") {
		t.Errorf("pdftotext extracted %q: want code and K, without 41 and 7", text)
	}
}
