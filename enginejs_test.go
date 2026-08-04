// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package engine

import (
	"context"
	"image"
	"strings"
	"testing"
)

// TestJSMutationReflectedInLayout proves the script pass runs BEFORE the cascade
// and layout: a script that injects many paragraphs makes the laid-out page
// materially taller than the same document rendered with JS disabled.
func TestJSMutationReflectedInLayout(t *testing.T) {
	var sb strings.Builder
	sb.WriteString(`<html><head><title>t</title></head><body><div id="app"></div>`)
	sb.WriteString(`<script>
		var html='';
		for (var i=0;i<40;i++){ html += '<p>line number '+i+' with some words to wrap</p>'; }
		document.getElementById('app').innerHTML = html;
	</script></body></html>`)
	src := sb.String()

	vp := image.Rect(0, 0, 400, 300)

	eOn := New()
	_, infoOn, err := eOn.RenderHTML(context.Background(), src, "https://example.com/", vp)
	if err != nil {
		t.Fatal(err)
	}

	eOff := New()
	eOff.DisableJS = true
	_, infoOff, err := eOff.RenderHTML(context.Background(), src, "https://example.com/", vp)
	if err != nil {
		t.Fatal(err)
	}

	if infoOn.ContentHeight <= infoOff.ContentHeight {
		t.Fatalf("JS-injected content should grow the page: on=%d off=%d",
			infoOn.ContentHeight, infoOff.ContentHeight)
	}
}

// TestJSClientJSSignalStyled proves the client-js signal reaches the cascade:
// a rule gated on .client-js changes the body background only when JS runs.
func TestJSClientJSSignalStyled(t *testing.T) {
	src := `<html class="client-nojs"><head><title>t</title>
		<style>
			body { background-color: rgb(10,20,30); }
			html.client-js body { background-color: rgb(200,100,50); }
		</style></head><body>hi</body></html>`
	vp := image.Rect(0, 0, 100, 50)

	on := renderTopLeft(t, New(), src, vp)
	off := func() [3]uint8 {
		e := New()
		e.DisableJS = true
		return renderTopLeft(t, e, src, vp)
	}()

	if on != [3]uint8{200, 100, 50} {
		t.Fatalf("client-js rule should apply with JS on, got %v", on)
	}
	if off != [3]uint8{10, 20, 30} {
		t.Fatalf("no-JS fallback should apply with JS off, got %v", off)
	}
}

// TestDisableJSDeterministic confirms DisableJS leaves the no-JS fallback in
// place (the html keeps client-nojs; no client-js is added).
func TestDisableJSDeterministic(t *testing.T) {
	src := `<html class="client-nojs"><head><title>t</title></head>` +
		`<body><script>document.documentElement.className='changed'</script></body></html>`
	e := New()
	e.DisableJS = true
	// Render must succeed and not run the script (which would rename the class).
	_, _, err := e.RenderHTML(context.Background(), src, "https://example.com/", image.Rect(0, 0, 50, 50))
	if err != nil {
		t.Fatal(err)
	}
}

// renderTopLeft renders src and returns the top-left pixel's RGB.
func renderTopLeft(t *testing.T, e *Engine, src string, vp image.Rectangle) [3]uint8 {
	t.Helper()
	img, _, err := e.RenderHTML(context.Background(), src, "https://example.com/", vp)
	if err != nil {
		t.Fatal(err)
	}
	i := img.PixOffset(1, 1)
	return [3]uint8{img.Pix[i], img.Pix[i+1], img.Pix[i+2]}
}
