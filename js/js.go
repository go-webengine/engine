// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

// Package js runs a page's JavaScript against a minimal but real DOM binding on
// a pure-Go goja ECMAScript runtime. It is deliberately not a full WHATWG
// implementation: the goal is progressive-enhancement and light hydration —
// class toggling, attribute/style mutation, small tree edits — so that the
// subsequent CSS cascade and layout see what a real browser would.
//
// All mutations write through to the shared dom.Node tree. Execution is bounded
// by a wall-clock budget and a script error never propagates: it is caught,
// optionally logged, and the render continues.
package js

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/dop251/goja"
	"github.com/tetratelabs/wazero"

	"github.com/go-webengine/engine/dom"
)

// Options configures a Run.
type Options struct {
	// PageURL is the document's final URL, used to resolve script src and to
	// populate window.location.
	PageURL string
	// UserAgent is exposed as navigator.userAgent and sent when fetching scripts.
	UserAgent string
	// Client fetches external <script src>. When nil, external scripts are skipped
	// (inline scripts still run).
	Client *http.Client
	// Ctx bounds network fetches; its cancellation also stops script execution.
	Ctx context.Context
	// Timeout is the total wall-clock budget for all scripts and drained timers.
	// Zero selects DefaultTimeout.
	Timeout time.Duration
	// ViewportWidth / ViewportHeight back window.innerWidth / innerHeight.
	ViewportWidth  int
	ViewportHeight int
	// DevicePixelRatio backs window.devicePixelRatio and the `resolution`
	// feature of matchMedia (CSSOM View; Media Queries 4 §5.1). 0 means 1.
	DevicePixelRatio float64
	// Log, if non-nil, receives console.* output as "level: message" lines.
	Log func(string)
	// MaxWasmModuleBytes bounds a WebAssembly.compile()/instantiate() call's
	// input size; zero selects defaultMaxWasmModuleBytes. See the field of
	// the same name on the root engine.Engine (the public-facing doc
	// comment) for the full rationale.
	MaxWasmModuleBytes int64
	// MaxWasmMemoryBytes bounds a WebAssembly module's own linear memory
	// growth; zero selects defaultMaxWasmMemoryBytes. See engine.Engine's
	// own field of the same name for the full rationale.
	MaxWasmMemoryBytes int64
	// EnableDoneSignal installs a global function (see install()'s own doc
	// comment for its exact name) a page's own script can call to end the
	// settle loop early, once its async work is actually finished, instead
	// of running every pass out to the pass cap or the wall-clock budget.
	// False (the default) installs nothing — no behaviour change for a
	// caller not using this feature. See engine.Engine's own field of the
	// same name for the full rationale.
	EnableDoneSignal bool
	// CrossOriginIsolated reports whether the top document's own response
	// satisfied the HTML Standard's COOP/COEP requirement — see
	// engine.Document.CrossOriginIsolated's own doc comment for the full
	// rationale and engine.Engine.ForceCrossOriginIsolated for the escape
	// hatch. False (the default) means SharedArrayBuffer/Atomics are not
	// installed at all (window.SharedArrayBuffer is undefined, matching a
	// real browser exactly) and window.crossOriginIsolated reads false.
	CrossOriginIsolated bool
	// RealTimeTimers gates a timer job's own FIRST firing by its real
	// requested delay (time.Now()+delay), not just "whenever drainTimers
	// next looks at it" — the one deliberate, OPT-IN exception to this
	// package's own long-documented "not a real clock" timer model (see
	// drainTimers' own doc comment). Off by default: every existing
	// one-shot render keeps today's exact "fire at the next opportunity"
	// behaviour, which is correct and desired there (no page has any way to
	// observe the difference within one bounded settle pass anyway). On
	// (mapped from engine.Engine.EnableWorkerStreaming — see its own field
	// for the full rationale), a page's own long-delay "safety net"
	// setTimeout (an extremely common, idiomatic JS pattern: race a
	// near-immediate retry against a much-later give-up timer) genuinely
	// waits that long instead of firing almost instantly — confirmed as a
	// real, blocking bug by go-wasmcontainers' own e2e probe: their
	// `setTimeout(giveUp, 15000)` fired within milliseconds, ending their
	// streaming-worker test before more than one message could arrive, a
	// render that completed in under 4ms instead of taking the 15 real
	// seconds its own JS expected. setInterval's own REPEAT firings
	// already had this same real gating unconditionally (shipped earlier,
	// unaffected by this field) — this extends it to a job's first firing
	// too, but only under this opt-in, so the ordinary bounded-render case
	// is untouched.
	RealTimeTimers bool
	// OPFSRoot, when non-empty, enables navigator.storage.getDirectory() and
	// names the real on-disk directory it is scoped under — see
	// engine.Engine.OPFSRoot's own doc comment for the full rationale
	// (including the security caveat this capability needed before
	// shipping, engine#344). Empty (the default) installs nothing: a
	// script's `'getDirectory' in navigator.storage` reads false, matching
	// a browser with no persistent-storage access granted.
	OPFSRoot string
	// MaxOPFSBytes bounds a single origin's total OPFS usage; zero selects
	// defaultMaxOPFSBytes, negative disables the guard. See
	// engine.Engine.MaxOPFSBytes for the full rationale.
	MaxOPFSBytes int64
}

// DefaultTimeout is the script budget when Options.Timeout is zero.
const DefaultTimeout = 5 * time.Second

// maxScripts and maxTimerJobs bound the work done regardless of the clock, so a
// pathological page cannot enqueue unbounded callbacks.
const (
	maxScripts   = 200
	maxTimerJobs = 2000
)

// Result reports what a Run did (used by tests and by the engine's logging).
type Result struct {
	ScriptsRun    int   // scripts that executed (inline + fetched)
	ScriptsFailed int   // scripts that threw or failed to compile
	TimersRun     int   // queued timer/animation callbacks drained
	Err           error // first fatal setup error (nil on normal completion)
}

// timerJob is a queued callback from setTimeout/setInterval/requestAnimationFrame/etc.
type timerJob struct {
	id    int64 // 0 for a job clearTimeout/clearInterval can never target (queueMicrotask, rAF, …)
	fn    goja.Callable
	self  goja.Value
	delay int64 // requested delay in ms (setTimeout/setInterval only; 0 otherwise)
	// repeat marks a setInterval job: drainTimers re-queues it (same id) after
	// it runs, instead of letting it drop like every other (one-shot) job.
	repeat bool
	// readyAt gates a REPEATING job's own re-queued copy to the real
	// wall-clock time its own delay actually requests — see drainTimers' own
	// doc comment for why this is the one place this package's timer queue
	// is a real clock, deliberately, and only there. Zero (every freshly
	// queued job, repeat or not) means "ready whenever drainTimers next
	// looks at it", unchanged from this package's existing, documented
	// "not a real clock" behaviour for every job's own FIRST firing.
	readyAt time.Time
}

// binder holds the runtime and all live state for one page execution.
type binder struct {
	vm    *goja.Runtime
	root  *dom.Node // the Document node
	opt   Options
	cache map[*dom.Node]*goja.Object

	// protos maps each DOM/BOM interface name (HTMLElement, Text, Event, …) to its
	// `.prototype`, built by installInterfaces. wrap() stamps a node wrapper with
	// the matching one so `el instanceof HTMLElement` holds and subclassing works.
	protos map[string]*goja.Object

	windowNode *dom.Node // sentinel node keying window-level listeners
	docNode    *dom.Node // sentinel node keying document-level listeners
	listeners  map[*dom.Node]map[string][]*eventListenerEntry
	// onHandlers tracks the current "onX" IDL event-handler-attribute value per
	// (node, type) — e.g. `el.onload = fn` — as ONE slot that a later
	// assignment replaces, unlike addEventListener's list. Implemented as a
	// registered listener under the hood (see setOnHandler) so dispatch needs
	// no separate code path; this map exists only so a reassignment can find
	// and remove the PREVIOUS handler before adding the new one.
	onHandlers map[*dom.Node]map[string]goja.Value

	jobs   []timerJob
	nextID int64
	// cancelledTimers records every id clearTimeout/clearInterval has ever
	// been called on, so a job already removed from b.jobs that is MID-FLIGHT
	// (running right now, about to re-queue itself if it's a setInterval)
	// still honours a cancel the callback issues on its own id.
	cancelledTimers map[int64]bool
	// scriptsCapWarned/jobsCapWarned make the maxScripts/maxTimerJobs caps
	// log ONCE each, the first time they actually stop work, rather than
	// either staying silent (confirmed, by go-wasmcontainers, as a real
	// foot-gun for EnableWorkerStreaming's own long-lived renders — a cap
	// hit there is an expected, reachable outcome, not just a pathological
	// page) or logging every single subsequent check for the rest of a
	// render that might run for minutes.
	scriptsCapWarned bool
	jobsCapWarned    bool
	// mutationObservers holds every live `new MutationObserver(cb)` with an
	// active observe() call — see mutationobserver.go.
	mutationObservers []*mutationObserverReg
	deadman           time.Time
	cookie            string
	storage           map[string]*storageArea
	reqCount          int // JS-initiated HTTP requests this render (bounded by maxRequests)

	// metrics is the real geometry/used-value source read back by
	// getBoundingClientRect / offset* / getComputedStyle. Nil = report zeros /
	// inline styles (the legacy Run path with no layout feedback).
	metrics Metrics
	// executed records which <script> elements have already run, so a settle pass
	// runs only scripts injected since the previous pass (in document order),
	// never re-running a script.
	executed map[*dom.Node]bool
	// currentScript is the <script> element synchronously executing right now
	// (nil between scripts, during callbacks/timers, and during module-bundle
	// execution — matching the real document.currentScript spec: non-null only
	// for a synchronously-running classic script). Set/cleared by runScripts
	// around each execute call.
	currentScript *dom.Node

	// pageDone/pageDoneResult back Session.Done — see Options.EnableDoneSignal's
	// own doc comment. Named distinctly from Session's OWN unrelated "done"
	// field (which means "this Session is closed") to avoid confusing the
	// two. Set once, by the one global function install() installs when
	// that option is set; never cleared, since a page signalling done more
	// than once still means "done" (the FIRST signal is what matters).
	pageDone       bool
	pageDoneResult goja.Value

	// scrollPos holds each element's own script-set scrollTop/scrollLeft, so
	// a script that sets one reads the same value back — this engine has no
	// real scroll/clip model (a single static layout pass, no overflow
	// viewport), so the value has no effect on layout or paint; it exists
	// purely so scroll-position-driven script LOGIC (e.g. a "scroll the
	// active item into view" branch that compares against the current
	// scrollTop) does not silently misbehave against a value hardcoded to 0
	// forever. See scrollTop/scrollLeft's own accessor doc comment in dom.go.
	scrollPos map[*dom.Node][2]float64

	// mediaTime/mediaPaused hold a <video>/<audio> element's own script-set
	// currentTime and play()/pause() state — this engine does no real media
	// decoding/playback (a static renderer has no timeline to advance), so
	// neither has any effect on rendering; they exist purely so a script's
	// OWN seek/play/pause logic (e.g. "click a transcript entry, seek the
	// video there, and play it") stays internally consistent instead of
	// reading back a value hardcoded to 0/paused forever. See
	// currentTime/play/pause's own doc comment in dom.go.
	mediaTime   map[*dom.Node]float64
	mediaPaused map[*dom.Node]bool

	// customElements holds every live customElements.define(name, ctor) —
	// see newCustomElementRegistry (window.go) and upgradeExisting
	// (customelements.go).
	customElements map[string]goja.Value
	// pendingUpgrade is set for the duration of upgradeElement's call into a
	// custom element constructor: the real element's own wrapper, which
	// newInterfaceCtor's structural-interface branch returns in place of the
	// fresh object goja's own [[Construct]] would otherwise allocate for
	// call.This, so `super()` ends up mutating the REAL element. Nil the rest
	// of the time (ordinary `new SomeClass extends HTMLElement` outside an
	// upgrade is unaffected).
	pendingUpgrade *goja.Object
	// shadowCache caches each ShadowRoot's own JS wrapper by pointer, the
	// same identity-preserving role b.cache plays for *dom.Node (el.shadowRoot
	// read twice, or compared against attachShadow()'s own return value, must
	// be the same object).
	shadowCache map[*dom.ShadowRoot]*goja.Object

	// wasmRuntime is created lazily (see wasmRT) on the first WebAssembly
	// call a page actually makes, so a page that never touches WebAssembly
	// pays nothing for it — see wasm.go.
	wasmRuntime wazero.Runtime
	// wasmModules caches each WebAssembly.Module JS wrapper's underlying
	// compiled form by object identity — see wrapWasmModule in wasm.go.
	wasmModules map[*goja.Object]wazero.CompiledModule

	// workers holds every live `new Worker(url)` this binder created — see
	// worker.go. Nil for the overwhelming majority of pages that never touch
	// Worker, making drainWorkerInboxes and stopWorkers true no-ops for them.
	workers []*jsWorker

	// ownedSharedBuffers holds every ArrayBuffer-classed object THIS binder's
	// own VM registered in the package-level sharedBufferRegistry (atomics.go)
	// — its own `new SharedArrayBuffer(n)` calls, and every one reconstructed
	// into this VM from an incoming postMessage. forgetSharedBuffers (called
	// from Session.Close/stopWorkers) uses this to remove exactly this
	// binder's own entries, nothing more, when the render/worker ends.
	ownedSharedBuffers []*goja.Object
	// sabProto caches this VM's own SharedArrayBuffer.prototype (atomics.go's
	// sharedArrayBufferPrototype) — built once, lazily, the first time a
	// SharedArrayBuffer wrapper is ever created or reconstructed in this VM.
	sabProto *goja.Object

	// isWorker marks a binder built by newWorkerBinder (worker.go) rather
	// than the main document's own Begin — the one thing opfs.go needs to
	// decide whether createSyncAccessHandle exists on a FileSystemFileHandle
	// this VM creates, matching the File System Standard's own
	// [Exposed=DedicatedWorker] restriction (it is meaningless outside a
	// Worker: there is no other context in this engine with its own
	// independent event loop to block synchronously).
	isWorker bool
}

// Run builds the DOM binding on root (a dom.Document node), sets the JS-enabled
// signal, executes the page's scripts in document order, drains queued timers,
// and dispatches DOMContentLoaded/load. It never returns a script error as
// fatal; Result.Err is only set for setup failures. It is the one-shot,
// no-layout-feedback entry point (used by the js package's own tests); the
// engine drives a Session directly so scripts can read real geometry.
func Run(root *dom.Node, opt Options) Result {
	s := Begin(root, opt)
	defer s.Close()
	s.RunInitial()
	return s.Result()
}

// runScripts executes every not-yet-run <script> in document order, honouring
// the budget. A script runs at most once across the whole session, so calling
// this again after the DOM gains new <script> elements runs only those new ones
// (a ResourceLoader-style injected-script chain), in document order.
func (b *binder) runScripts(res *Result) {
	if b.executed == nil {
		b.executed = map[*dom.Node]bool{}
	}
	for _, s := range collectScripts(b.root) {
		if b.executed[s.node] {
			continue
		}
		if res.ScriptsRun+res.ScriptsFailed >= maxScripts {
			if !b.scriptsCapWarned {
				b.scriptsCapWarned = true
				b.logf("script cap reached (%d): remaining <script> elements this render will not run", maxScripts)
			}
			return
		}
		if b.expired() {
			return
		}
		b.executed[s.node] = true
		src, ok := b.scriptSource(s)
		if !ok {
			// A real browser fires "error" on the <script> element itself when
			// fetching its src fails (404, network error, …), distinct from a
			// runtime exception during execution (which still fires "load" on
			// the element — see below — reporting the exception separately via
			// window's own error event instead). Dynamically-injected script
			// loaders (webpack/Turbopack's own chunk-loading helper, confirmed
			// load-bearing live on react.dev) set onerror/onload on the element
			// specifically to detect this outcome; without it, a failed fetch
			// left their loading promise pending forever.
			b.dispatch(s.node, "error", b.newEvent("error"))
			continue
		}
		b.currentScript = s.node
		ok = b.execute(src, s.name)
		b.currentScript = nil
		b.dispatch(s.node, "load", b.newEvent("load"))
		if ok {
			res.ScriptsRun++
		} else {
			res.ScriptsFailed++
		}
	}
}

// execute compiles and runs one script, returning whether it completed without
// error. Compile and runtime errors (including interrupts) are contained.
func (b *binder) execute(src, name string) (ok bool) {
	prog, err := goja.Compile(name, src, false)
	if err != nil {
		b.logf("compile %s: %v", name, err)
		return false
	}
	defer func() {
		if r := recover(); r != nil {
			b.logf("panic in %s: %v", name, r)
			ok = false
		}
	}()
	b.vm.ClearInterrupt()
	// ClearInterrupt wipes any pending interrupt — including the watcher's
	// one-shot "context cancelled" fired for a context that was ALREADY cancelled
	// before this script started (the watcher's select fires once, then exits, so
	// it will not re-fire). Re-arm it here so an already-cancelled context still
	// interrupts this script immediately instead of letting it run to the budget
	// timeout.
	if b.opt.Ctx.Err() != nil {
		b.vm.Interrupt("context cancelled")
	}
	if _, err := b.vm.RunProgram(prog); err != nil {
		b.logf("run %s: %v", name, err)
		return false
	}
	return true
}

// dispatchLifecycle fires DOMContentLoaded then load, draining queued timers
// between and after so deferred hydration runs.
func (b *binder) dispatchLifecycle(res *Result) {
	b.dispatch(b.docNode, "DOMContentLoaded", b.newEvent("DOMContentLoaded"))
	b.dispatch(b.windowNode, "DOMContentLoaded", b.newEvent("DOMContentLoaded"))
	b.drainTimers(res)
	b.dispatch(b.windowNode, "load", b.newEvent("load"))
	b.dispatch(b.windowNode, "pageshow", b.newEvent("pageshow"))
	b.drainTimers(res)
}

// drainTimers runs queued, DUE callbacks in ascending order of their own
// requested delay (ties broken FIFO by original enqueue order), until the
// queue has nothing due, the job cap is hit, or the budget expires.
//
// For a one-shot job (setTimeout, rAF, …) this is NOT a real clock — no
// wall-clock waiting happens before its first, only, firing — but ordering
// by delay rather than pure enqueue order matters: real page code commonly
// races a near-immediate setTimeout(fn, 0) retry/continuation against a much
// later setTimeout(giveUp, 30000+) safety-net timeout, and draining strict
// FIFO can let the safety-net job run first purely because it happened to be
// enqueued earlier, when every real browser would run the smaller-delay job
// first. Callbacks may enqueue more work, which is honoured within those
// bounds.
//
// A setInterval job (repeat=true) is the ONE deliberate exception: once it
// fires, its re-queued copy carries a real readyAt (time.Now()+delay), and
// the "due" check below skips it until that wall-clock time actually passes
// — otherwise a repeating interval would refire thousands of times in a
// single burst the instant anything calls drainTimers, rather than at the
// real cadence its own delay asks for (this is EXACTLY the bug
// go-wasmcontainers hit: their page's periodic "are we done yet" poll,
// before this fix, could only ever check once, the one time its own interval
// job happened to be ready — see FIDELITY.md's own entry for the full story).
// When nothing is due yet, this returns immediately rather than busy-waiting
// — a later call (the next real tick, for a page using EnableWorkerStreaming)
// catches it once its time arrives.
func (b *binder) drainTimers(res *Result) {
	// A worker's own top-level script may have posted to main before any
	// main->worker message gave postToWorker a chance to deliver it (see
	// drainWorkerInboxes' own doc comment) — give it a chance here too, the
	// same "run pending async work" moment drainTimers already is. A no-op
	// when b.workers is nil (no Worker ever created).
	b.drainWorkerInboxes()
	for len(b.jobs) > 0 {
		if res.TimersRun >= maxTimerJobs {
			if !b.jobsCapWarned {
				b.jobsCapWarned = true
				b.logf("timer-job cap reached (%d) with %d still queued: further timers and Worker messages this render will not run", maxTimerJobs, len(b.jobs))
			}
			return
		}
		if b.expired() {
			return
		}
		now := time.Now()
		best := -1
		for i := range b.jobs {
			if !b.jobs[i].readyAt.IsZero() && b.jobs[i].readyAt.After(now) {
				continue // a repeating interval's own next firing isn't due yet
			}
			if best == -1 || b.jobs[i].delay < b.jobs[best].delay {
				best = i
			}
		}
		if best == -1 {
			return // every remaining job is a not-yet-due interval repeat
		}
		job := b.jobs[best]
		b.jobs = append(b.jobs[:best], b.jobs[best+1:]...)
		res.TimersRun++
		b.callSafely(job.fn, job.self)
		if job.repeat && !b.cancelledTimers[job.id] {
			b.jobs = append(b.jobs, timerJob{
				id: job.id, fn: job.fn, self: job.self, delay: job.delay, repeat: true,
				readyAt: time.Now().Add(time.Duration(job.delay) * time.Millisecond),
			})
		}
		b.drainWorkerInboxes()
	}
}

// callSafely invokes a stored callable, containing any error or panic.
func (b *binder) callSafely(fn goja.Callable, self goja.Value, args ...goja.Value) {
	if fn == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			b.logf("callback panic: %v", r)
		}
	}()
	b.vm.ClearInterrupt()
	if _, err := fn(self, args...); err != nil {
		b.logf("callback: %v", err)
	}
}

// expired reports whether the wall-clock budget is spent.
func (b *binder) expired() bool { return !time.Now().Before(b.deadman) }

// logf routes an internal diagnostic to the console sink, if any.
func (b *binder) logf(format string, args ...any) {
	if b.opt.Log != nil {
		b.opt.Log("engine: " + fmt.Sprintf(format, args...))
	}
}

// setClientJS marks the document root as JS-enabled (client-js) and clears the
// no-JS fallback class — the single change that restores JS-gated CSS (e.g.
// MediaWiki's nav collapse) even if the page's own scripts do not run.
func (b *binder) setClientJS() {
	html := dom.Find(b.root, "html")
	if html == nil {
		return
	}
	classRemove(html, "client-nojs")
	classAdd(html, "client-js")
}

// script is one collected <script> element with a display name.
type script struct {
	node *dom.Node
	name string
}

// collectScripts returns runnable <script> elements in document order, skipping
// module/non-JavaScript types (handled gracefully rather than mis-run).
func collectScripts(root *dom.Node) []script {
	var out []script
	i := 0
	var walk func(n *dom.Node)
	walk = func(n *dom.Node) {
		if n.Type == dom.Element && n.Tag == "script" && scriptIsJS(n) {
			i++
			out = append(out, script{node: n, name: scriptName(n, i)})
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(root)
	return out
}

// hasPendingScripts reports whether the tree now holds a <script> element that
// runScripts has not executed yet — see RunPending's own doc comment for why
// this is checked without actually running anything here.
func (b *binder) hasPendingScripts() bool {
	for _, s := range collectScripts(b.root) {
		if !b.executed[s.node] {
			return true
		}
	}
	return false
}

// scriptIsJS reports whether a <script>'s type is (classic) JavaScript. Module,
// JSON and other typed scripts are skipped.
func scriptIsJS(n *dom.Node) bool {
	t, ok := n.Attribute("type")
	if !ok {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "", "text/javascript", "application/javascript", "application/ecmascript",
		"text/ecmascript", "text/jscript":
		return true
	}
	return false
}

func scriptName(n *dom.Node, i int) string {
	if src, ok := n.Attribute("src"); ok && strings.TrimSpace(src) != "" {
		return strings.TrimSpace(src)
	}
	return "inline#" + strconv.Itoa(i)
}

// scriptSource returns the JS source for a script: its fetched src, or its inline
// text. Missing/unfetchable external scripts report false.
func (b *binder) scriptSource(s script) (string, bool) {
	if src, ok := s.node.Attribute("src"); ok && strings.TrimSpace(src) != "" {
		return b.fetchScript(strings.TrimSpace(src))
	}
	var sb strings.Builder
	for _, c := range s.node.Children {
		if c.Type == dom.Text {
			sb.WriteString(c.Text)
		}
	}
	if strings.TrimSpace(sb.String()) == "" {
		return "", false
	}
	return sb.String(), true
}

// fetchScript retrieves an external script's text, bounded and best-effort.
func (b *binder) fetchScript(src string) (string, bool) {
	if b.opt.Client == nil {
		return "", false
	}
	abs, ok := resolveURL(b.opt.PageURL, src)
	if !ok || !strings.HasPrefix(abs, "http") {
		return "", false
	}
	ctx, cancel := context.WithTimeout(b.opt.Ctx, scriptFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, abs, nil)
	if err != nil {
		return "", false
	}
	if b.opt.UserAgent != "" {
		req.Header.Set("User-Agent", b.opt.UserAgent)
	}
	resp, err := b.opt.Client.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxScriptBytes))
	if err != nil {
		return "", false
	}
	return string(body), true
}

const (
	scriptFetchTimeout = 8 * time.Second
	maxScriptBytes     = 4 << 20
)

// resolveURL resolves ref against base.
func resolveURL(base, ref string) (string, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", false
	}
	b, err := url.Parse(base)
	if err != nil {
		return "", false
	}
	r, err := url.Parse(ref)
	if err != nil {
		return "", false
	}
	return b.ResolveReference(r).String(), true
}
