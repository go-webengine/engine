// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package js

import (
	"errors"
	"sync"
	"time"

	"github.com/dop251/goja"

	"github.com/go-webengine/engine/dom"
)

// workerInboxCap bounds how many not-yet-delivered main->worker messages can
// queue up — the same "always bounded" convention as maxTimerJobs/
// maxRequests. A producer that outruns this gets its message dropped rather
// than blocking the page's own script forever (see postToWorker).
const workerInboxCap = 64

// jsWorker is one `new Worker(url)`: a second, independent binder+goja.Runtime
// on its own goroutine. goja's own documented contract is one Runtime per
// goroutine with no passing object values between runtimes, so a Worker is
// architecturally a second VM, not a second thread sharing the first —
// everything crossing the main<->worker boundary goes through
// cloneValue/reconstructValue (clone.go), never a raw goja.Value.
//
// The postMessage round-trip is deliberately synchronous/blocking rather
// than truly async: this package has no background event loop of its own
// (the main binder's timers/microtasks only drain when the engine's settle
// loop or RunPending calls in) to deliver a worker's reply into, so
// Worker.postMessage blocks the caller until the worker has fully processed
// that one message (bounded by the render's own deadman budget) and then
// delivers whatever the worker queued back in that same call — giving a
// deterministic, testable round trip without inventing a second clock.
type jsWorker struct {
	wb          *binder   // the worker's own runtime, touched ONLY by w.run's goroutine
	wrapperNode *dom.Node // sentinel key for the MAIN-side Worker object's own listeners

	inbox  chan workerTask
	stopCh chan struct{}
	ready  chan struct{} // closed once the top-level script has finished
	done   chan struct{} // closed when w.run actually RETURNS, see stopWorkers
	stop   sync.Once

	mainMu    sync.Mutex
	mainInbox []clonedValue // worker->main messages, delivered by drainWorkerInboxes

	res Result // the worker's own script/timer tally (diagnostics only)
}

// workerTask is one main->worker postMessage delivery.
type workerTask struct {
	data clonedValue
	done chan struct{} // closed once the worker has finished handling this task
}

// newWorkerBinder builds the binder for a Worker's own dedicated runtime. It
// deliberately does not call install(): a Worker has no DOM, no document and
// no window — only installWorkerGlobals' own cut-down global scope — so this
// reuses just the fields that machinery needs (the vm, the sentinel-node
// event dispatch maps windowNode/docNode already prove generic, the timer
// queue). deadline is shared with the creating binder: a worker cannot
// outlive the render's own overall budget.
func newWorkerBinder(opt Options, deadline time.Time) *binder {
	self := &dom.Node{Type: dom.Element, Tag: "#worker-self"}
	return &binder{
		vm:         goja.New(),
		opt:        opt,
		cache:      map[*dom.Node]*goja.Object{},
		windowNode: self,
		docNode:    self,
		listeners:  map[*dom.Node]map[string][]*eventListenerEntry{},
		onHandlers: map[*dom.Node]map[string]goja.Value{},
		executed:   map[*dom.Node]bool{},
		deadman:    deadline,
		timeOrigin: time.Now(),
		isWorker:   true,
	}
}

// installWorkerGlobals wires a Worker's own global scope (DedicatedWorkerGlobalScope,
// minimal subset): console, postMessage (to main), onmessage/addEventListener
// for "message", close(), and the same timer surface window gets.
func (w *jsWorker) installWorkerGlobals() {
	wb := w.wb
	g := wb.vm.GlobalObject()
	for _, name := range []string{"self", "globalThis"} {
		g.Set(name, g)
	}
	g.Set("console", wb.newConsole())
	// DOMException is a general exception type the spec exposes in every
	// script context, not a DOM-specific interface installInterfaces alone
	// would cover — see installDOMExceptionOnly's own doc comment. Needed
	// here specifically because createSyncAccessHandle's own errors
	// (opfs.go's domException helper) are thrown as real DOMExceptions.
	wb.installDOMExceptionOnly(g)
	// WorkerNavigator (HTML Standard §10.2.3.1): the same newNavigator() the
	// main document's window gets — it only ever reads wb.vm/wb.opt, nothing
	// document-specific — so a worker gets userAgent and, per opfs.go, the
	// navigator.storage.getDirectory() entry point its own OPFS usage needs
	// (createSyncAccessHandle itself is only ever reachable from a
	// FileSystemFileHandle built by THIS binder, gated on wb.isWorker).
	g.Set("navigator", wb.newNavigator())
	// Crypto (WebCryptoAPI §10, Exposed=(Window, Worker)): another piece of
	// general-purpose plumbing that rode along with the DOM-specific
	// install() a Worker correctly skips, even though crypto itself has
	// nothing to do with the DOM — the same shape of gap
	// installDOMExceptionOnly already fixed for DOMException. Confirmed
	// needed directly by go-wasmcontainers: Go's own wasm_exec.js calls
	// crypto.getRandomValues at startup, inside the worker, before anything
	// else in their own script runs.
	wb.installCrypto(g)
	// The rest of this block is the SAME gap, audited exhaustively against
	// every remaining install() call rather than waiting for each to surface
	// as its own go-wasmcontainers blocker in turn: TextEncoder/TextDecoder
	// (installEncoding), fetch/XMLHttpRequest/Headers/Request/MessageChannel
	// (installNet — and now correctly resolves a worker's own relative
	// fetch() URLs against the worker's OWN script URL, not the creating
	// document's, thanks to newWorker's own PageURL fix), the plain URL
	// constructor (installConstructors' own OTHER registrations —
	// MutationObserver, IntersectionObserver/ResizeObserver/
	// PerformanceObserver, Image — are real DOM/layout concepts a Worker
	// correctly has none of, so only URL is pulled out here, not the whole
	// function), URLSearchParams, Intl, and performance (a plain stub
	// object even for the main document — see newPerformance's own body —
	// so there is nothing document-specific to lose by sharing it). Each
	// confirmed, by reading its own body, to touch nothing but b.vm/Go
	// stdlib — no document, no DOM node, no layout/paint feedback — before
	// being added here, the same bar installCrypto's own fix was held to.
	wb.installEncoding(g)
	wb.installNet(g)
	g.Set("URL", func(call goja.ConstructorCall) *goja.Object {
		return wb.buildURL(call.Argument(0).String(), call.Argument(1))
	})
	wb.installURLSearchParams(g)
	wb.installIntl(g)
	g.Set("performance", wb.newPerformance())
	// WebAssembly (Exposed=(Window, Worker) per the WebAssembly JS API —
	// confirmed missing live by go-wasmcontainers' own worker-side probe:
	// their wasm_exec.js calls WebAssembly.instantiateStreaming at startup).
	// newWebAssembly's own body touches only b.vm/b.opt/the lazily-created
	// wazero runtime (wasmRT) — confirmed by reading it, no DOM dependency.
	g.Set("WebAssembly", wb.newWebAssembly())
	// location (WorkerLocation, HTML Standard §10.2.3.2): confirmed missing
	// live ("location is not defined") by the same probe, which reads
	// location.search/location.hostname. Reuses the SAME newLocation() the
	// main document's window gets — it only ever reads wb.opt.PageURL
	// (correctly the WORKER's own script URL, not the creating document's,
	// thanks to newWorker's own base-URL fix) and does real net/url
	// parsing, no DOM dependency. A stated, deliberate simplification:
	// real WorkerLocation has no assign()/replace()/reload() (a worker
	// cannot navigate itself) — this object still has them as harmless
	// no-ops/mutators, since no confirmed need justifies a stricter,
	// separate read-only type yet.
	g.Set("location", wb.newLocation())
	wb.installTimers(g)
	// crossOriginIsolated + SharedArrayBuffer + Atomics (atomics.go): a
	// worker's own global scope needs these every bit as much as the main
	// document's does — the hot path (go-wasmcontainers, engine#343) is
	// specifically a worker calling Atomics.wait on a buffer main shared
	// with it. wb.opt is the SAME Options the creating binder's own
	// CrossOriginIsolated was resolved into (newWorkerBinder passes it
	// through unchanged) — "workers inherit it" (the spec's own rule,
	// confirmed directly by go-wasmcontainers), not a separate decision.
	g.Set("crossOriginIsolated", wb.opt.CrossOriginIsolated)
	if wb.opt.CrossOriginIsolated {
		wb.installSharedArrayBuffer(g)
		wb.installAtomics(g)
	}
	// navigator.gpu + GPUBufferUsage/GPUMapMode/GPUShaderStage (WebGPU,
	// Exposed=(Window, Worker)): same opt-in gate as the main document's
	// own install().
	if wb.opt.EnableWebGPU {
		g.Set("GPUShaderStage", wb.newGPUShaderStage())
		g.Set("GPUBufferUsage", wb.newGPUBufferUsage())
		g.Set("GPUMapMode", wb.newGPUMapMode())
	}
	// Nested Worker (HTML Standard §10.2.6 permits it; confirmed needed
	// directly by go-wasmcontainers: their own engine spawns workers from
	// within a worker). newWorker has no hidden assumption that it is only
	// ever called on the TOP-level binder — it already operates generically
	// on whichever *binder it is a method of (wb here), appending to wb's
	// OWN workers slice, fetching the child's script against wb's OWN
	// PageURL (so a grandchild's relative URL correctly resolves against
	// ITS creating worker's location, recursively) — the one thing that
	// needed fixing for this to be safe was stopWorkers' own cleanup not
	// cascading into a worker's own children; see its own doc comment.
	g.Set("Worker", func(call goja.ConstructorCall) *goja.Object { return wb.newWorker(call) })

	g.Set("postMessage", func(call goja.FunctionCall) goja.Value {
		cv, err := cloneValue(call.Argument(0))
		if err != nil {
			wb.logf("postMessage: %v", err)
			return goja.Undefined()
		}
		w.mainMu.Lock()
		w.mainInbox = append(w.mainInbox, cv)
		w.mainMu.Unlock()
		return goja.Undefined()
	})
	g.Set("addEventListener", func(call goja.FunctionCall) goja.Value {
		opts, _ := call.Argument(2).(*goja.Object)
		wb.addListener(wb.windowNode, call.Argument(0).String(), call.Argument(1), optBool(opts, "once"))
		return goja.Undefined()
	})
	g.Set("removeEventListener", func(call goja.FunctionCall) goja.Value {
		wb.removeListener(wb.windowNode, call.Argument(0).String(), call.Argument(1))
		return goja.Undefined()
	})
	g.Set("dispatchEvent", func(call goja.FunctionCall) goja.Value {
		wb.dispatch(wb.windowNode, eventType(call.Argument(0)), call.Argument(0))
		return wb.vm.ToValue(true)
	})
	wb.accessor(g, "onmessage",
		func() goja.Value { return orUndefined(wb.onHandler(wb.windowNode, "message")) },
		func(v goja.Value) { wb.setOnHandler(wb.windowNode, "message", v) })
	wb.accessor(g, "onerror",
		func() goja.Value { return orUndefined(wb.onHandler(wb.windowNode, "error")) },
		func(v goja.Value) { wb.setOnHandler(wb.windowNode, "error", v) })
	g.Set("close", func(goja.FunctionCall) goja.Value {
		w.stop.Do(func() { close(w.stopCh) })
		return goja.Undefined()
	})
	// importScripts (HTML Standard §10.3.4, WorkerGlobalScope): the one
	// thing a classic dedicated worker needs that this package's own
	// top-level-script-only model didn't yet cover — confirmed needed
	// directly by go-wasmcontainers (`importScripts("wasm_exec.js")` is
	// literally their worker's first statement).
	g.Set("importScripts", wb.importScripts)
}

// importScripts implements WorkerGlobalScope.importScripts(...urls) (HTML
// Standard §10.3.4, "import scripts into worker global scope"). Every url
// is resolved and syntax-checked FIRST — a bad url throws SyntaxError
// before anything is fetched, matching the spec's own algorithm order —
// then each is fetched and run IN TURN, sharing this worker's own global
// scope exactly like multiple top-level <script> tags would (runRethrow
// runs against the SAME b.vm). A fetch failure or an uncaught exception in
// an imported script propagates to the CALLER (the worker script that
// invoked importScripts) — spec's own "rethrow errors: true" — unlike
// every OTHER script this package ever runs (a worker's own top-level
// script, or a queued timer callback), whose errors are caught and merely
// logged (see execute's own doc comment: "a script error never
// propagates"). A script that already ran is not undone if a LATER url in
// the same call fails.
func (b *binder) importScripts(call goja.FunctionCall) goja.Value {
	urls := make([]string, len(call.Arguments))
	for i, a := range call.Arguments {
		urls[i] = a.String()
	}
	for _, u := range urls {
		if _, ok := resolveURL(b.opt.PageURL, u); !ok {
			panic(b.domException("SyntaxError", "importScripts: cannot parse URL "+u))
		}
	}
	for _, u := range urls {
		src, ok := b.fetchScript(u)
		if !ok {
			panic(b.vm.NewTypeError("importScripts: failed to fetch " + u))
		}
		if err := b.runRethrow(src, u); err != nil {
			var exc *goja.Exception
			if errors.As(err, &exc) {
				panic(exc.Value())
			}
			panic(b.vm.NewGoError(err))
		}
	}
	return goja.Undefined()
}

// runRethrow compiles and runs one script exactly like execute (js.go),
// except it RETURNS a compile/runtime error instead of logging it and
// swallowing it — the one case this package needs that for: importScripts'
// own spec-mandated "rethrow errors" contract.
func (b *binder) runRethrow(src, name string) error {
	prog, err := goja.Compile(name, src, false)
	if err != nil {
		return err
	}
	b.vm.ClearInterrupt()
	if b.opt.Ctx.Err() != nil {
		b.vm.Interrupt("context cancelled")
	}
	_, err = b.vm.RunProgram(prog)
	return err
}

// workerTickInterval is the real wall-clock cadence a worker's own event
// loop (run, below) drains its queued timers at once no message is pending
// — independently chosen from the engine package's own liveLoopPollInterval
// (settle_scripts.go), since js has no dependency on engine, but the same
// reasoning: fast enough that a self-rescheduling setTimeout chain (a
// container's own "read a chunk, post it, schedule the next read" loop)
// behaves like real streaming over time rather than running out its WHOLE
// maxTimerJobs budget instantly in one synchronous burst; slow enough that a
// worker with nothing queued costs negligible CPU for the rest of its
// lifetime.
const workerTickInterval = 15 * time.Millisecond

// run executes the worker's top-level script once (bounded, contained like
// every other script this package runs), signals ready, then services
// main->worker messages AND the worker's own queued timers until stopped.
// It is the only goroutine that ever touches w.wb.
//
// The periodic tick exists because a worker is meant to run autonomously —
// go-wasmcontainers' own confirmed shape: "the worker runs for the
// container's whole lifetime... posts unsolicited messages at any time" —
// not merely react to messages from main. Without it, a worker's own
// self-rescheduling setTimeout chain would have EVERY iteration drained
// back-to-back with no real pacing the one time drainTimers ran (right after
// the top-level script), burning its entire maxTimerJobs budget in a single
// synchronous burst and then going silent forever; ticking on a real
// interval lets such a chain behave like genuine streaming instead.
func (w *jsWorker) run(src, name string) {
	// Closed on EVERY return path (including a panic that escapes the
	// recover below, which there is no such path for today, but a future
	// change must not get to silently reopen this race by adding one) —
	// see stopWorkers' own doc comment for why this specific guarantee is
	// load-bearing: it is the only thing that lets the main goroutine know
	// this goroutine has genuinely stopped touching w.wb before anything
	// else frees what w.wb owns.
	defer close(w.done)
	// Deliberately does NOT drain w.wb's timers here, before close(w.ready):
	// the constructor (newWorker) blocks on w.ready and only then returns
	// the Worker object, so the calling script's very next line is almost
	// always `w.onmessage = fn`. A synchronous, unpaced drain right here
	// would run a self-rescheduling setTimeout chain (a worker's own
	// autonomous streaming loop) to completion in one burst BEFORE that
	// listener is ever registered — every message dispatched to nobody,
	// silently lost, with the worker then going silent forever having
	// burned its whole run on an unobserved burst. Queued timers are
	// drained by the ticker below instead, exactly like any other real
	// timer: paced, and only once the constructor (and whatever the script
	// does right after) has had its chance to register a listener first.
	func() {
		defer func() {
			if r := recover(); r != nil {
				w.wb.logf("worker panic: %v", r)
			}
		}()
		w.wb.execute(src, name)
	}()
	close(w.ready)
	// No case for w.inbox being closed: nothing ever closes it (only stopCh
	// stops this loop — see terminate()/self-close()/Session.Close's own
	// stopWorkers, and postToWorker's own send side, which only ever sends,
	// never closes). A second receive-form here would be unreachable dead
	// code, like Session.Done's own now-removed pageDoneResult nil-check.
	ticker := time.NewTicker(workerTickInterval)
	defer ticker.Stop()
	for {
		select {
		case task := <-w.inbox:
			w.deliver(task)
		case <-ticker.C:
			if w.wb.expired() {
				return
			}
			w.wb.drainTimers(&w.res)
		case <-w.stopCh:
			return
		}
	}
}

// deliver reconstructs one main->worker message into the worker's own VM,
// dispatches it as a "message" event on the worker's self scope, then
// signals the task done — the point at which the creating binder's blocked
// postToWorker call unblocks.
//
// Deliberately does NOT drain the worker's own queued timers here, unlike
// the main binder's own dispatch paths: a handler may schedule a blocking
// Atomics.wait via setTimeout(fn, 0) specifically to escape this very call
// (see atomicsWait's own doc comment — calling it synchronously from inside
// a message handler would hold the SENDER's blocked postToWorker hostage,
// deadlocking against the sender's own later Atomics.notify). Draining here
// too would inline that deferred call right back into this same stack,
// defeating the escape. The ticker in run's own select loop drains every
// queued timer — this handler's included — exactly once it actually ticks,
// by which point this call (and the sender's blocked wait on task.done) has
// already returned.
func (w *jsWorker) deliver(task workerTask) {
	defer func() {
		if r := recover(); r != nil {
			w.wb.logf("worker panic: %v", r)
		}
		close(task.done)
	}()
	v := reconstructValue(w.wb, task.data)
	ev := w.wb.newEvent("message")
	ev.Set("data", v)
	w.wb.dispatch(w.wb.windowNode, "message", ev)
}

// newWorker implements `new Worker(scriptURL)` on the main VM. The script is
// fetched synchronously (reusing fetchScript exactly as external <script src>
// does) and its top-level run completes before the constructor returns
// (bounded by the render's own remaining budget), so by the time script gets
// the Worker object back, the worker is already up — matching every other
// fetch in this binder being synchronous-from-JS's-perspective. A fetch
// failure dispatches "error" on the wrapper, mirroring runScripts' own
// failed-external-script precedent, rather than throwing.
func (b *binder) newWorker(call goja.ConstructorCall) *goja.Object {
	o := b.vm.NewObject()
	wrapperNode := &dom.Node{Type: dom.Element, Tag: "#worker"}

	o.Set("addEventListener", func(c goja.FunctionCall) goja.Value {
		opts, _ := c.Argument(2).(*goja.Object)
		b.addListener(wrapperNode, c.Argument(0).String(), c.Argument(1), optBool(opts, "once"))
		return goja.Undefined()
	})
	o.Set("removeEventListener", func(c goja.FunctionCall) goja.Value {
		b.removeListener(wrapperNode, c.Argument(0).String(), c.Argument(1))
		return goja.Undefined()
	})
	o.Set("dispatchEvent", func(c goja.FunctionCall) goja.Value {
		b.dispatch(wrapperNode, eventType(c.Argument(0)), c.Argument(0))
		return b.vm.ToValue(true)
	})
	b.accessor(o, "onmessage",
		func() goja.Value { return orUndefined(b.onHandler(wrapperNode, "message")) },
		func(v goja.Value) { b.setOnHandler(wrapperNode, "message", v) })
	b.accessor(o, "onerror",
		func() goja.Value { return orUndefined(b.onHandler(wrapperNode, "error")) },
		func(v goja.Value) { b.setOnHandler(wrapperNode, "error", v) })

	scriptArg := call.Argument(0).String()
	src, ok := b.fetchScript(scriptArg)
	if !ok {
		o.Set("postMessage", func(goja.FunctionCall) goja.Value { return goja.Undefined() })
		o.Set("terminate", func(goja.FunctionCall) goja.Value { return goja.Undefined() })
		// Queued, not dispatched here directly: the fetch already happened
		// synchronously (fetchScript, like every other fetch in this
		// binder), but the calling script's very next line is almost always
		// `w.onerror = fn` — dispatching immediately, before that line runs,
		// would fire to no listener. Queuing via the existing timer/job
		// drain (the same mechanism wirePort's postMessage delivery uses)
		// lets the rest of THIS script run first, matching the idiom's own
		// expectation (and how a real, asynchronous fetch failure would
		// naturally land after the constructor call returns).
		b.jobs = append(b.jobs, timerJob{fn: b.wrapJob(func() {
			b.dispatch(wrapperNode, "error", b.newEvent("error"))
		}), self: goja.Undefined()})
		return o
	}

	// A worker's own settings object resolves relative URLs (importScripts,
	// or any future fetch a worker makes) against the WORKER's own script
	// URL, never the creating document's — a real, confirmed bug fixed here
	// (engine#342 follow-up): newWorkerBinder previously inherited
	// b.opt.PageURL UNCHANGED from the creating binder, so
	// `importScripts('wasm_exec.js')` from a worker NOT co-located with the
	// main page resolved against the wrong origin/path entirely. Found by
	// direct reproduction (a worker served from a different path than its
	// own relative importScripts target), not assumed.
	workerOpt := b.opt
	if abs, ok := resolveURL(b.opt.PageURL, scriptArg); ok {
		workerOpt.PageURL = abs
	}
	w := &jsWorker{
		wb:          newWorkerBinder(workerOpt, b.deadman),
		wrapperNode: wrapperNode,
		inbox:       make(chan workerTask, workerInboxCap),
		stopCh:      make(chan struct{}),
		ready:       make(chan struct{}),
		done:        make(chan struct{}),
	}
	w.installWorkerGlobals()
	b.workers = append(b.workers, w)

	go w.run(src, call.Argument(0).String())
	select {
	case <-w.ready:
	case <-time.After(time.Until(b.deadman)):
		w.stop.Do(func() { close(w.stopCh) })
	}
	// Deliberately NOT drained here directly: the worker's own top-level
	// script can (and commonly does — a "ready"/handshake message is an
	// everyday idiom) call postMessage synchronously before this constructor
	// even returns, and the calling script's very next line is almost always
	// `w.onmessage = fn`. Draining right here, like the error-dispatch path
	// above, would deliver to no listener and lose it. Queuing via the same
	// job mechanism lets the REST of this script run first.
	b.jobs = append(b.jobs, timerJob{fn: b.wrapJob(func() {
		b.drainWorkerInboxes()
	}), self: goja.Undefined()})

	o.Set("postMessage", func(c goja.FunctionCall) goja.Value {
		b.postToWorker(w, c.Argument(0))
		return goja.Undefined()
	})
	o.Set("terminate", func(goja.FunctionCall) goja.Value {
		w.stop.Do(func() { close(w.stopCh) })
		return goja.Undefined()
	})
	return o
}

// postToWorker clones data on the MAIN vm (the caller's own goroutine),
// hands it to the worker, and blocks until the worker has finished handling
// it (or the render's remaining budget runs out, or the worker was already
// stopped) — see jsWorker's own doc comment for why this is synchronous.
// It then delivers anything the worker queued back in that same call, so a
// request/reply script (the common case, and this round's own verification
// shape) sees the reply before postMessage returns.
func (b *binder) postToWorker(w *jsWorker, data goja.Value) {
	cv, err := cloneValue(data)
	if err != nil {
		b.logf("Worker.postMessage: %v", err)
		return
	}
	select {
	case <-w.stopCh:
		return
	default:
	}
	done := make(chan struct{})
	select {
	case w.inbox <- workerTask{data: cv, done: done}:
	default:
		b.logf("Worker.postMessage: inbox full, message dropped")
		return
	}
	select {
	case <-done:
	case <-w.stopCh:
	case <-time.After(time.Until(b.deadman)):
	}
	b.drainWorkerInboxes()
}

// drainWorkerInboxes delivers every worker->main message queued since the
// last drain, dispatching "message" on each worker's own wrapper object on
// THIS (the main) goroutine — the only goroutine allowed to touch b.vm. A
// binder that never created a Worker has a nil b.workers, making this a
// true no-op, called from drainTimers for eventual delivery even outside an
// explicit postToWorker round trip (e.g. a worker's own top-level script
// posting before any main->worker message occurs).
func (b *binder) drainWorkerInboxes() {
	for _, w := range b.workers {
		w.mainMu.Lock()
		items := w.mainInbox
		w.mainInbox = nil
		w.mainMu.Unlock()
		for _, cv := range items {
			v := reconstructValue(b, cv)
			ev := b.newEvent("message")
			ev.Set("data", v)
			b.dispatch(w.wrapperNode, "message", ev)
		}
	}
}

// stopWorkers terminates every live worker this binder created — called by
// Session.Close so a render never leaks a worker goroutine past its own
// session's lifetime. Recurses into each worker's OWN stopWorkers too — a
// real fix needed once nested Worker (a worker creating its own child
// workers) became possible: without this, Session.Close's single-level
// walk would stop only the direct children, leaking every grandchild's own
// goroutine, GPU device and SharedArrayBuffer registry entries past the
// session's lifetime.
//
// Waits for w.run to actually RETURN (bounded by the SAME deadman every
// worker is already bound by) before touching anything w.wb owns — a real,
// previously-shipped bug, not a hypothetical: closing w.stopCh is only a
// SIGNAL, observed at the top of w.run's own select loop between units of
// work; a worker whose top-level script is still inside a long synchronous
// call (a real-hardware GPU dispatch's own blocking wait, in particular) is
// not selecting on anything and does not react to it at all. The previous
// code called w.wb.closeGPUDevices() (releasing live Objective-C Metal
// objects) immediately after signalling, racing a worker goroutine that
// might still be mid-flight inside a Metal call on those EXACT objects —
// jsWorker's own struct doc comment already says "wb touched ONLY by
// w.run's goroutine", a invariant this exact cleanup code was violating.
// Confirmed as the real cause of a native SIGSEGV go-wasmcontainers
// reported and made deterministic (6/6) once their workload's own GPU
// dispatches ran long enough to reliably land inside this window; an
// earlier go-macos/metal fix (AutoreleasePool wrapping) addressed a real
// but DIFFERENT gap and did not touch this one.
func (b *binder) stopWorkers() {
	for _, w := range b.workers {
		w.stop.Do(func() { close(w.stopCh) })
		select {
		case <-w.done:
		case <-time.After(time.Until(b.deadman)):
			// Past the render's own deadline with the worker still not
			// having returned (stuck in an unbounded native call, not
			// merely a slow one) — proceeding anyway matches every other
			// deadman-bounded wait in this file; the alternative is an
			// unkillable goroutine holding Session.Close open forever.
		}
		w.wb.stopWorkers()
		w.wb.forgetSharedBuffers()
		w.wb.forgetWasmMemories()
		w.wb.closeGPUDevices()
	}
}
