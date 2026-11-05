// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package js

import (
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
	// navigator.gpu + GPUBufferUsage/GPUMapMode (WebGPU, Exposed=(Window,
	// Worker)): same opt-in gate as the main document's own install().
	if wb.opt.EnableWebGPU {
		g.Set("GPUBufferUsage", wb.newGPUBufferUsage())
		g.Set("GPUMapMode", wb.newGPUMapMode())
	}

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

	src, ok := b.fetchScript(call.Argument(0).String())
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

	w := &jsWorker{
		wb:          newWorkerBinder(b.opt, b.deadman),
		wrapperNode: wrapperNode,
		inbox:       make(chan workerTask, workerInboxCap),
		stopCh:      make(chan struct{}),
		ready:       make(chan struct{}),
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
// session's lifetime.
func (b *binder) stopWorkers() {
	for _, w := range b.workers {
		w.stop.Do(func() { close(w.stopCh) })
		w.wb.forgetSharedBuffers()
		w.wb.closeGPUDevices()
	}
}
