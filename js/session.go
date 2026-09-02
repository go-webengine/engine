// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package js

import (
	"context"
	"time"

	"github.com/dop251/goja"

	"github.com/go-webengine/engine/dom"
)

// Metrics supplies real used geometry and resolved (used) style values from a
// completed cascade+layout pass. The DOM binding reads it so that
// getBoundingClientRect / offset* / client* / scroll* and
// window.getComputedStyle answer with real numbers instead of zeros — the
// signal responsive scripts and MediaWiki's mw.loader consult to decide what to
// collapse or reveal. A nil Metrics (the legacy js.Run path) makes those APIs
// report zeros / inline styles, exactly as before.
type Metrics interface {
	// Rect returns the used border-box rectangle (document coords, CSS px) of n.
	// ok is false when n was not laid out (display:none or detached).
	Rect(n *dom.Node) (x, y, w, h float64, ok bool)
	// Computed returns the resolved used value of a CSS property (lower-case,
	// hyphenated — e.g. "width", "display", "margin-top", "font-size"). ok is
	// false for a property the resolver does not model, matching a browser
	// returning "" for an unsupported computed property.
	Computed(n *dom.Node, prop string) (string, bool)
}

// Session is a live page-script execution bound to one DOM tree and one goja
// runtime. Unlike the one-shot Run, it stays resident across layout passes: the
// engine lays the page out, feeds the geometry in via SetMetrics, runs scripts
// (which may read that geometry and mutate the DOM / inject <script>/<style>),
// re-lays-out, and runs any newly-injected scripts — iterating to a bounded
// fixpoint before the final paint.
type Session struct {
	b    *binder
	res  Result
	stop func()
	done bool
}

// Begin builds the DOM/BOM binding on a fresh runtime, sets the client-js
// signal, and starts the wall-clock/interrupt watchdog. The caller MUST call
// Close to release the watchdog goroutine. A binding-setup panic is contained;
// Begin always returns a usable (possibly inert) Session.
func Begin(root *dom.Node, opt Options) *Session {
	if opt.Ctx == nil {
		opt.Ctx = context.Background()
	}
	if opt.Timeout <= 0 {
		opt.Timeout = DefaultTimeout
	}
	if opt.ViewportWidth <= 0 {
		opt.ViewportWidth = 1024
	}
	if opt.ViewportHeight <= 0 {
		opt.ViewportHeight = 768
	}

	b := &binder{
		vm:         goja.New(),
		root:       root,
		opt:        opt,
		cache:      map[*dom.Node]*goja.Object{},
		windowNode: &dom.Node{Type: dom.Element, Tag: "#window"},
		docNode:    root,
		listeners:  map[*dom.Node]map[string][]goja.Value{},
		storage:    map[string]*storageArea{},
		executed:   map[*dom.Node]bool{},
		deadman:    time.Now().Add(opt.Timeout),
	}
	s := &Session{b: b}

	func() {
		defer func() {
			if r := recover(); r != nil {
				b.logf("panic: %v", r)
			}
		}()
		b.setClientJS()
		b.install()
	}()

	// One watchdog for the whole session: interrupt a runaway script once the
	// budget is spent or the caller's context is cancelled. Stopped by Close.
	timer := time.AfterFunc(opt.Timeout, func() { b.vm.Interrupt("script budget exceeded") })
	done := make(chan struct{})
	go func() {
		select {
		case <-opt.Ctx.Done():
			b.vm.Interrupt("context cancelled")
		case <-done:
		}
	}()
	s.stop = func() {
		timer.Stop()
		close(done)
	}
	return s
}

// SetMetrics installs (or replaces) the geometry/used-value source the DOM
// binding reads back. Call it after each layout pass, before running scripts.
func (s *Session) SetMetrics(m Metrics) { s.b.metrics = m }

// EventInit carries the extra fields a synthetic event needs beyond
// type/target — which ones matter depends on typ, mirroring the real DOM
// (Key for keydown/keyup, Data/InputType for input). Bubbles/Cancelable
// default to false; a caller sets them per the real event's own defaults
// (e.g. click/keydown/keyup/input/change/submit all bubble in a real
// browser — Dispatch does not assume this for you, since it also serves
// dispatchEvent-style callers that want an exact, spec-shaped event).
type EventInit struct {
	Bubbles    bool
	Cancelable bool
	Key        string // keydown / keyup
	Data       string // input
	InputType  string // input
}

// Dispatch fires a synthetic DOM event of type typ on n — the seam a host
// outside this package uses to synthesize real user interaction (focus, a
// keystroke, a click) against the live session, exactly as if the page's
// own script had called element.dispatchEvent(). It bubbles per init.
// Bubbles (see binder.dispatch) and reports whether a listener called
// preventDefault(), so a caller (e.g. native form submission) can honor it.
// Contained against a listener panic, like every other script execution
// path in this package.
func (s *Session) Dispatch(n *dom.Node, typ string, init EventInit) (defaultPrevented bool) {
	var ev *goja.Object
	s.guard(func() {
		ev = s.b.newEvent(typ)
		ev.Set("bubbles", init.Bubbles)
		ev.Set("cancelable", init.Cancelable)
		if init.Key != "" {
			ev.Set("key", init.Key)
		}
		if init.Data != "" {
			ev.Set("data", init.Data)
		}
		if init.InputType != "" {
			ev.Set("inputType", init.InputType)
		}
		s.b.dispatch(n, typ, ev)
	})
	return ev != nil && ev.Get("defaultPrevented").ToBoolean()
}

// RunInitial executes every page <script> in document order, then dispatches
// DOMContentLoaded/load, draining queued timers/promises/XHR callbacks to
// quiescence within the budget. Contained against panics.
func (s *Session) RunInitial() {
	s.guard(func() {
		s.b.runScripts(&s.res)
		s.b.dispatchLifecycle(&s.res)
	})
}

// RunPending executes any <script> elements inserted into the DOM since the last
// run (the core of a ResourceLoader-style dynamic loader) in document order,
// then drains the async loop. It reports whether at least one new script ran.
func (s *Session) RunPending() (ran bool) {
	s.guard(func() {
		before := s.res.ScriptsRun + s.res.ScriptsFailed
		s.b.runScripts(&s.res)
		if s.res.ScriptsRun+s.res.ScriptsFailed > before {
			ran = true
		}
		s.b.drainTimers(&s.res)
	})
	return ran
}

// Result returns the cumulative execution tally.
func (s *Session) Result() Result { return s.res }

// Close stops the watchdog. It is idempotent.
func (s *Session) Close() {
	if s.done {
		return
	}
	s.done = true
	if s.stop != nil {
		s.stop()
	}
}

// guard runs fn with a recover so no binding bug crashes the host render.
func (s *Session) guard(fn func()) {
	defer func() {
		if r := recover(); r != nil {
			s.b.logf("panic: %v", r)
		}
	}()
	fn()
}

// MarkJSEnabled sets the client-js signal on the document root (swapping the
// client-nojs no-JS fallback for client-js on <html>) without running any
// script. The engine calls it before the INITIAL cascade so the first layout —
// the geometry scripts read back — already reflects a JS-enabled browser.
func MarkJSEnabled(root *dom.Node) {
	if root == nil {
		return
	}
	(&binder{root: root}).setClientJS()
}
