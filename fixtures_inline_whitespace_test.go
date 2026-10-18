// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package engine

import (
	"context"
	"image"
	"testing"
)

// TestInlineWhiteSpaceNowrap: white-space applies to inline elements too
// (CSS Text 3 §3). A soft wrap opportunity between two characters is
// controlled by the white-space of their nearest common ancestor (§5.5), so
// the spaces inside a nowrap/pre span offer no break, while the boundaries
// around the span still do.
func TestInlineWhiteSpaceNowrap(t *testing.T) {
	measure := func(body string) string {
		t.Helper()
		page := `<!DOCTYPE html><html><head><style>body{margin:0}h1{width:400px;font-size:20px;margin:0;line-height:26px}</style></head><body>` +
			body + `<script>var h=document.querySelector('h1'),s=document.querySelector('span');` +
			`document.title=h.offsetHeight+','+(s?s.getBoundingClientRect().height:0)</script></body></html>`
		_, info, err := New().RenderHTML(context.Background(), page, "https://demo.test/", image.Rect(0, 0, 800, 200))
		if err != nil {
			t.Fatal(err)
		}
		return info.Title
	}
	const long = "A rather long heading that would wrap over two lines otherwise"
	for _, tc := range []struct{ name, body, want string }{
		{"nowrap on the block (control)", `<h1 style="white-space:nowrap">` + long + `</h1>`, "26,0"},
		{"nowrap on an inline span", `<h1><span style="white-space:nowrap">` + long + `</span></h1>`, "26,26"},
		{"pre on an inline span", `<h1><span style="white-space:pre">` + long + `</span></h1>`, "26,26"},
		{"normal span still wraps (control)", `<h1><span>` + long + `</span></h1>`, "52,52"},
		// The boundary before the nowrap span is a wrap opportunity: the
		// span moves to the next line as a whole.
		{"break before a nowrap span", `<h1>Lead words <span style="white-space:nowrap">` + long + `</span></h1>`, "52,26"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := measure(tc.body); got != tc.want {
				t.Errorf("h1 height, span height = %q, want %q", got, tc.want)
			}
		})
	}
}
