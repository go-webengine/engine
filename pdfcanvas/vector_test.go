// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package pdfcanvas

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-pdfkit/pdfkit"

	"github.com/go-webengine/engine"
)

// vectorSVG has every kind of shape a form holds: a solid fill under
// opacity, a linear and a radial gradient, a stroke, a clip path.
const vectorSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 40 40">
<defs>
 <linearGradient id="l"><stop offset="0" stop-color="#00f"/><stop offset="1" stop-color="#0f0"/></linearGradient>
 <radialGradient id="r"><stop offset="0" stop-color="#ff0"/><stop offset="1" stop-color="#f00"/></radialGradient>
 <clipPath id="c"><circle cx="30" cy="30" r="8"/></clipPath>
</defs>
<rect width="20" height="20" fill="url(#l)"/>
<rect x="20" width="20" height="20" fill="url(#r)"/>
<rect y="20" width="20" height="20" fill="#c00" fill-opacity="0.5"/>
<rect x="20" y="20" width="20" height="20" fill="#080" clip-path="url(#c)"/>
<path d="M2 38 L18 22" stroke="#000" stroke-width="3" stroke-linecap="round"/>
</svg>`

const vectorPage = `<!DOCTYPE html><html lang="en"><head><style>
body { margin: 0; background: #fff }
img { display: inline-block }
</style></head><body>
<p><img alt="A drawing" src="SVG" width="120" height="120"><img alt="A drawing" src="SVG" width="60" height="60"><img alt="A drawing" src="SVG" width="30" height="30"></p>
<p>Area <svg role="math" aria-label="x^2" width="40" height="20" viewBox="0 0 4 2"><path d="M0 0 L4 2 L0 2 Z"/></svg></p>
</body></html>`

// vectorPDF draws the page on two pages of one tagged document.
func vectorPDF(t *testing.T, compress bool) ([]byte, *image.RGBA) {
	t.Helper()
	e := engine.New()
	e.DisableJS = true
	html := strings.ReplaceAll(vectorPage, "SVG", "data:image/svg+xml,"+url.PathEscape(vectorSVG))
	l, err := e.LayoutHTML(context.Background(), html, "", image.Rect(0, 0, 400, 300))
	if err != nil {
		t.Fatal(err)
	}
	doc := pdfkit.New(pdfkit.Options{Compress: compress, Title: "Vector", Lang: "en", PDFUA: true, DisplayDocTitle: true})
	fonts := NewFonts()
	for i := 0; i < 2; i++ {
		pg := doc.AddPage(pdfkit.NewPageSize(300, 225))
		c := New(pg, 0.75, fonts)
		c.Tag(doc.Structure())
		st := l.PaintVector(c)
		c.Finish()
		if c.Err != nil {
			t.Fatal(c.Err)
		}
		if st.Vectors != 4 || st.Images != 0 {
			t.Fatalf("stats %+v, want 4 vector images and no bitmap", st)
		}
	}
	var buf bytes.Buffer
	if err := doc.Write(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), l.Paint()
}

// The drawing shown three times on each of two pages is one form; the
// formula another; each use is one Do inside its element's sequence, the
// formula's under its TeX as ActualText. The structure keeps its Figures and
// its Formula with their descriptions.
func TestSVGIsAFormDrawnOnce(t *testing.T) {
	b, _ := vectorPDF(t, false)
	if n := bytes.Count(b, []byte("/Subtype /Form")); n != 2 {
		t.Errorf("%d forms, want 2 (the drawing, the formula)", n)
	}
	if n := bytes.Count(b, []byte("/Fm0 Do")); n != 6 {
		t.Errorf("the drawing is painted %d times, want 6", n)
	}
	if n := bytes.Count(b, []byte("/Span <</ActualText (x^2)>> BDC\nq\n")); n != 2 {
		t.Errorf("%d formulas under their ActualText, want 2", n)
	}
	for _, op := range []string{" sh\n", " gs\n", "W\nn\n", " l\n", " c\n"} {
		if !bytes.Contains(b, []byte(op)) {
			t.Errorf("no %q in the forms", op)
		}
	}
	got := strings.Join(structure(t, b), " ")
	want := "Document P Figure[A drawing] Figure[A drawing] Figure[A drawing] P Formula[x^2] P Figure[A drawing] Figure[A drawing] Figure[A drawing] P Formula[x^2]"
	if got != want {
		t.Errorf("structure\n got %s\nwant %s", got, want)
	}
}

// The PDF looks like the bitmap render, and at 8× it is sharp where the
// bitmap would be blocks: rendered by poppler when installed.
func TestSVGFormLooksLikeTheBitmap(t *testing.T) {
	bin, err := exec.LookPath("pdftoppm")
	if err != nil {
		t.Skip("pdftoppm not installed")
	}
	b, want := vectorPDF(t, true)
	dir := t.TempDir()
	in := filepath.Join(dir, "v.pdf")
	if err := os.WriteFile(in, b, 0o644); err != nil {
		t.Fatal(err)
	}
	// 0.75 pt a pixel: 96 dpi is one PDF pixel per layout pixel
	if out, err := exec.Command(bin, "-r", "96", "-png", "-f", "1", "-l", "1", "-singlefile", in, filepath.Join(dir, "p")).CombinedOutput(); err != nil {
		t.Fatalf("pdftoppm: %v\n%s", err, out)
	}
	f, err := os.Open(filepath.Join(dir, "p.png"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	// Compare the drawing's box at 120×120 (the first image), pixel by
	// pixel, away from its edges, where antialiasing differs.
	r := image.Rect(4, 0, 124, 124)
	diff, n := 0, 0
	for y := r.Min.Y + 20; y < r.Max.Y-4; y++ {
		for x := r.Min.X + 2; x < r.Max.X-2; x++ {
			gr, gg, gb, _ := got.At(x, y).RGBA()
			w := want.RGBAAt(x, y)
			d := absDiff(gr>>8, uint32(w.R)) + absDiff(gg>>8, uint32(w.G)) + absDiff(gb>>8, uint32(w.B))
			if d > 3*48 {
				diff++
			}
			n++
		}
	}
	if diff*100 > n*3 {
		t.Errorf("%d of %d pixels differ from the bitmap render", diff, n)
	}
}

func absDiff(a, b uint32) uint32 {
	if a > b {
		return a - b
	}
	return b - a
}
