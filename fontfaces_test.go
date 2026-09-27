// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package engine

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-opentype/fonts/cabin"
	"github.com/go-webengine/engine/css"
	"github.com/go-webengine/engine/paint"
)

// fontServer serves one TTF at /f.ttf and counts the requests for it.
func fontServer(t *testing.T) (*httptest.Server, *int) {
	t.Helper()
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/f.ttf":
			n++
			w.Header().Set("Content-Type", "font/ttf")
			_, _ = w.Write(cabin.TTF)
		case "/missing.ttf":
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

// The whole path: a sheet's @font-face rule, the file fetched, the face
// registered, and the MEASUREMENT changed by it — which is the only thing
// that makes the typeface really the document's own.
func TestLoadFontFacesFetchesRegistersAndMeasures(t *testing.T) {
	srv, hits := fontServer(t)
	sheet := `@font-face { font-family: 'Cabin Web'; font-weight: 400; src: url(` + srv.URL + `/f.ttf) format('truetype'); }`
	doc := &Document{URL: srv.URL + "/page.html"}
	e := New()
	faces := e.LoadFontFaces(context.Background(), doc, []string{sheet}, css.Media{Width: 1024})
	if len(faces) != 1 {
		t.Fatalf("got %d faces, want 1", len(faces))
	}
	if faces[0].Family != "cabin web" || faces[0].Weight != 400 || faces[0].Italic {
		t.Errorf("face = %+v", faces[0])
	}
	if len(faces[0].Data) != len(cabin.TTF) {
		t.Errorf("Data is %d bytes, want the %d fetched", len(faces[0].Data), len(cabin.TTF))
	}
	if *hits != 1 {
		t.Errorf("fetched the file %d times, want 1", *hits)
	}
	fonts := paint.NewFonts()
	fam := css.FontFamily{Names: "cabin web", Generic: css.GenericSans}
	before := fonts.Measure("Partenaires", fam, 40, 400, false)
	if n := RegisterFontFaces(fonts, faces); n != 1 {
		t.Fatalf("registered %d faces, want 1", n)
	}
	if after := fonts.Measure("Partenaires", fam, 40, 400, false); after == before {
		t.Errorf("the registered face did not change the measurement (%v)", before)
	}
}

// A rule this build cannot decode leaves the page on its bundled family
// rather than half-loading a face: WOFF and WOFF2 are what a font service
// serves a browser, and reaching them needs a decoder in go-opentype.
func TestLoadFontFacesSkipsWhatItCannotDecode(t *testing.T) {
	srv, _ := fontServer(t)
	doc := &Document{URL: srv.URL + "/page.html"}
	e := New()
	for _, sheet := range []string{
		`@font-face { font-family: W; src: url(` + srv.URL + `/f.woff2) format('woff2'); }`,
		`@font-face { font-family: W; src: url(` + srv.URL + `/missing.ttf) format('truetype'); }`,
		`@font-face { font-family: W; src: url(data:font/ttf;base64,bm90IGEgZm9udA==) format('truetype'); }`,
	} {
		if faces := e.LoadFontFaces(context.Background(), doc, []string{sheet}, css.Media{}); len(faces) != 0 {
			t.Errorf("%q gave %d faces, want none", sheet, len(faces))
		}
	}
	// A WOFF wrapper is recognised and refused rather than fed to the parser.
	if _, ok := decodeFontFile([]byte("wOFFxxxx")); ok {
		t.Error("a WOFF wrapper must be refused")
	}
	if _, ok := decodeFontFile([]byte("wOF2xxxx")); ok {
		t.Error("a WOFF2 wrapper must be refused")
	}
	if _, ok := decodeFontFile([]byte("ab")); ok {
		t.Error("a truncated file must be refused")
	}
}

// src entries are tried in order, so a woff2 first choice falls through to the
// truetype beside it — which is exactly how a font service's sheet is written.
func TestLoadFontFacesFallsThroughTheSrcList(t *testing.T) {
	srv, _ := fontServer(t)
	sheet := `@font-face { font-family: F; src: url(` + srv.URL + `/f.woff2) format('woff2'), url(` + srv.URL + `/f.ttf) format('truetype'); }`
	faces := New().LoadFontFaces(context.Background(), &Document{URL: srv.URL + "/p.html"}, []string{sheet}, css.Media{})
	if len(faces) != 1 || len(faces[0].Data) != len(cabin.TTF) {
		t.Fatalf("faces = %+v", faces)
	}
}

// A data: URI src, which is how a self-contained document carries its own
// typefaces with no network at all.
func TestLoadFontFacesReadsADataURI(t *testing.T) {
	uri := "data:font/ttf;base64," + base64.StdEncoding.EncodeToString(cabin.TTF)
	sheet := `@font-face { font-family: Inline; src: url(` + uri + `); }`
	faces := New().LoadFontFaces(context.Background(), &Document{}, []string{sheet}, css.Media{})
	if len(faces) != 1 || faces[0].Family != "inline" {
		t.Fatalf("faces = %+v", faces)
	}
}

// One fetch per slot: a second rule for the same family, weight and slant is
// the same face, and a page that repeats it should not pay twice.
func TestLoadFontFacesFetchesEachSlotOnce(t *testing.T) {
	srv, hits := fontServer(t)
	rule := `@font-face { font-family: F; font-weight: 400; src: url(` + srv.URL + `/f.ttf); }`
	faces := New().LoadFontFaces(context.Background(), &Document{URL: srv.URL + "/p.html"}, []string{rule, rule}, css.Media{})
	if len(faces) != 1 {
		t.Errorf("got %d faces, want 1", len(faces))
	}
	if *hits != 1 {
		t.Errorf("fetched %d times, want 1", *hits)
	}
}

// Distinct slots of one family are distinct faces.
func TestLoadFontFacesKeepsDistinctSlots(t *testing.T) {
	srv, _ := fontServer(t)
	sheet := `@font-face { font-family: F; font-weight: 400; src: url(` + srv.URL + `/f.ttf); }
	          @font-face { font-family: F; font-weight: 700; src: url(` + srv.URL + `/f.ttf); }
	          @font-face { font-family: F; font-style: italic; src: url(` + srv.URL + `/f.ttf); }`
	faces := New().LoadFontFaces(context.Background(), &Document{URL: srv.URL + "/p.html"}, []string{sheet}, css.Media{})
	if len(faces) != 3 {
		t.Fatalf("got %d faces, want 3: %+v", len(faces), faces)
	}
}

func TestItoaAndBoolKey(t *testing.T) {
	for _, c := range []struct {
		n    int
		want string
	}{{0, "0"}, {7, "7"}, {400, "400"}, {12345678, "12345678"}} {
		if got := itoa(c.n); got != c.want {
			t.Errorf("itoa(%d) = %q want %q", c.n, got, c.want)
		}
	}
	if boolKey(true) != "i" || boolKey(false) != "n" {
		t.Error("boolKey")
	}
}
