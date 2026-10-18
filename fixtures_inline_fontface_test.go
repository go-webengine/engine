// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package engine

import (
	"bytes"
	"encoding/base64"
	"testing"

	"github.com/go-opentype/fonts/cabin"
)

// TestRenderLoadsInlineStyleFontFace: an @font-face rule in the document's
// own <style> element is a font of the document, exactly like one in a
// linked sheet. A self-contained document — a Marp deck exported for
// offline rendering, which embeds its theme fonts as data: URIs in its
// inline theme CSS — carries its typefaces nowhere else. Before this, the
// render handed LoadFontFaces only the LINKED sheets, so such a face was
// never registered and the text fell back to the bundled family.
func TestRenderLoadsInlineStyleFontFace(t *testing.T) {
	uri := "data:font/ttf;base64," + base64.StdEncoding.EncodeToString(cabin.TTF)
	page := func(fontFace string) string {
		return `<!DOCTYPE html><html><head><style>` + fontFace +
			`html,body{margin:0}p{font-size:40px;font-family:'Inline Face', serif;margin:0}</style></head>` +
			`<body><p>Hamburgefonstiv</p></body></html>`
	}
	without := renderString(t, page(""), 400, 60)
	with := renderString(t, page(`@font-face{font-family:'Inline Face';src:url(`+uri+`) format('truetype')}`), 400, 60)
	if bytes.Equal(with.Pix, without.Pix) {
		t.Fatal("an inline <style> @font-face did not change the rendering: the face was not loaded")
	}
	// Positive control for "differs": the same face named by a family that no
	// rule declares renders exactly like the fallback.
	unrelated := renderString(t, page(`@font-face{font-family:'Other Face';src:url(`+uri+`)}`), 400, 60)
	if !bytes.Equal(unrelated.Pix, without.Pix) {
		t.Error("a face the text does not use changed the rendering")
	}
}
