// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package engine

import (
	"context"
	"image"
	"testing"
)

// TestCanvasDrawnByScriptReachesFinalPaint is the real end-to-end path:
// a <canvas> drawn to by a <script>, composited into the final rendered
// page the exact same way an <img> already is (images.go's loadOneImage
// canvas branch for the pre-script pass, refreshCanvasImages for the
// post-settle one — a canvas has no src, so all of its content comes from
// script running DURING settle, after loadAllImages' own one-shot call
// already ran).
func TestCanvasDrawnByScriptReachesFinalPaint(t *testing.T) {
	html := `<html><body style="margin:0">
		<canvas id="c" width="10" height="10"></canvas>
		<script>
			var ctx = document.getElementById('c').getContext('2d');
			ctx.fillStyle = '#ff0000';
			ctx.fillRect(0, 0, 10, 10);
		</script>
	</body></html>`
	e := &Engine{}
	img, info, err := e.RenderHTML(context.Background(), html, "https://example.com/", image.Rect(0, 0, 20, 20))
	if err != nil {
		t.Fatalf("RenderHTML error: %v", err)
	}
	if info.ContentHeight < 10 {
		t.Fatalf("contentHeight = %d, want at least 10 (the canvas's own height)", info.ContentHeight)
	}
	r, g, b, a := img.At(5, 5).RGBA()
	if r>>8 != 255 || g>>8 != 0 || b>>8 != 0 || a>>8 != 255 {
		t.Fatalf("pixel at (5,5) = (%d,%d,%d,%d), want opaque red — the canvas fill never reached the final page bitmap", r>>8, g>>8, b>>8, a>>8)
	}
}

// TestCanvasGetContextWebGLReturnsNullNotThrow is stripe.com's own confirmed
// real trigger (FIDELITY.md round 39): WebGL-support feature-detection
// calling canvas.getContext("webgl") must get null, not a thrown
// TypeError, or the whole script aborts.
func TestCanvasGetContextWebGLReturnsNullNotThrow(t *testing.T) {
	var logs []string
	html := `<html><body>
		<canvas id="c"></canvas>
		<script>
			var c = document.getElementById('c');
			var gl = c.getContext('webgl');
			console.log('gl=' + gl);
		</script>
	</body></html>`
	e := &Engine{JSLog: func(s string) { logs = append(logs, s) }}
	_, _, err := e.RenderHTML(context.Background(), html, "https://example.com/", image.Rect(0, 0, 20, 20))
	if err != nil {
		t.Fatalf("RenderHTML error: %v", err)
	}
	found := false
	for _, l := range logs {
		if l == "log: gl=null" {
			found = true
		}
		if l != "log: gl=null" {
			t.Errorf("unexpected log line (script should not have thrown): %q", l)
		}
	}
	if !found {
		t.Fatalf("expected 'log: gl=null', got logs=%v", logs)
	}
}
