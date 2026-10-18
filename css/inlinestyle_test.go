// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package css

import (
	"testing"

	"github.com/go-webengine/engine/dom"
)

func TestInlineStyleTextFeedsParseFontFaces(t *testing.T) {
	root, err := dom.Parse(`<html><head><style>@font-face{font-family:A;src:url(a.ttf)}</style></head>` +
		`<body><style>@font-face{font-family:B;font-weight:700;src:url(b.ttf)}</style></body></html>`)
	if err != nil {
		t.Fatal(err)
	}
	faces := ParseFontFaces(InlineStyleText(root), Media{Width: 1024})
	if len(faces) != 2 || faces[0].Family != "a" || faces[1].Family != "b" || faces[1].Weight != 700 {
		t.Fatalf("faces = %+v", faces)
	}
}
