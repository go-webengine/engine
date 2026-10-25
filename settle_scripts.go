// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

//go:build !webengine_nojs

package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/go-webengine/engine/dom"
	"github.com/go-webengine/engine/js"
	"github.com/go-webengine/engine/paint"
)

// jsSession is a page's live script session (nil when DisableJS is set).
type jsSession = js.Session

// markJSEnabled sets the client-js signal on the document root before the
// first cascade (see js.MarkJSEnabled).
func markJSEnabled(root *dom.Node) { js.MarkJSEnabled(root) }

// settle runs the layout↔JS feedback loop: it starts a JS Session against the
// already-laid-out DOM, feeds it the real geometry, runs the page scripts, then
// iterates cascade→layout→inject-scripts to a bounded fixpoint, updating rp in
// place. It is deterministic and always terminates (pass cap + time budget).
// onStage (when non-nil, from a progressive render) is called with "settle"
// after each pass that re-laid-out the document; the caller dedups against the
// previous emitted frame's geometry.
//
// settle returns the live Session rather than closing it: a one-shot render
// (renderCoreStaged) closes it immediately after, byte-identical to before;
// Engine.Open keeps it alive instead, across separate later interactions, via
// LiveDocument (interactive.go) — the only reason the return value exists.
func (e *Engine) settle(ctx context.Context, doc *Document, vpW, vpH int, fonts *paint.Fonts, rp *renderPass, initialLayout time.Duration, onStage func(stage string, rp *renderPass)) *js.Session {
	sess := js.Begin(doc.Root, e.jsOptions(ctx, doc, vpW, vpH))

	sess.SetMetrics(e.liveMetrics(doc, rp, fonts, vpW, vpH))
	// Signature of the DOM the INITIAL layout was computed from (client-js already
	// applied), captured before the scripts run so a mutation is detectable.
	s := newSettleState(ctx, doc, initialLayout)
	sess.RunInitialScripts()

	// ES modules run after classic scripts (they are deferred). goja cannot run a
	// module graph natively, so transpile the page's <script type="module"> graph
	// to one classic IIFE with esbuild, inject it as a classic script, and run it
	// through the same pass. Strictly gated: a page with no module scripts skips
	// this entirely. Best-effort — a bundle failure leaves the page as-is.
	//
	// Deliberately BEFORE DispatchLifecycle below, not after: a classic script
	// above may have just created the page's <script type="module"> element(s)
	// itself (a hand-rolled ResourceLoader, e.g. pkg.go.dev's own loadScript()
	// helper) whose own top-level code registers a window "load" listener —
	// see DispatchLifecycle's own doc comment for why that listener must get a
	// chance to register before "load" fires, not after.
	if !e.DisableJS {
		if bundled, mstats, ok := e.bundleModuleScripts(ctx, doc); ok {
			e.jslog(fmt.Sprintf("modules: bundled %d entr(y/ies) -> %d modules, %d->%d bytes in %s",
				mstats.entries, mstats.fetched, mstats.bytesIn, mstats.bytesOut, mstats.wallClock))
			injectBundledScript(doc.Root, bundled)
		} else if mstats.entries > 0 {
			e.jslog(fmt.Sprintf("modules: %d module script(s) not bundled (fetched=%d, errors=%d, first=%q)",
				mstats.entries, mstats.fetched, mstats.errors, mstats.firstErr))
		}
	}
	// Unconditional, not just after a successful module bundle: a classic
	// script above may equally well have appended a plain, non-module <script>
	// of its own (the same hand-rolled-ResourceLoader idiom, e.g. pkg.go.dev's
	// loadScript() also loads a plain dialog-polyfill.js this way) — it too
	// needs to run before "load" fires, for the same reason.
	sess.RunPending()
	sess.DispatchLifecycle()

	// The links are those the scripts left: a <link> a script added is not
	// refetched by the first pass (as before the loop was shared).
	s.prevLinks = linkKey(doc.Root)
	for pass := 0; pass < maxSettlePasses; pass++ {
		if !e.relayoutPass(ctx, doc, vpW, vpH, fonts, rp, s, onStage) {
			break
		}
		sess.SetMetrics(e.liveMetrics(doc, rp, fonts, vpW, vpH))
		// Run any <script> injected since the last pass (a ResourceLoader chain).
		// If none ran, a layout change alone triggers no further JS, so the state
		// just computed is final.
		if !sess.RunPending() {
			break
		}
	}
	e.finishSettle(ctx, doc, vpW, vpH, fonts, rp, s)
	return sess
}

// jsOptions builds the Options for a Session from the engine config.
func (e *Engine) jsOptions(ctx context.Context, doc *Document, vpW, vpH int) js.Options {
	return js.Options{
		PageURL:          doc.URL,
		UserAgent:        e.UserAgent,
		Client:           e.Client,
		Ctx:              ctx,
		Timeout:          e.JSTimeout,
		ViewportWidth:    vpW,
		ViewportHeight:   vpH,
		DevicePixelRatio: e.devScale(),
		Log:              e.JSLog,
	}
}
