// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package engine

import (
	"context"
	"fmt"
	"image"
	"testing"

	"github.com/go-opentype/fonts/robotomono"
)

// redRowTotal counts the rows of img that hold a pure red pixel.
func redRowTotal(img *image.RGBA) int {
	n := 0
	for y := img.Bounds().Min.Y; y < img.Bounds().Max.Y; y++ {
		for x := img.Bounds().Min.X; x < img.Bounds().Max.X; x++ {
			if c := img.RGBAAt(x, y); c.R == 0xff && c.G == 0 && c.B == 0 {
				n++
				break
			}
		}
	}
	return n
}

// TestInlineBlockWordFillsItsBoxExactly pins the shrink-to-fit of an inline-block
// holding one word (CSS Sizing 3 §5: its width is the max-content width of its
// text): the word fits the box it sized, whole, on one line. gaia sets
// `code{display:inline-block;font-family:Roboto Mono;font-size:.8em;...}` inside
// a section with `overflow-wrap:break-word`; the box was given the text's
// max-content width less its edges, the line measured the same text again, and
// the two sums differed by the last bit of a float64, so `chapter` did not fit
// the box it had sized and was broken in two ("chapte", "r").
//
// The red box holds the word in red on red: its height in rows is one line's when
// the word is whole, two lines' when it was broken. The sizes cover the ones where
// the sums are known to come apart (35px is gaia's, 0.8em of it the code's).
func TestInlineBlockWordFillsItsBoxExactly(t *testing.T) {
	face, err := ParseFontFace("Roboto Mono", 400, false, robotomono.TTF)
	if err != nil {
		t.Fatal(err)
	}
	const page = `<!DOCTYPE html><html><head><style>html,body{margin:0}
p{font-size:%dpx;width:1000px;margin:0;line-height:1.35;overflow-wrap:break-word}
code{display:inline-block;font-family:Roboto Mono,monospace;font-size:.8em;margin:-.1em .15em;padding:.1em .2em;background:#f00;color:#f00%s}
</style></head><body><p>The file defines <code>chapter</code> and <code>section</code>.</p></body></html>`
	render := func(size int, extra string) *image.RGBA {
		e := New()
		e.FontFaces = []LoadedFontFace{face}
		img, _, err := e.RenderHTML(context.Background(), fmt.Sprintf(page, size, extra), "https://demo.test/", image.Rect(0, 0, 1000, 200))
		if err != nil {
			t.Fatal(err)
		}
		return img
	}
	for size := 24; size <= 48; size++ {
		t.Run(fmt.Sprintf("%dpx", size), func(t *testing.T) {
			// white-space:nowrap cannot break the word whatever the sums say:
			// the height of one whole line, which the default must also have
			one := redRowTotal(render(size, ";white-space:nowrap"))
			got := redRowTotal(render(size, ""))
			if one == 0 || got != one {
				t.Errorf("the code boxes are %d rows high, want %d (one line): the word was broken inside its own box", got, one)
			}
		})
	}
}
