// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package js

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-webengine/engine/dom"
)

// workerServer serves the small worker scripts the tests below need.
func workerServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/echo.js":
			w.Write([]byte(`
				onmessage = function(e) {
					console.log('worker got: ' + e.data);
					postMessage('pong:' + e.data);
				};
			`))
		case "/404.js":
			w.WriteHeader(404)
		default:
			w.WriteHeader(500)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestWorkerPostMessageRoundTrip proves M2's own core contract (issue #342,
// the roadmap's own verification plan): two independent binders on two
// goroutines, a postMessage round trip observed via console.log on BOTH
// sides, deterministic (no sleeps, no retries) because Worker.postMessage
// blocks until the worker has fully handled the message and delivered
// whatever it replied with.
func TestWorkerPostMessageRoundTrip(t *testing.T) {
	srv := workerServer(t)
	_, logs, res := runJS(t, page(`
		var w = new Worker('`+srv.URL+`/echo.js');
		w.onmessage = function(e) { console.log('main got: ' + e.data); };
		w.postMessage('ping');
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	mustHave(t, logs, "worker got: ping", "main got: pong:ping")
}

// TestWorkerFetchFailureDispatchesError mirrors runScripts' own failed-
// external-script precedent: a Worker whose script 404s dispatches "error"
// on the wrapper rather than throwing out of `new Worker`.
func TestWorkerFetchFailureDispatchesError(t *testing.T) {
	srv := workerServer(t)
	_, logs, res := runJS(t, page(`
		var w = new Worker('`+srv.URL+`/404.js');
		w.onerror = function() { console.log('worker errored'); };
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v", res)
	}
	mustHave(t, logs, "worker errored")
}

// TestWorkerCloneUnsupportedValueLogged proves postMessage with a value this
// round's structured-clone subset cannot represent (a function) logs and
// returns rather than panicking or hanging the caller.
func TestWorkerCloneUnsupportedValueLogged(t *testing.T) {
	srv := workerServer(t)
	_, logs, _ := runJS(t, page(`
		var w = new Worker('`+srv.URL+`/echo.js');
		w.postMessage(function(){});
	`))
	mustHave(t, logs, "Worker.postMessage: structured clone: a function cannot be cloned")
}

// TestWorkerTerminateIsImmediate proves terminate() actually stops the
// worker's goroutine (closes stopCh) rather than merely being accepted and
// ignored.
func TestWorkerTerminateIsImmediate(t *testing.T) {
	srv := workerServer(t)
	root, err := dom.Parse(page(`
		var w = new Worker('` + srv.URL + `/echo.js');
		w.terminate();
	`))
	if err != nil {
		t.Fatal(err)
	}
	sess := Begin(root, Options{PageURL: testURL, Client: http.DefaultClient, Timeout: 3 * time.Second})
	sess.RunInitial()
	defer sess.Close()

	if len(sess.b.workers) != 1 {
		t.Fatalf("want 1 worker, got %d", len(sess.b.workers))
	}
	select {
	case <-sess.b.workers[0].stopCh:
	default:
		t.Fatal("terminate() should have closed the worker's stopCh")
	}
}

// TestWorkerAddEventListenerRemoveListenerAndSelfClose exercises the
// addEventListener/removeEventListener path on BOTH sides (not just the
// onX-assignment idiom the round-trip test above already covers) and a
// worker closing itself (self.close()) mid-handler, rather than only ever
// being terminated from the main side.
func TestWorkerAddEventListenerRemoveListenerAndSelfClose(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/listener.js" {
			w.Write([]byte(`
				self.addEventListener('message', function(e) {
					console.log('worker got: ' + e.data);
					postMessage('ack:' + e.data);
					close();
				});
			`))
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()

	root, logs, res := runJS(t, page(`
		var w = new Worker('`+srv.URL+`/listener.js');
		var extra = 0;
		function extraHandler() { extra++; }
		w.addEventListener('message', extraHandler);
		w.removeEventListener('message', extraHandler);
		w.addEventListener('message', function(e) { console.log('main got: ' + e.data); });
		w.postMessage('x');
		document.body.setAttribute('data-extra', String(extra));
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	mustHave(t, logs, "worker got: x", "main got: ack:x")
	if got, _ := dom.Find(root, "body").Attribute("data-extra"); got != "0" {
		t.Fatalf("removeEventListener did not remove the handler before postMessage: data-extra=%q", got)
	}
}

// TestWorkerCloneUnsupportedValueFromWorkerLogged proves the WORKER side of
// postMessage (installWorkerGlobals, not the main-side postToWorker already
// covered above) also logs and drops a value it cannot clone, rather than
// losing every subsequent message from the same handler.
func TestWorkerCloneUnsupportedValueFromWorkerLogged(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`onmessage = function(e){ postMessage(function(){}); postMessage('ok'); };`))
	}))
	defer srv.Close()

	_, logs, res := runJS(t, page(`
		var w = new Worker('`+srv.URL+`/w.js');
		w.onmessage = function(e) { console.log('main got: ' + e.data); };
		w.postMessage('go');
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	mustHave(t, logs, "postMessage: structured clone: a function cannot be cloned", "main got: ok")
}

// TestSessionCloseStopsWorkers proves Close() terminates every worker the
// session created, so a render never leaks a worker goroutine past its own
// session's lifetime.
func TestSessionCloseStopsWorkers(t *testing.T) {
	srv := workerServer(t)
	root, err := dom.Parse(page(`var w = new Worker('` + srv.URL + `/echo.js');`))
	if err != nil {
		t.Fatal(err)
	}
	sess := Begin(root, Options{PageURL: testURL, Client: http.DefaultClient, Timeout: 3 * time.Second})
	sess.RunInitial()
	if len(sess.b.workers) != 1 {
		t.Fatalf("want 1 worker, got %d", len(sess.b.workers))
	}
	w := sess.b.workers[0]

	sess.Close()

	select {
	case <-w.stopCh:
	default:
		t.Fatal("Session.Close() should stop every live worker")
	}
}

// slowGPUDispatchWorkerSrc registers a self-rescheduling-free, ONE-shot
// setTimeout whose callback runs a real, deliberately slow Metal compute
// dispatch (a single invocation doing a large serial loop, not relying on
// parallelism, so its wall-clock cost is controllable and repeatable). The
// top-level script itself returns almost instantly — the dispatch only
// starts once w.run's own ticker drains the timer, AUTONOMOUSLY, with no
// main-side call (postMessage, or new Worker() itself) blocked on it. This
// is the exact shape go-wasmcontainers' own "the worker runs for the
// container's whole lifetime... posts unsolicited messages at any time"
// describes, and the only way to get the worker's own goroutine genuinely
// busy inside a long native call with nothing on the main side waiting for
// it: both new Worker() and Worker.postMessage already block until the
// worker goroutine finishes the work they triggered, so a race can only
// start from timer-driven, truly unsolicited worker-side activity.
const slowGPUDispatchWorkerSrc = `
	setTimeout(function() {
		navigator.gpu.requestAdapter().then(function(a) {
			if (!a) { return; }
			return a.requestDevice();
		}).then(function(dev) {
			if (!dev) { return; }
			var module = dev.createShaderModule({ code: ` + "`" + `
				@group(0) @binding(0) var<storage, read_write> data: array<u32>;
				@compute @workgroup_size(1)
				fn main() {
					// sqrt(), not a trivial increment: a closed-form-foldable
					// loop body gets strength-reduced away by the shader
					// compiler (measured: 1.6 BILLION plain increments took
					// 48ms, LESS than 80 million of these), making the
					// dispatch far too fast for this test's own purpose.
					var x: f32 = 0.0;
					for (var i: u32 = 0u; i < 80000000u; i = i + 1u) {
						x = x + sqrt(f32(i) + 1.0);
					}
					data[0] = u32(x);
				}
			` + "`" + ` });
			var pipeline = dev.createComputePipeline({ layout: 'auto', compute: { module: module } });
			var buf = dev.createBuffer({ size: 4, usage: GPUBufferUsage.STORAGE });
			var bindGroup = dev.createBindGroup({
				layout: pipeline.getBindGroupLayout(0),
				entries: [{ binding: 0, resource: { buffer: buf } }],
			});
			var encoder = dev.createCommandEncoder();
			var pass = encoder.beginComputePass();
			pass.setPipeline(pipeline);
			pass.setBindGroup(0, bindGroup);
			pass.dispatchWorkgroups(1);
			pass.end();
			dev.queue.submit([encoder.finish()]);
		});
	}, 0);
`

// TestSessionCloseWaitsForWorkerBeforeClosingGPUDevices is a regression
// test for the real root cause behind a native SIGSEGV go-wasmcontainers
// reported — and which an earlier, independently-real go-macos/metal fix
// (AutoreleasePool wrapping, v0.6.1) did NOT resolve, because it was a
// DIFFERENT gap. stopWorkers used to signal a worker to stop and
// IMMEDIATELY release its GPU devices, without waiting for the worker's
// own goroutine (the only one jsWorker's own struct doc comment says may
// touch its binder) to actually stop touching them first. A worker whose
// own autonomously-scheduled timer callback is still inside a slow,
// blocking real GPU dispatch is not observing the stop signal at all — it
// is buried inside a native Metal call, deaf to anything but that call
// returning.
//
// Deterministic, not timing-luck, by construction: both new Worker() and
// Worker.postMessage already block the MAIN goroutine until the worker's
// own goroutine finishes whatever they triggered (see their own doc
// comments), so neither can create the race window this bug needs — only
// genuinely unsolicited, timer-driven worker-side activity can (see
// slowGPUDispatchWorkerSrc's own doc comment). This test waits past
// workerTickInterval so the worker's ticker has started the slow dispatch,
// then calls Close() WHILE it is still running and times how long Close()
// itself takes: the old code returned in microseconds regardless (it
// never waited on anything); the fix cannot return before w.run itself
// returns, which cannot happen before the ~hundred-millisecond dispatch
// does. A real elapsed-time floor, not a channel check, is the only
// externally observable proof available here — the dispatch runs
// autonomously, with nothing to synchronously await it against.
func TestSessionCloseWaitsForWorkerBeforeClosingGPUDevices(t *testing.T) {
	// Checked FIRST, and separately, exactly as webgpu_test.go's own
	// TestRequestAdapterNeverRejects does: a GPU adapter may genuinely be
	// unavailable on some machine/CI runner, in which case the dispatch
	// below never happens at all and Close() returning instantly would be
	// CORRECT, not a bug — that must not be confused with "Close() was
	// fast because the race was lost," so GPU availability is settled as
	// its own precondition, skipped on explicitly, before the timing
	// assertion below is allowed to be a hard failure rather than an
	// ambiguous one.
	adapterRoot, _, adapterRes := runWebGPU(t, page(`
		navigator.gpu.requestAdapter().then(function(a) {
			document.body.setAttribute('data-adapter', a ? 'ok' : 'null');
		});
	`))
	if adapterRes.ScriptsFailed != 0 {
		t.Fatalf("adapter probe script failed: %+v", adapterRes)
	}
	if got, _ := dom.Find(adapterRoot, "body").Attribute("data-adapter"); got != "ok" {
		t.Skip("no usable GPU on this machine/platform — nothing to test the race against")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(slowGPUDispatchWorkerSrc))
	}))
	defer srv.Close()

	root, err := dom.Parse(page(`var w = new Worker('` + srv.URL + `/slowgpu.js');`))
	if err != nil {
		t.Fatal(err)
	}
	sess := Begin(root, Options{PageURL: testURL, Client: http.DefaultClient, Timeout: 3 * time.Second, EnableWebGPU: true})
	sess.RunInitial()
	if len(sess.b.workers) != 1 {
		t.Fatalf("want 1 worker, got %d", len(sess.b.workers))
	}

	// Past workerTickInterval (15ms): the worker's own ticker has fired at
	// least once, so its drainTimers call has already STARTED the slow
	// dispatch on the worker's own goroutine by the time Close() runs.
	time.Sleep(3 * workerTickInterval)

	start := time.Now()
	sess.Close()
	elapsed := time.Since(start)

	// With a confirmed real adapter above, the dispatch is genuinely
	// running (measured independently at ~600ms for this exact shader) —
	// Close() returning in under this generous floor, well below that,
	// can only mean it raced ahead instead of waiting for w.run to return.
	const floor = 150 * time.Millisecond
	if elapsed < floor {
		t.Fatalf("Session.Close() returned in %s, under the %s floor — it must block until the worker's in-flight GPU dispatch (and w.run itself) has finished before releasing anything the worker's binder owns", elapsed, floor)
	}
}

// TestWorkerPostMessageSharesTypedArrayView proves M3's completeness item
// confirmed directly by go-wasmcontainers: posting a typed-array VIEW
// itself (not the raw SharedArrayBuffer) shares the underlying memory with
// a Worker. The worker writes through the view it receives, reads the SAME
// byte back via a DIFFERENT view kind (DataView) over the identical
// buffer — proving cross-view consistency within one VM, go-wasmcontainers'
// item 1 — and main observes the write too, once its own (synchronous,
// blocking) postMessage call returns, proving the memory is genuinely
// shared across the VMs, not copied.
func TestWorkerPostMessageSharesTypedArrayView(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`
			onmessage = function(e) {
				var view = e.data;
				view[0] = 200;
				var dv = new DataView(view.buffer);
				postMessage('ctor=' + view.constructor.name + ' len=' + view.length + ' dvByte0=' + dv.getUint8(0));
			};
		`))
	}))
	defer srv.Close()

	root, err := dom.Parse(page(`
		var sab = new SharedArrayBuffer(4);
		var sendView = new Uint8Array(sab);
		sendView[0] = 1;
		var w = new Worker('` + srv.URL + `/view.js');
		w.onmessage = function(e) {
			document.body.setAttribute('data-reply', e.data);
			document.body.setAttribute('data-main-byte0', String(sendView[0]));
		};
		w.postMessage(sendView);
	`))
	if err != nil {
		t.Fatal(err)
	}
	var logs []string
	res := Run(root, Options{
		PageURL:             testURL,
		Client:              http.DefaultClient,
		Timeout:             3 * time.Second,
		Log:                 func(s string) { logs = append(logs, s) },
		CrossOriginIsolated: true,
	})
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}

	body := dom.Find(root, "body")
	if got, _ := body.Attribute("data-reply"); got != "ctor=Uint8Array len=4 dvByte0=200" {
		t.Fatalf("data-reply = %q, want %q", got, "ctor=Uint8Array len=4 dvByte0=200")
	}
	if got, _ := body.Attribute("data-main-byte0"); got != "200" {
		t.Fatalf("data-main-byte0 = %q, want %q (the worker's write through the shared view should be visible back on main)", got, "200")
	}
}

// TestWorkerReceivedSharedArrayBufferReportsCorrectIdentity is the EXACT
// shape of go-wasmcontainers' own failing e2e probe case ("posting a
// SharedArrayBuffer shares it"): their worker script checks
// `d instanceof SharedArrayBuffer` on the value it just received via
// postMessage, branching to a copy-path (silently wrong) if that check
// fails even though the underlying memory IS shared. Proves the
// RECONSTRUCTED object on the RECEIVING side — not just a freshly
// constructed one on the creating side (TestSharedArrayBufferHasItsOwnIdentity,
// atomics_test.go) — also reports the correct identity, since
// wrapSharedArrayBuffer is the one path both construction and
// reconstruction go through.
func TestWorkerReceivedSharedArrayBufferReportsCorrectIdentity(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`
			onmessage = (e) => {
				const d = e.data;
				const sab = d instanceof SharedArrayBuffer;
				const a = sab ? new Uint8Array(d) : d;
				a[0] = 42;
				postMessage((sab || a.buffer instanceof SharedArrayBuffer) ? "shared" : "copied");
			};
		`))
	}))
	defer srv.Close()

	root, err := dom.Parse(page(`
		var sab = new SharedArrayBuffer(16), view = new Uint8Array(sab);
		var w = new Worker('` + srv.URL + `/sab-worker.js');
		w.onmessage = function(e) {
			document.body.setAttribute('data-reply', e.data);
			document.body.setAttribute('data-view0', String(view[0]));
		};
		w.postMessage(sab);
	`))
	if err != nil {
		t.Fatal(err)
	}
	var logs []string
	res := Run(root, Options{
		PageURL:             testURL,
		Client:              http.DefaultClient,
		Timeout:             3 * time.Second,
		Log:                 func(s string) { logs = append(logs, s) },
		CrossOriginIsolated: true,
	})
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}

	body := dom.Find(root, "body")
	if got, _ := body.Attribute("data-reply"); got != "shared" {
		t.Fatalf("data-reply = %q, want %q (the worker's own `d instanceof SharedArrayBuffer` check should be true)", got, "shared")
	}
	if got, _ := body.Attribute("data-view0"); got != "42" {
		t.Fatalf("data-view0 = %q, want %q (the worker's write should be visible through main's own original view)", got, "42")
	}
}

// TestWorkerReceivedWasmMemoryIsTrulyShared proves the exact architecture
// go-wasmcontainers confirmed directly needing: a shared `WebAssembly.Memory`
// object itself (not just its .buffer) posted to a Worker, with a write made
// through the WORKER's own reconstructed wrapper visible back through the
// MAIN thread's original one — i.e. the postMessage reconstructed a SECOND
// view over the SAME Go-level bytes (wrapSharedWasmMemory/sharedBuffer),
// never a copy. This is the "engine worker reads/writes the guest's memory
// directly" half of their own described flow.
func TestWorkerReceivedWasmMemoryIsTrulyShared(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`
			onmessage = (e) => {
				const mem = e.data;
				const view = new Int32Array(mem.buffer);
				view[0] = 4242;
				postMessage('tag=' + Object.prototype.toString.call(mem) + ' isSAB=' + (mem.buffer instanceof SharedArrayBuffer));
			};
		`))
	}))
	defer srv.Close()

	root, err := dom.Parse(page(`
		var mem = new WebAssembly.Memory({initial: 1, maximum: 2, shared: true});
		var view = new Int32Array(mem.buffer);
		var w = new Worker('` + srv.URL + `/mem-worker.js');
		w.onmessage = function(e) {
			document.body.setAttribute('data-reply', e.data);
			document.body.setAttribute('data-view0', String(view[0]));
		};
		w.postMessage(mem);
	`))
	if err != nil {
		t.Fatal(err)
	}
	var logs []string
	res := Run(root, Options{
		PageURL:             testURL,
		Client:              http.DefaultClient,
		Timeout:             3 * time.Second,
		Log:                 func(s string) { logs = append(logs, s) },
		CrossOriginIsolated: true,
	})
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}

	body := dom.Find(root, "body")
	if got, _ := body.Attribute("data-reply"); got != "tag=[object WebAssembly.Memory] isSAB=true" {
		t.Fatalf("data-reply = %q, want %q", got, "tag=[object WebAssembly.Memory] isSAB=true")
	}
	if got, _ := body.Attribute("data-view0"); got != "4242" {
		t.Fatalf("data-view0 = %q, want %q (the worker's write through its own reconstructed Memory should be visible through main's original one)", got, "4242")
	}
}

// TestWorkerReceivedModuleDeclaredSharedMemoryIsTrulyShared is
// TestWorkerReceivedWasmMemoryIsTrulyShared's own counterpart for
// go-wasmcontainers' ACTUAL confirmed shape: a module DECLARES its own
// shared memory (never a standalone `new WebAssembly.Memory(...)`), and
// the resulting instance.exports.memory is read back and posted to a
// second worker — exactly runtime/browser/worker.js's own flow as they
// described it. Proves wrapWasmMemory's isShared path produces a genuinely
// shared object through this SAME code path, not just the standalone
// constructor's.
func TestWorkerReceivedModuleDeclaredSharedMemoryIsTrulyShared(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`
			onmessage = (e) => {
				const mem = e.data;
				const view = new Int32Array(mem.buffer);
				view[0] = 1234;
				postMessage('tag=' + Object.prototype.toString.call(mem) + ' isSAB=' + (mem.buffer instanceof SharedArrayBuffer));
			};
		`))
	}))
	defer srv.Close()

	root, err := dom.Parse(page(`
		` + uint8ArrayLiteral("bytes", memoryOnlyModuleBytes(1, 2, true)) + `
		WebAssembly.instantiate(bytes, {}).then(function(result) {
			var mem = result.instance.exports.memory;
			var view = new Int32Array(mem.buffer);
			var w = new Worker('` + srv.URL + `/mem-worker.js');
			w.onmessage = function(e) {
				document.body.setAttribute('data-reply', e.data);
				document.body.setAttribute('data-view0', String(view[0]));
			};
			w.postMessage(mem);
		});
	`))
	if err != nil {
		t.Fatal(err)
	}
	var logs []string
	res := Run(root, Options{
		PageURL:             testURL,
		Client:              http.DefaultClient,
		Timeout:             3 * time.Second,
		Log:                 func(s string) { logs = append(logs, s) },
		CrossOriginIsolated: true,
	})
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}

	body := dom.Find(root, "body")
	if got, _ := body.Attribute("data-reply"); got != "tag=[object WebAssembly.Memory] isSAB=true" {
		t.Fatalf("data-reply = %q, want %q", got, "tag=[object WebAssembly.Memory] isSAB=true")
	}
	if got, _ := body.Attribute("data-view0"); got != "1234" {
		t.Fatalf("data-view0 = %q, want %q (the worker's write through its own reconstructed Memory should be visible through main's original one)", got, "1234")
	}
}

// TestNonSharedWasmMemoryCannotBePostMessaged proves the documented
// restriction (clone.go's own lookupWasmMemory check): postMessage-ing a
// NON-shared Memory is logged and dropped with a clear, specific reason —
// the SAME "logged and dropped, not thrown" contract
// TestWorkerCloneUnsupportedValueFromWorkerLogged already establishes for
// an unsupported value in general — rather than silently cloning as a
// useless plain object or crashing on its own `grow` function property.
func TestNonSharedWasmMemoryCannotBePostMessaged(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`onmessage = (e) => { postMessage('should-not-receive'); };`))
	}))
	defer srv.Close()

	_, logs, res := runJS(t, page(`
		var mem = new WebAssembly.Memory({initial: 1});
		var w = new Worker('`+srv.URL+`/noop-worker.js');
		w.postMessage(mem);
		w.postMessage('still-alive');
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	mustHave(t, logs, "postMessage: structured clone: a non-shared WebAssembly.Memory cannot be cloned")
}

// TestImportScriptsSharesGlobalScopeAndRunsInOrder proves the core contract
// (confirmed needed directly by go-wasmcontainers, whose classic worker's
// very first statement is `importScripts("wasm_exec.js")`): each url is
// fetched and run, IN ORDER, sharing the worker's own global scope exactly
// like multiple top-level <script> tags — a `var` from the first script is
// visible to the second, and both are visible to the worker's own
// top-level code afterward.
func TestImportScriptsSharesGlobalScopeAndRunsInOrder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/a.js":
			w.Write([]byte(`var fromA = 'a'; self.order = (self.order || []).concat('a');`))
		case "/b.js":
			w.Write([]byte(`var fromB = fromA + 'b'; self.order = self.order.concat('b');`))
		case "/worker.js":
			w.Write([]byte(`
				importScripts('/a.js', '/b.js');
				postMessage(fromB + ':' + order.join(','));
			`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	root, logs, res := runJS(t, page(`
		var w = new Worker('`+srv.URL+`/worker.js');
		w.onmessage = function(e) { document.body.setAttribute('data-reply', e.data); };
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	if got, _ := dom.Find(root, "body").Attribute("data-reply"); got != "ab:a,b" {
		t.Fatalf("data-reply = %q, want %q", got, "ab:a,b")
	}
}

// TestImportScriptsFetchFailureThrowsCatchably proves a fetch failure
// rethrows into the CALLING script (catchable there) rather than silently
// aborting the worker or being swallowed like a top-level script's own
// errors are.
func TestImportScriptsFetchFailureThrowsCatchably(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/worker.js" {
			w.Write([]byte(`
				try {
					importScripts('/missing.js');
					postMessage('should-not-reach-here');
				} catch (e) {
					postMessage('caught:' + e.name);
				}
			`))
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()

	root, logs, res := runJS(t, page(`
		var w = new Worker('`+srv.URL+`/worker.js');
		w.onmessage = function(e) { document.body.setAttribute('data-reply', e.data); };
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	if got, _ := dom.Find(root, "body").Attribute("data-reply"); got != "caught:TypeError" {
		t.Fatalf("data-reply = %q, want %q", got, "caught:TypeError")
	}
}

// TestImportScriptsUncaughtErrorPropagatesButWorkerSurvives proves an
// uncaught exception from an IMPORTED script propagates to the calling
// script (here, uncaught all the way out of the worker's own top-level
// script, so it is caught and logged exactly like any other top-level
// worker script error — see execute's own "a script error never
// propagates" doc comment) rather than crashing the worker goroutine or
// silently continuing as if nothing happened.
func TestImportScriptsUncaughtErrorPropagatesButWorkerSurvives(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/throws.js":
			w.Write([]byte(`throw new RangeError('boom from throws.js');`))
		case "/worker.js":
			w.Write([]byte(`
				importScripts('/throws.js');
				postMessage('should-not-reach-here');
			`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	root, err := dom.Parse(page(`
		var w = new Worker('` + srv.URL + `/worker.js');
		var replied = false;
		w.onmessage = function(e) { replied = true; };
	`))
	if err != nil {
		t.Fatal(err)
	}
	var logs []string
	res := Run(root, Options{
		PageURL: testURL,
		Client:  http.DefaultClient,
		Timeout: 3 * time.Second,
		Log:     func(s string) { logs = append(logs, s) },
	})
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed (the MAIN document's own script, not the worker's): %+v logs=%v", res, logs)
	}
	mustHave(t, logs, "boom from throws.js")
	if has(logs, "should-not-reach-here") {
		t.Fatal("the worker's own postMessage after importScripts should never have run")
	}
}

// TestDOMExceptionAvailableInWorkerForImportScripts is a narrower,
// deterministic check of the SyntaxError path alone (a URL containing a NUL
// byte, which net/url.Parse rejects), run synchronously inside the worker's
// own top-level script rather than depending on a second real fetch.
func TestDOMExceptionAvailableInWorkerForImportScripts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/worker.js" {
			w.Write([]byte("try {\n" +
				"importScripts('http://ex\\x00ample/a.js');\n" +
				"postMessage('should-not-reach-here');\n" +
				"} catch (e) {\n" +
				"postMessage('caught:' + e.name);\n" +
				"}\n"))
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()

	root, logs, res := runJS(t, page(`
		var w = new Worker('`+srv.URL+`/worker.js');
		w.onmessage = function(e) { document.body.setAttribute('data-reply', e.data); };
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	got, _ := dom.Find(root, "body").Attribute("data-reply")
	if got != "caught:SyntaxError" && got != "caught:TypeError" {
		t.Fatalf("data-reply = %q, want caught:SyntaxError (or caught:TypeError, if this Go version's net/url accepts the NUL byte)", got)
	}
}

// TestWorkerHasCrypto proves crypto.getRandomValues (and the rest of
// WebCryptoAPI's own [Exposed=(Window, Worker)] surface) is reachable from
// a Worker's own global scope — confirmed needed directly by
// go-wasmcontainers: Go's own wasm_exec.js calls it at startup, inside the
// worker, before anything else in their script runs, the same way
// DOMException's own absence from a Worker (installDOMExceptionOnly) was
// found: general-purpose plumbing riding along with the DOM-specific
// install() a Worker correctly skips.
func TestWorkerHasCrypto(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/worker.js" {
			w.Write([]byte(`
				var a = new Uint8Array(16);
				crypto.getRandomValues(a);
				var allZero = a.every(function(v) { return v === 0; });
				postMessage('hasCrypto=' + (typeof crypto) + ' allZero=' + allZero + ' uuidLen=' + crypto.randomUUID().length);
			`))
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()

	root, logs, res := runJS(t, page(`
		var w = new Worker('`+srv.URL+`/worker.js');
		w.onmessage = function(e) { document.body.setAttribute('data-reply', e.data); };
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	want := "hasCrypto=object allZero=false uuidLen=36"
	if got, _ := dom.Find(root, "body").Attribute("data-reply"); got != want {
		t.Fatalf("data-reply = %q, want %q", got, want)
	}
}

// TestWorkerHasGeneralPurposeGlobals is the exhaustive audit's own
// regression test: every general-purpose (non-DOM) global the main
// document's install() has, that the WebCryptoAPI-shaped gap could equally
// have hidden from a Worker, is reachable — TextEncoder/TextDecoder, URL,
// URLSearchParams, Intl, and performance.now() (a real, advancing clock
// as of round 7 — see newPerformance's own doc comment — shared by the
// main document and every Worker alike, so nothing is lost by sharing it).
func TestWorkerHasGeneralPurposeGlobals(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/worker.js" {
			w.Write([]byte(`
				var bytes = new TextEncoder().encode('hi');
				var text = new TextDecoder().decode(bytes);
				var u = new URL('/foo?x=1', 'https://ex.com/a/');
				var sp = new URLSearchParams('a=1&b=2');
				var intlOk = (typeof Intl === 'object') && (typeof Intl.NumberFormat === 'function');
				var perfOk = typeof performance.now() === 'number';
				postMessage([
					'bytesLen=' + bytes.length,
					'text=' + text,
					'url=' + u.href,
					'sp=' + sp.get('a') + ',' + sp.get('b'),
					'intlOk=' + intlOk,
					'perfOk=' + perfOk,
				].join(' '));
			`))
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()

	root, logs, res := runJS(t, page(`
		var w = new Worker('`+srv.URL+`/worker.js');
		w.onmessage = function(e) { document.body.setAttribute('data-reply', e.data); };
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	want := "bytesLen=2 text=hi url=https://ex.com/foo?x=1 sp=1,2 intlOk=true perfOk=true"
	if got, _ := dom.Find(root, "body").Attribute("data-reply"); got != want {
		t.Fatalf("data-reply = %q, want %q", got, want)
	}
}

// TestWorkerHasAbortControllerAndHeadersEntries reproduces go-wasmcontainers'
// own exact confirmed context: INSIDE a Worker, Go's js/wasm net/http
// transport (net/http/roundtrip_js.go) creates an AbortController before
// every fetch and passes its signal, then calls `.headers.entries()` on
// the response — both threw outright (not just missing data) before this
// round, aborting the guest before a single byte came back.
func TestWorkerHasAbortControllerAndHeadersEntries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/worker.js" {
			w.Write([]byte(`
				var c = new AbortController();
				fetch('/data.json', {signal: c.signal}).then(function(resp) {
					var pairs = [];
					for (var e of resp.headers.entries()) { pairs.push(e[0] + '=' + e[1]); }
					postMessage('abortedBefore=' + c.signal.aborted + ' headerEntries=' + pairs.join(','));
				}, function(err) { postMessage('REJECTED:' + err); });
			`))
			return
		}
		if r.URL.Path == "/data.json" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"ok":true}`))
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()

	root, logs, res := runJS(t, page(`
		var w = new Worker('`+srv.URL+`/worker.js');
		w.onmessage = function(e) { document.body.setAttribute('data-reply', e.data); };
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	got, _ := dom.Find(root, "body").Attribute("data-reply")
	if !strings.Contains(got, "abortedBefore=false") || !strings.Contains(got, "content-type=application/json") {
		t.Fatalf("data-reply = %q, want abortedBefore=false and a content-type header entry", got)
	}
}

// TestWorkerFetchResolvesAgainstTheWorkersOwnURL proves installNet's own
// fetch() inside a Worker benefits from the SAME base-URL fix
// importScripts needed (newWorker's own PageURL override): a relative
// fetch('./data.json') resolves against the WORKER's own script location,
// not the creating document's — the shape a GPU-compute worker fetching
// its own sibling model-weight file needs.
func TestWorkerFetchResolvesAgainstTheWorkersOwnURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sub/data.json":
			w.Write([]byte(`{"ok":true}`))
		case "/sub/worker.js":
			w.Write([]byte(`
				fetch('./data.json').then(function(r) { return r.json(); }).then(function(j) {
					postMessage('ok=' + j.ok);
				});
			`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	root, logs, res := runJS(t, page(`
		var w = new Worker('`+srv.URL+`/sub/worker.js');
		w.onmessage = function(e) { document.body.setAttribute('data-reply', e.data); };
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	if got, _ := dom.Find(root, "body").Attribute("data-reply"); got != "ok=true" {
		t.Fatalf("data-reply = %q, want %q", got, "ok=true")
	}
}

// TestWorkerHasWebAssembly proves WebAssembly is reachable from a Worker's
// own global scope — confirmed missing live by go-wasmcontainers' own
// worker-side capability probe ("WebAssembly is not defined"): Go's own
// wasm_exec.js calls WebAssembly.instantiateStreaming at startup, inside
// the worker. Reuses the SAME real compiled module (testdata/add.wasm,
// wasmFixtureScript) the main-document WebAssembly tests already exercise.
func TestWorkerHasWebAssembly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/worker.js" {
			w.Write([]byte(
				wasmFixtureScript(t) + `
				WebAssembly.instantiate(bytes, {env: {host_offset: function(x) { return x + 1000; }}}).then(function(result) {
					postMessage('add=' + result.instance.exports.add(2, 3));
				}, function(e) {
					postMessage('REJECTED:' + e);
				});
			`))
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()

	root, logs, res := runJS(t, page(`
		var w = new Worker('`+srv.URL+`/worker.js');
		w.onmessage = function(e) { document.body.setAttribute('data-reply', e.data); };
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	if got, _ := dom.Find(root, "body").Attribute("data-reply"); got != "add=1005" {
		t.Fatalf("data-reply = %q, want %q", got, "add=1005")
	}
}

// TestWorkerWasmInstanceofAndPerformanceNow reproduces go-wasmcontainers'
// own exact failing context: INSIDE a Worker (not the main document),
// `instance instanceof WebAssembly.Instance` must hold (Go's own
// wasm_exec.js, Go.run, throws "Value is not an object: undefined" at
// wasm_exec.js:480 otherwise) and performance.now() must advance with
// real elapsed time across a real busy-loop (their own probe: a
// 2e6-iteration loop between two calls gave the SAME value under the old
// always-0 stub — Chromium's own does not).
func TestWorkerWasmInstanceofAndPerformanceNow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/worker.js" {
			w.Write([]byte(
				wasmFixtureScript(t) + `
				WebAssembly.instantiate(bytes, {env: {host_offset: function(x) { return x + 1000; }}}).then(function(result) {
					var t0 = performance.now();
					var sum = 0;
					for (var i = 0; i < 2000000; i++) { sum += i; }
					var t1 = performance.now();
					postMessage('isInstance=' + (result.instance instanceof WebAssembly.Instance) + ' advanced=' + (t1 > t0) + ' sum=' + sum);
				}, function(e) {
					postMessage('REJECTED:' + e);
				});
			`))
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()

	root, logs, res := runJS(t, page(`
		var w = new Worker('`+srv.URL+`/worker.js');
		w.onmessage = function(e) { document.body.setAttribute('data-reply', e.data); };
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	got, _ := dom.Find(root, "body").Attribute("data-reply")
	if !strings.Contains(got, "isInstance=true") || !strings.Contains(got, "advanced=true") {
		t.Fatalf("data-reply = %q, want isInstance=true and advanced=true", got)
	}
}

// TestWorkerHasLocation proves location (WorkerLocation) is reachable from
// a Worker's own global scope and reflects the WORKER's own script URL —
// confirmed missing live by the same probe ("location is not defined"),
// which reads location.search/location.hostname.
func TestWorkerHasLocation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/sub/worker.js" {
			w.Write([]byte(`
				postMessage('hostname=' + location.hostname + ' search=' + location.search + ' pathname=' + location.pathname);
			`))
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()

	root, logs, res := runJS(t, page(`
		var w = new Worker('`+srv.URL+`/sub/worker.js?x=1');
		w.onmessage = function(e) { document.body.setAttribute('data-reply', e.data); };
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	srvURL, _ := url.Parse(srv.URL)
	want := "hostname=" + srvURL.Hostname() + " search=?x=1 pathname=/sub/worker.js"
	if got, _ := dom.Find(root, "body").Attribute("data-reply"); got != want {
		t.Fatalf("data-reply = %q, want %q (location must reflect the WORKER's own script URL, not the main page's)", got, want)
	}
}

// TestNestedWorkerAndCleanupCascade proves a worker can itself create a
// worker (HTML Standard §10.2.6 permits it; confirmed needed directly by
// go-wasmcontainers: their own engine spawns workers from within a
// worker), that the round trip through the grandchild works, AND that
// Session.Close's own cleanup cascades all the way down — the real fix
// stopWorkers' own doc comment describes: without it, a grandchild
// worker's goroutine would leak past the session's lifetime.
func TestNestedWorkerAndCleanupCascade(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/child.js":
			w.Write([]byte(`onmessage = function(e) { postMessage('grandchild got: ' + e.data); };`))
		case "/parent.js":
			w.Write([]byte(`
				var child = new Worker('child.js');
				child.onmessage = function(e) { postMessage(e.data); };
				child.postMessage('ping');
			`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	root, err := dom.Parse(page(`
		var w = new Worker('` + srv.URL + `/parent.js');
		w.onmessage = function(e) { document.body.setAttribute('data-reply', e.data); };
	`))
	if err != nil {
		t.Fatal(err)
	}
	sess := Begin(root, Options{PageURL: testURL, Client: http.DefaultClient, Timeout: 3 * time.Second})
	sess.RunInitial()
	if sess.res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v", sess.res)
	}
	if got, _ := dom.Find(root, "body").Attribute("data-reply"); got != "grandchild got: ping" {
		t.Fatalf("data-reply = %q, want %q", got, "grandchild got: ping")
	}
	if len(sess.b.workers) != 1 {
		t.Fatalf("want 1 top-level worker, got %d", len(sess.b.workers))
	}
	parent := sess.b.workers[0]
	if len(parent.wb.workers) != 1 {
		t.Fatalf("want 1 nested (grandchild) worker, got %d", len(parent.wb.workers))
	}
	grandchild := parent.wb.workers[0]

	sess.Close()

	select {
	case <-parent.stopCh:
	default:
		t.Fatal("Session.Close() should stop the top-level worker")
	}
	select {
	case <-grandchild.stopCh:
	default:
		t.Fatal("Session.Close() should cascade into stopping the nested (grandchild) worker too")
	}
}
