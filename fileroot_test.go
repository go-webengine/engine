// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package engine

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-opentype/fonts/cabin"

	"github.com/go-webengine/engine/css"
)

func cabinTTF() []byte { return cabin.TTF }

// writePNG writes a 4x4 solid PNG of colour c to path.
func writePNG(t *testing.T, path string, c color.RGBA) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = c.R, c.G, c.B, c.A
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fileTree builds root/{blue.png,s.css,link.png -> ../outside/red.png} and
// outside/red.png under a fresh temporary directory, returning root and the
// file: base URL of root (with its trailing slash).
func fileTree(t *testing.T) (root, base string) {
	t.Helper()
	dir := t.TempDir()
	root = filepath.Join(dir, "deck")
	outside := filepath.Join(dir, "outside")
	for _, d := range []string{root, outside} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writePNG(t, filepath.Join(root, "blue.png"), color.RGBA{0, 0, 0xff, 0xff})
	writePNG(t, filepath.Join(outside, "red.png"), color.RGBA{0xff, 0, 0, 0xff})
	if err := os.WriteFile(filepath.Join(root, "s.css"), []byte(`.box{background-color:#00ff00}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "red.png"), filepath.Join(root, "link.png")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	return root, (&url.URL{Scheme: "file", Path: filepath.ToSlash(root) + "/"}).String()
}

func TestFileURLPath(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		ok       bool
	}{
		{"file:///tmp/x/a.png", filepath.FromSlash("/tmp/x/a.png"), true},
		{"file://localhost/tmp/a.png", filepath.FromSlash("/tmp/a.png"), true},
		{"FILE:///tmp/a.png", filepath.FromSlash("/tmp/a.png"), true},
		{"file:///C:/deck/a.png", filepath.FromSlash("C:/deck/a.png"), true}, // Windows drive letter
		{"file:///c|/deck/a.png", filepath.FromSlash("c:/deck/a.png"), true}, // legacy '|' form
		{"file://server/share/a.png", "", false},                             // UNC: not local
		{"file://", "", false},
		{"http://x/a.png", "", false},
		{"file://%zz/", "", false},
	} {
		got, ok := fileURLPath(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("fileURLPath(%q) = %q,%v want %q,%v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestReadFileURLRefusals(t *testing.T) {
	root, base := fileTree(t)
	e := New()
	e.FileRoot = root
	if _, ok := e.readFileURL(base, 1<<20); ok {
		t.Error("a directory was read as a file")
	}
	if _, ok := e.readFileURL("http://x/", 1<<20); ok {
		t.Error("a non-file URL was read")
	}
	if data, ok := e.readFileURL(base+"s.css", 4); !ok || string(data) != ".box" {
		t.Errorf("limit not applied: %q,%v", data, ok)
	}
	e.FileRoot = filepath.Join(root, "does-not-exist")
	if _, ok := e.readFileURL(base+"s.css", 1<<20); ok {
		t.Error("read through a FileRoot that does not exist")
	}
}

// TestFileRootFontFace: an @font-face src relative to a file: document loads
// from under FileRoot too.
func TestFileRootFontFace(t *testing.T) {
	root, base := fileTree(t)
	if err := os.WriteFile(filepath.Join(root, "f.ttf"), cabinTTF(), 0o644); err != nil {
		t.Fatal(err)
	}
	sheet := `@font-face{font-family:Local;src:url(f.ttf) format('truetype')}`
	doc := &Document{URL: base + "index.html"}
	e := New()
	if faces := e.LoadFontFaces(context.Background(), doc, []string{sheet}, css.Media{}); len(faces) != 0 {
		t.Fatalf("loaded %d faces with no FileRoot, want 0", len(faces))
	}
	e.FileRoot = root
	if faces := e.LoadFontFaces(context.Background(), doc, []string{sheet}, css.Media{}); len(faces) != 1 {
		t.Fatalf("loaded %d faces under FileRoot, want 1", len(faces))
	}
}

// TestFileRoot: file: resources load only when the host opts in with
// Engine.FileRoot, and only from under that directory — never by default
// (the Fetch standard leaves file: to the implementation and says "when in
// doubt, return a network error"; Chromium and marp-cli gate it behind
// --allow-file-access-from-files / --allow-local-files), never through ../,
// never through a symlink that leads outside the root.
func TestFileRoot(t *testing.T) {
	root, base := fileTree(t)
	render := func(e *Engine, body string) *image.RGBA {
		t.Helper()
		e.DisableJS = true
		html := `<!DOCTYPE html><html><head><style>html,body{margin:0}</style>` + body + `</html>`
		img, _, err := e.RenderHTML(context.Background(), html, base, image.Rect(0, 0, 200, 200))
		if err != nil {
			t.Fatal(err)
		}
		return img
	}
	bg := func(u string) string {
		return `</head><body><div style="width:200px;height:200px;background-image:url(&quot;` + u + `&quot;);background-size:100% 100%"></div></body>`
	}
	imgTag := `</head><body><img src="blue.png" style="display:block;width:200px;height:200px"></body>`
	sheet := `<link rel="stylesheet" href="s.css"></head><body><div class="box" style="width:200px;height:200px"></div></body>`
	allowed := func() *Engine { e := New(); e.FileRoot = root; return e }

	for _, tc := range []struct {
		name    string
		e       *Engine
		body    string
		r, g, b uint8
	}{
		{"default: background refused", New(), bg("blue.png"), 0xff, 0xff, 0xff},
		{"default: img refused", New(), imgTag, 0xff, 0xff, 0xff},
		{"default: stylesheet refused", New(), sheet, 0xff, 0xff, 0xff},
		{"root: background", allowed(), bg("blue.png"), 0, 0, 0xff},
		{"root: img", allowed(), imgTag, 0, 0, 0xff},
		{"root: stylesheet", allowed(), sheet, 0, 0xff, 0},
		{"root: absolute file URL inside", allowed(), bg(base + "blue.png"), 0, 0, 0xff},
		{"root: ../ escape refused", allowed(), bg("../outside/red.png"), 0xff, 0xff, 0xff},
		{"root: symlink escape refused", allowed(), bg("link.png"), 0xff, 0xff, 0xff},
		{"root: missing file", allowed(), bg("nope.png"), 0xff, 0xff, 0xff},
		{"root: remote host refused", allowed(), bg("file://example.com" + filepath.ToSlash(root) + "/blue.png"), 0xff, 0xff, 0xff},
	} {
		t.Run(tc.name, func(t *testing.T) {
			img := render(tc.e, tc.body)
			assertPixel(t, img, 100, 100, tc.r, tc.g, tc.b, tc.name)
		})
	}
}
