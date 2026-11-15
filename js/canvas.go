// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package js

import (
	"math"
	"strconv"
	"strings"

	"github.com/dop251/goja"
	"github.com/go-webengine/engine/css"
	"github.com/go-webengine/engine/dom"
	"github.com/go-widgets/painter"
)

// defineCanvas adds <canvas>'s own surface — getContext and the width/
// height IDL accessors — to an element wrapper defineElement already built.
// Scoped to a confirmed real need, found live on stripe.com: Three.js
// generates small offscreen 2D canvases as WebGL textures via exactly
// beginPath/moveTo/lineTo/arc/rect/fill/fillRect/fillStyle/clearRect, and
// several WebGL-support feature-detection paths call
// canvas.getContext("webgl") first and must get null back — not a thrown
// TypeError — to fall through gracefully. Deliberately NOT here, no
// confirmed trigger yet: stroke()/strokeStyle, gradients/patterns,
// drawImage, getImageData/putImageData/toDataURL, save/restore, transforms
// (translate/scale/rotate), and text.
func (b *binder) defineCanvas(o *goja.Object, n *dom.Node) {
	b.accessor(o, "width",
		func() goja.Value { return b.vm.ToValue(canvasWidth(n)) },
		func(v goja.Value) { b.setCanvasDim(n, "width", int(v.ToInteger())) })
	b.accessor(o, "height",
		func() goja.Value { return b.vm.ToValue(canvasHeight(n)) },
		func(v goja.Value) { b.setCanvasDim(n, "height", int(v.ToInteger())) })

	o.Set("getContext", func(call goja.FunctionCall) goja.Value {
		if call.Argument(0).String() != "2d" {
			// Every OTHER context type (webgl, webgl2, bitmaprenderer, …) is
			// a real, named "not supported" outcome per the HTML Standard's
			// own getContext algorithm — null, never a thrown error.
			return goja.Null()
		}
		if ctx, ok := b.canvasCtx[n]; ok {
			return ctx
		}
		ctx := b.newCanvas2D(n)
		if b.canvasCtx == nil {
			b.canvasCtx = map[*dom.Node]*goja.Object{}
		}
		b.canvasCtx[n] = ctx
		return ctx
	})
}

// canvasWidth/canvasHeight are the IDL width/height getters: the width/
// height content attribute if it parses as a non-negative integer, else the
// HTML Standard's own 300x150 default (§4.12.5).
func canvasWidth(n *dom.Node) int  { return canvasDim(n, "width", 300) }
func canvasHeight(n *dom.Node) int { return canvasDim(n, "height", 150) }

func canvasDim(n *dom.Node, name string, def int) int {
	v, ok := n.Attribute(name)
	if !ok {
		return def
	}
	i, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || i < 0 {
		return def
	}
	return i
}

// setCanvasDim backs the width/height IDL setters: HTML Standard §4.12.5 —
// set the corresponding content attribute, then the bitmap (if any exists
// yet) is cleared to fully transparent black. A resize always discards
// whatever was drawn, matching a real browser exactly.
func (b *binder) setCanvasDim(n *dom.Node, name string, v int) {
	if v < 0 {
		v = 0
	}
	b.setAttr(n, name, strconv.Itoa(v))
	n.CanvasPixels = nil
	n.CanvasW, n.CanvasH = 0, 0
}

// newCanvas2D builds one <canvas>'s CanvasRenderingContext2D: a mutable
// outline (go-widgets/painter's own Path, the exact outline builder this
// engine's paint package already rasterises SVG/clip-path/list-marker
// shapes with) plus the one paint style a real caller sets before filling,
// fillStyle. beginPath/moveTo/lineTo/closePath/rect/arc build the outline;
// fill()/fillRect() rasterise it (or a plain rect) into the canvas's own
// pixel buffer, lazily allocated at its CURRENT width/height the first time
// anything is drawn — matching a real browser's "the bitmap exists once you
// ask for a context," not before.
func (b *binder) newCanvas2D(n *dom.Node) *goja.Object {
	ctx := b.vm.NewObject()
	path := painter.NewPath()
	fillColor := css.Color{A: 255} // opaque black: the spec's own fillStyle default
	fillStr := "#000000"

	// ensure allocates/returns a PixelPainter over the canvas's own pixel
	// buffer, sized to its CURRENT width/height — re-allocating if a script
	// resized the element since the buffer was last (re)made. A 0-sized
	// canvas (width or height set to 0) has nothing to paint into.
	ensure := func() *painter.PixelPainter {
		w, h := canvasWidth(n), canvasHeight(n)
		if w <= 0 || h <= 0 {
			return nil
		}
		if len(n.CanvasPixels) != w*h*4 {
			n.CanvasPixels = make([]byte, w*h*4)
			n.CanvasW, n.CanvasH = w, h
		}
		return painter.NewPixelPainter(n.CanvasPixels, w, h)
	}

	ctx.Set("beginPath", func(goja.FunctionCall) goja.Value {
		path = painter.NewPath()
		return goja.Undefined()
	})
	ctx.Set("closePath", func(goja.FunctionCall) goja.Value {
		path.Close()
		return goja.Undefined()
	})
	ctx.Set("moveTo", func(call goja.FunctionCall) goja.Value {
		path.MoveTo(call.Argument(0).ToFloat(), call.Argument(1).ToFloat())
		return goja.Undefined()
	})
	ctx.Set("lineTo", func(call goja.FunctionCall) goja.Value {
		path.LineTo(call.Argument(0).ToFloat(), call.Argument(1).ToFloat())
		return goja.Undefined()
	})
	ctx.Set("rect", func(call goja.FunctionCall) goja.Value {
		x, y := call.Argument(0).ToFloat(), call.Argument(1).ToFloat()
		w, h := call.Argument(2).ToFloat(), call.Argument(3).ToFloat()
		path.MoveTo(x, y).LineTo(x+w, y).LineTo(x+w, y+h).LineTo(x, y+h).Close()
		return goja.Undefined()
	})
	ctx.Set("arc", func(call goja.FunctionCall) goja.Value {
		cx, cy := call.Argument(0).ToFloat(), call.Argument(1).ToFloat()
		r := call.Argument(2).ToFloat()
		start, end := call.Argument(3).ToFloat(), call.Argument(4).ToFloat()
		ccw := call.Argument(5).ToBoolean()
		arcToBezier(path, cx, cy, r, start, end, ccw)
		return goja.Undefined()
	})
	ctx.Set("fill", func(goja.FunctionCall) goja.Value {
		if pp := ensure(); pp != nil {
			pp.FillPath(path, painterRGBA(fillColor), painter.NonZero)
		}
		return goja.Undefined()
	})
	ctx.Set("fillRect", func(call goja.FunctionCall) goja.Value {
		if pp := ensure(); pp != nil {
			r := painter.Rect{
				X: int(call.Argument(0).ToFloat()), Y: int(call.Argument(1).ToFloat()),
				W: int(call.Argument(2).ToFloat()), H: int(call.Argument(3).ToFloat()),
			}
			pp.FillRect(r, painterRGBA(fillColor))
		}
		return goja.Undefined()
	})
	ctx.Set("clearRect", func(call goja.FunctionCall) goja.Value {
		if len(n.CanvasPixels) > 0 {
			clearCanvasRect(n, call.Argument(0).ToFloat(), call.Argument(1).ToFloat(),
				call.Argument(2).ToFloat(), call.Argument(3).ToFloat())
		}
		return goja.Undefined()
	})
	b.accessor(ctx, "fillStyle",
		func() goja.Value { return b.vm.ToValue(fillStr) },
		func(v goja.Value) {
			s := v.String()
			if c, ok := css.ParseColor(s); ok {
				fillColor, fillStr = c, s
			}
		})
	b.accessor(ctx, "canvas", func() goja.Value { return b.wrap(n) }, nil)
	return ctx
}

// painterRGBA converts this engine's own 8-bit colour type to painter's
// identical-shaped one — a field-for-field relabelling, not a conversion.
func painterRGBA(c css.Color) painter.RGBA {
	return painter.RGBA{R: c.R, G: c.G, B: c.B, A: c.A}
}

// clearCanvasRect sets every pixel in [x,y,x+w,y+h) (clamped to the canvas's
// own bounds) to fully transparent black — Canvas2D's clearRect, which,
// unlike fillRect, is NOT a composite-over (painter.Painter has no such
// primitive: filling with a transparent colour would correctly do nothing,
// per the Porter-Duff "over" operator fillRect's own compositing already
// uses). Writes the raw buffer directly, the same shape as paint/vector.go's
// own ink-clearing after a raster fallback.
func clearCanvasRect(n *dom.Node, x, y, w, h float64) {
	cw, ch := n.CanvasW, n.CanvasH
	x0, y0 := clampInt(x, 0, cw), clampInt(y, 0, ch)
	x1, y1 := clampInt(x+w, 0, cw), clampInt(y+h, 0, ch)
	if x1 <= x0 || y1 <= y0 {
		return
	}
	stride := cw * 4
	for row := y0; row < y1; row++ {
		clear(n.CanvasPixels[row*stride+x0*4 : row*stride+x1*4])
	}
}

func clampInt(v float64, lo, hi int) int {
	i := int(v)
	if i < lo {
		return lo
	}
	if i > hi {
		return hi
	}
	return i
}

// arcToBezier appends a circular arc (Canvas2D's arc(x, y, radius,
// startAngle, endAngle, counterclockwise), HTML/CSSOM Canvas §4.12.5.1.14)
// to path as cubic Bezier segments of at most 90 degrees each, via the
// standard 4/3*tan(theta/4) control-point approximation — the same
// construction real browsers use for this exact call. Always starts a new
// subpath at the arc's own first point; no confirmed real caller chains an
// arc onto a preceding moveTo/lineTo point instead.
func arcToBezier(path *painter.Path, cx, cy, r, start, end float64, ccw bool) {
	if r <= 0 {
		return
	}
	sweep := end - start
	switch {
	case ccw && sweep > 0:
		sweep -= 2 * math.Pi * math.Ceil(sweep/(2*math.Pi))
	case !ccw && sweep < 0:
		sweep += 2 * math.Pi * math.Ceil(-sweep/(2*math.Pi))
	}
	if math.Abs(sweep) > 2*math.Pi {
		sweep = math.Copysign(2*math.Pi, sweep)
	}
	path.MoveTo(cx+r*math.Cos(start), cy+r*math.Sin(start))
	const maxSeg = math.Pi / 2
	segs := int(math.Ceil(math.Abs(sweep) / maxSeg))
	if segs < 1 {
		segs = 1
	}
	step := sweep / float64(segs)
	a0 := start
	for i := 0; i < segs; i++ {
		a1 := a0 + step
		k := 4.0 / 3.0 * math.Tan(step/4)
		x0, y0 := cx+r*math.Cos(a0), cy+r*math.Sin(a0)
		x1, y1 := cx+r*math.Cos(a1), cy+r*math.Sin(a1)
		c1x, c1y := x0-k*r*math.Sin(a0), y0+k*r*math.Cos(a0)
		c2x, c2y := x1+k*r*math.Sin(a1), y1-k*r*math.Cos(a1)
		path.CubicTo(c1x, c1y, c2x, c2y, x1, y1)
		a0 = a1
	}
}
