// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package engine

import (
	"bytes"
	"context"
	"errors"
	"image"
	"testing"

	"github.com/go-opentype/fonts/cabin"
	"github.com/go-opentype/fonts/lato"
)

// A lazy host face is fetched when a render draws its family, once across
// renders, and never when nothing does; it renders what the same face
// parsed up front renders. One whose fetch fails is passed over.
func TestLazyFontFace(t *testing.T) {
	const doc = `<!DOCTYPE html><html><body style="margin:0"><p style="font-family:Cabin,sans-serif;font-size:40px">Partenaires</p></body></html>`
	view := image.Rect(0, 0, 400, 120)
	paint := func(e *Engine) []byte {
		l, err := e.LayoutHTML(context.Background(), doc, "about:blank", view)
		if err != nil {
			t.Fatal(err)
		}
		return l.Paint().Pix
	}
	cabinLoads, latoLoads := 0, 0
	lazy := New()
	lazy.DisableJS = true
	lazy.FontFaces = []LoadedFontFace{
		LazyFontFace("cabin", 400, false, func() ([]byte, error) { cabinLoads++; return cabin.TTF, nil }),
		LazyFontFace("lato", 400, false, func() ([]byte, error) { latoLoads++; return lato.TTF, nil }),
	}
	eager := New()
	eager.DisableJS = true
	face, err := ParseFontFace("cabin", 400, false, cabin.TTF)
	if err != nil {
		t.Fatal(err)
	}
	eager.FontFaces = []LoadedFontFace{face}
	a, b := paint(lazy), paint(eager)
	paint(lazy)
	if !bytes.Equal(a, b) || cabinLoads != 1 || latoLoads != 0 {
		t.Fatalf("same pixels %v, cabin loaded %d times, lato %d", bytes.Equal(a, b), cabinLoads, latoLoads)
	}
	// a face that cannot be fetched: the render falls back, as without it
	broken := New()
	broken.DisableJS = true
	broken.FontFaces = []LoadedFontFace{LazyFontFace("cabin", 400, false, func() ([]byte, error) { return nil, errors.New("offline") })}
	plain := New()
	plain.DisableJS = true
	if !bytes.Equal(paint(broken), paint(plain)) {
		t.Fatal("a face that failed to load changed the render")
	}
	if bytes.Equal(paint(plain), a) {
		t.Fatal("Cabin draws as the fallback does: the comparison proves nothing")
	}
}
