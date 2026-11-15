// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package js

import "testing"

func canvasPage(script string) string {
	return `<html><body><canvas id="c"></canvas><script>` + script + `</script></body></html>`
}

func TestCanvasGetContextReturnsSameObjectEachCall(t *testing.T) {
	_, logs, _ := runJS(t, canvasPage(`
		var c = document.getElementById('c');
		var a = c.getContext('2d');
		var b = c.getContext('2d');
		console.log('same=' + (a === b));
	`))
	mustHave(t, logs, "same=true")
}

// TestCanvasGetContextUnsupportedTypeReturnsNull is the real, confirmed
// trigger (stripe.com's own WebGL feature-detection, FIDELITY.md round 39):
// an unsupported context type must come back null, never throw, so a page's
// own feature-detection falls through gracefully instead of crashing the
// whole script.
func TestCanvasGetContextUnsupportedTypeReturnsNull(t *testing.T) {
	_, logs, res := runJS(t, canvasPage(`
		var c = document.getElementById('c');
		console.log('webgl=' + (c.getContext('webgl') === null));
		console.log('bogus=' + (c.getContext('nonsense') === null));
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	mustHave(t, logs, "webgl=true", "bogus=true")
}

func TestCanvasWidthHeightDefaultsAndReflection(t *testing.T) {
	root, logs, _ := runJS(t, canvasPage(`
		var c = document.getElementById('c');
		console.log('default=' + c.width + 'x' + c.height);
		c.width = 64;
		c.height = 32;
		console.log('set=' + c.width + 'x' + c.height);
	`))
	mustHave(t, logs, "default=300x150", "set=64x32")
	n := byID(root, "c")
	if v, _ := n.Attribute("width"); v != "64" {
		t.Errorf(`width attribute = %q, want "64"`, v)
	}
	if v, _ := n.Attribute("height"); v != "32" {
		t.Errorf(`height attribute = %q, want "32"`, v)
	}
}

// TestCanvasFillRectWritesOpaquePixels checks the drawing pipeline end to
// end at the pixel level: fillStyle, fillRect, and the resulting
// dom.Node.CanvasPixels buffer this engine's paint package reads at
// composite time (canvas.go's refreshCanvasImages).
func TestCanvasFillRectWritesOpaquePixels(t *testing.T) {
	root, _, res := runJS(t, canvasPage(`
		var c = document.getElementById('c');
		c.width = 4; c.height = 4;
		var ctx = c.getContext('2d');
		ctx.fillStyle = '#ff0000';
		ctx.fillRect(0, 0, 4, 4);
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v", res)
	}
	n := byID(root, "c")
	if n.CanvasW != 4 || n.CanvasH != 4 {
		t.Fatalf("CanvasW/H = %dx%d, want 4x4", n.CanvasW, n.CanvasH)
	}
	if len(n.CanvasPixels) != 4*4*4 {
		t.Fatalf("CanvasPixels len = %d, want %d", len(n.CanvasPixels), 4*4*4)
	}
	// Every pixel should be opaque red: R=255 G=0 B=0 A=255.
	for i := 0; i < len(n.CanvasPixels); i += 4 {
		r, g, b, a := n.CanvasPixels[i], n.CanvasPixels[i+1], n.CanvasPixels[i+2], n.CanvasPixels[i+3]
		if r != 255 || g != 0 || b != 0 || a != 255 {
			t.Fatalf("pixel %d = (%d,%d,%d,%d), want (255,0,0,255)", i/4, r, g, b, a)
		}
	}
}

// TestCanvasClearRectZeroesPixels is TestCanvasFillRectWritesOpaquePixels's
// own control: clearRect must zero every channel including alpha, which a
// composite-over fill with a transparent colour would NOT do (see
// clearCanvasRect's own doc comment for why this can't just call FillRect).
func TestCanvasClearRectZeroesPixels(t *testing.T) {
	root, _, res := runJS(t, canvasPage(`
		var c = document.getElementById('c');
		c.width = 2; c.height = 2;
		var ctx = c.getContext('2d');
		ctx.fillStyle = 'blue';
		ctx.fillRect(0, 0, 2, 2);
		ctx.clearRect(0, 0, 2, 2);
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v", res)
	}
	n := byID(root, "c")
	for i, p := range n.CanvasPixels {
		if p != 0 {
			t.Fatalf("byte %d = %d, want 0 (clearRect should zero everything)", i, p)
		}
	}
}

// TestCanvasArcFillDrawsACircle exercises the real confirmed call shape
// (stripe.com's own Three.js canvas-texture generator): beginPath/arc/fill.
// A circle inscribed in an 8x8 canvas should paint its centre but leave the
// corners untouched.
func TestCanvasArcFillDrawsACircle(t *testing.T) {
	root, _, res := runJS(t, canvasPage(`
		var c = document.getElementById('c');
		c.width = 8; c.height = 8;
		var ctx = c.getContext('2d');
		ctx.fillStyle = 'red';
		ctx.beginPath();
		ctx.arc(4, 4, 3.5, 0, 2 * Math.PI);
		ctx.fill();
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v", res)
	}
	n := byID(root, "c")
	at := func(x, y int) byte { return n.CanvasPixels[(y*n.CanvasW+x)*4+3] } // alpha
	if at(4, 4) == 0 {
		t.Error("centre pixel should be painted (alpha > 0)")
	}
	if at(0, 0) != 0 {
		t.Error("corner pixel should be untouched (alpha == 0)")
	}
}

// TestCanvasFillStyleRoundTrips checks the getter returns exactly what the
// setter was given, and that an unparseable value leaves the previous style
// in place rather than silently going black or throwing.
func TestCanvasFillStyleRoundTrips(t *testing.T) {
	_, logs, res := runJS(t, canvasPage(`
		var ctx = document.getElementById('c').getContext('2d');
		console.log('initial=' + ctx.fillStyle);
		ctx.fillStyle = '#ff0000';
		console.log('after=' + ctx.fillStyle);
		ctx.fillStyle = 'not-a-colour';
		console.log('unchanged=' + ctx.fillStyle);
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	mustHave(t, logs, "initial=#000000", "after=#ff0000", "unchanged=#ff0000")
}

// TestCanvasResizeClearsPixels is the HTML Standard's own §4.12.5 behaviour:
// setting width/height discards whatever was drawn.
func TestCanvasResizeClearsPixels(t *testing.T) {
	root, _, res := runJS(t, canvasPage(`
		var c = document.getElementById('c');
		c.width = 4; c.height = 4;
		var ctx = c.getContext('2d');
		ctx.fillStyle = 'red';
		ctx.fillRect(0, 0, 4, 4);
		c.width = 4; // same value, still a resize per spec
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v", res)
	}
	n := byID(root, "c")
	if n.CanvasPixels != nil {
		t.Fatal("CanvasPixels should be nil after a width/height set, even to the same value")
	}
}
