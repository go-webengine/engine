// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package engine

import (
	"context"
	"image"
	"strconv"
	"strings"

	"github.com/go-webengine/engine/dom"
)

// iframeDepthKey is the context key carrying how many <iframe> levels deep
// the current render already is. A struct{}-valued, unexported key type
// (not a string) so no other package's context.WithValue can collide with
// it — the same shape images.go's own imgByteCacheKey already uses.
type iframeDepthKey struct{}

// maxIframeDepth bounds how many levels of <iframe src="..."> this engine
// will recurse into: an iframe's OWN nested iframes are never rendered.
// Without this, a page embedding itself (deliberately or by accident, the
// same class of hazard a tracking pixel or an ad network's own iframe chain
// can create) would recurse until some OTHER bound (MaxRequests, a render
// timeout) eventually stopped it — bounded on purpose, at the cheapest
// possible point, rather than relying on a slower backstop to catch it.
const maxIframeDepth = 1

func iframeDepth(ctx context.Context) int {
	d, _ := ctx.Value(iframeDepthKey{}).(int)
	return d
}

func withIframeDepth(ctx context.Context, d int) context.Context {
	return context.WithValue(ctx, iframeDepthKey{}, d)
}

// loadIframe renders one <iframe> element's own document — fetched from its
// `src`, or parsed directly from its `srcdoc` — into a bitmap sized to the
// iframe's own width/height (the HTML Standard's own 300x150 default,
// §4.8.5, the SAME default <canvas> has — see layout.imageSize). It reuses
// this engine's own top-level Fetch/RenderDocument pipeline recursively, so
// an iframe's content gets the exact same cascade/layout/paint (and,
// unlike an ordinary <img>, its own JavaScript) a top-level page does — not
// a narrower, special-cased renderer.
//
// Deliberately scoped, matching every other replaced-element limit in this
// file: an iframe whose `src`/`srcdoc` is set or changed by script AFTER
// the initial pre-script image-loading pass (loadAllImages, which this is
// called from) is not re-rendered — the same disclosed boundary a
// script-swapped `<img src>` already has, not a new one introduced here.
// `src="about:blank"` (or no src/srcdoc at all) renders nothing, matching a
// real browser's own empty initial document.
// replacedDim parses a width/height content attribute as a non-negative
// integer, or def if absent/invalid — the HTML Standard's own default for
// <canvas> (js/canvas.go's canvasDim) and <iframe> alike (both default to
// 300x150, HTML Standard §4.8.5/§4.12.5); kept as its own small copy here
// rather than exported across the js/engine package boundary for one
// three-line helper.
func replacedDim(n *dom.Node, name string, def int) int {
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

func (e *Engine) loadIframe(ctx context.Context, baseURL string, n *dom.Node) *LoadedImage {
	if iframeDepth(ctx) >= maxIframeDepth {
		return nil
	}
	w, h := replacedDim(n, "width", 300), replacedDim(n, "height", 150)
	if w <= 0 || h <= 0 {
		return nil
	}
	nested := withIframeDepth(ctx, iframeDepth(ctx)+1)

	var doc *Document
	if srcdoc, ok := n.Attribute("srcdoc"); ok {
		root, err := dom.Parse(srcdoc)
		if err != nil {
			return nil
		}
		doc = &Document{URL: baseURL, Root: root, HTML: srcdoc}
	} else if src, ok := n.Attribute("src"); ok && src != "" {
		abs, ok := resolveURL(baseURL, src)
		if !ok || abs == "about:blank" {
			return nil
		}
		d, err := e.Fetch(nested, abs)
		if err != nil {
			return nil
		}
		doc = d
	} else {
		return nil
	}

	// Keep the nested tree reachable from the iframe's own node, independent
	// of whether painting it succeeds below — js/iframe.go's
	// contentDocument/contentWindow read this directly, genuinely live DOM
	// access to the same nodes the render used, not a snapshot.
	n.IframeDoc = doc.Root

	img, _, err := e.RenderDocument(nested, doc, image.Rect(0, 0, w, h))
	if err != nil {
		return nil
	}
	return &LoadedImage{Size: [2]float64{float64(w), float64(h)}, Bitmap: img}
}
