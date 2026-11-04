// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package js

import (
	"net/http"
	"net/http/httptest"
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
