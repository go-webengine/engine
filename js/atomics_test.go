// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package js

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-webengine/engine/dom"
)

// runJSCOI is runJS with CrossOriginIsolated set — SharedArrayBuffer/Atomics
// are gated on it (see window.go's own install() doc comment: a real
// browser denies both without COOP/COEP, confirmed as the right behaviour
// here too by go-wasmcontainers), so every test in this file needs it.
func runJSCOI(t *testing.T, htmlSrc string) (*dom.Node, []string, Result) {
	t.Helper()
	root, err := dom.Parse(htmlSrc)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var logs []string
	res := Run(root, Options{
		PageURL:             testURL,
		UserAgent:           "TestUA/1.0",
		Client:              http.DefaultClient,
		Timeout:             3 * time.Second,
		Log:                 func(s string) { logs = append(logs, s) },
		CrossOriginIsolated: true,
	})
	return root, logs, res
}

// TestSharedArrayBufferHasItsOwnIdentity proves SharedArrayBuffer reports
// its own identity, not plain ArrayBuffer's — confirmed needed directly by
// go-wasmcontainers: real code (including their own e2e probe) branches on
// `instanceof SharedArrayBuffer` to choose a shared-memory path over a copy
// path, so reporting "ArrayBuffer" here would send such code down the WRONG
// path even though the underlying memory genuinely is shared.
func TestSharedArrayBufferHasItsOwnIdentity(t *testing.T) {
	_, logs, res := runJSCOI(t, page(`
		var sab = new SharedArrayBuffer(8);
		console.log('instanceofSAB=' + (sab instanceof SharedArrayBuffer));
		console.log('tag=' + Object.prototype.toString.call(sab));
		console.log('ctorName=' + sab.constructor.name);
		console.log('ctorIsSAB=' + (sab.constructor === SharedArrayBuffer));
		console.log('isView=' + ArrayBuffer.isView(new Uint8Array(sab)));
		console.log('byteLength=' + sab.byteLength);
		// The peer's own probe idiom: branch on instanceof to pick a path.
		var path = sab instanceof SharedArrayBuffer ? 'shared-path' : 'copy-path';
		console.log('path=' + path);
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	mustHave(t, logs,
		"instanceofSAB=true",
		"tag=[object SharedArrayBuffer]",
		"ctorName=SharedArrayBuffer",
		"ctorIsSAB=true",
		"isView=true",
		"byteLength=8",
		"path=shared-path",
	)
}

// TestSharedArrayBufferAndAtomicsGatedOnCrossOriginIsolation proves the gate
// itself: without CrossOriginIsolated, SharedArrayBuffer/Atomics are not
// installed at all (window.SharedArrayBuffer is undefined, matching a real
// browser exactly — confirmed as the correct behaviour, not a shortcut, by
// go-wasmcontainers, who use this engine as a test lane beside real
// browsers), and window.crossOriginIsolated itself reads false. With it on,
// both are present.
func TestSharedArrayBufferAndAtomicsGatedOnCrossOriginIsolation(t *testing.T) {
	_, logs, res := runJS(t, page(`
		console.log('coi=' + crossOriginIsolated);
		console.log('sab=' + (typeof SharedArrayBuffer));
		console.log('atomics=' + (typeof Atomics));
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	mustHave(t, logs, "coi=false", "sab=undefined", "atomics=undefined")

	_, logs2, res2 := runJSCOI(t, page(`
		console.log('coi=' + crossOriginIsolated);
		console.log('sab=' + (typeof SharedArrayBuffer));
		console.log('atomics=' + (typeof Atomics));
	`))
	if res2.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res2, logs2)
	}
	mustHave(t, logs2, "coi=true", "sab=function", "atomics=object")
}

// TestAtomicsLoadStoreAddCompareExchange covers the non-blocking ops within
// a single VM (no Worker needed — these operate correctly same-VM too, the
// blocking wait/notify pair is the one that needs two real goroutines, see
// TestAtomicsWaitWokenByNotify below).
func TestAtomicsLoadStoreAddCompareExchange(t *testing.T) {
	_, logs, res := runJSCOI(t, page(`
		var sab = new SharedArrayBuffer(16);
		var v = new Int32Array(sab);
		Atomics.store(v, 0, 10);
		console.log('load=' + Atomics.load(v, 0));
		console.log('add-returns-old=' + Atomics.add(v, 0, 5));
		console.log('after-add=' + Atomics.load(v, 0));
		console.log('cas-match=' + Atomics.compareExchange(v, 0, 15, 99));
		console.log('after-cas-match=' + Atomics.load(v, 0));
		console.log('cas-nomatch=' + Atomics.compareExchange(v, 0, 15, 1));
		console.log('after-cas-nomatch=' + Atomics.load(v, 0));
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	mustHave(t, logs,
		"load=10",
		"add-returns-old=10",
		"after-add=15",
		"cas-match=15",
		"after-cas-match=99",
		"cas-nomatch=99",
		"after-cas-nomatch=99",
	)
}

// TestAtomicsOnPlainArrayBufferThrows proves this binding's own deliberate
// restriction (atomics.go's own doc comment: the one confirmed need for
// Atomics at all is cross-VM shared memory) throws a clear, named error
// rather than silently misbehaving against a plain ArrayBuffer's bytes.
func TestAtomicsOnPlainArrayBufferThrows(t *testing.T) {
	_, logs, res := runJSCOI(t, page(`
		try {
			var v = new Int32Array(new ArrayBuffer(16));
			Atomics.load(v, 0);
			console.log('should not reach here');
		} catch (e) {
			console.log('threw:' + e.name + ':' + e.message);
		}
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	found := false
	for _, l := range logs {
		if strings.HasPrefix(l, "log: threw:TypeError:") && strings.Contains(l, "SharedArrayBuffer") {
			found = true
		}
	}
	if !found {
		t.Fatalf("want a TypeError naming SharedArrayBuffer, got: %v", logs)
	}
}

// TestAtomicsWaitNotEqualReturnsImmediately proves Atomics.wait does not
// block at all when the current value already differs from the expected
// one — the fast, common, non-blocking path every ring-buffer consumer
// takes most of the time.
func TestAtomicsWaitNotEqualReturnsImmediately(t *testing.T) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, logs, res := runJSCOI(t, page(`
			var sab = new SharedArrayBuffer(4);
			var v = new Int32Array(sab);
			Atomics.store(v, 0, 1);
			console.log('result=' + Atomics.wait(v, 0, 0, 5000));
		`))
		if res.ScriptsFailed != 0 {
			t.Errorf("script failed: %+v logs=%v", res, logs)
		}
		mustHave(t, logs, "result=not-equal")
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Atomics.wait on a mismatched value blocked instead of returning immediately")
	}
}

// TestAtomicsWaitTimesOut proves a real, bounded timeout: nobody ever
// notifies, so wait must return "timed-out" close to the requested
// duration, never hang the test suite.
func TestAtomicsWaitTimesOut(t *testing.T) {
	start := time.Now()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, logs, res := runJSCOI(t, page(`
			var sab = new SharedArrayBuffer(4);
			var v = new Int32Array(sab);
			console.log('result=' + Atomics.wait(v, 0, 0, 50));
		`))
		if res.ScriptsFailed != 0 {
			t.Errorf("script failed: %+v logs=%v", res, logs)
		}
		mustHave(t, logs, "result=timed-out")
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Atomics.wait did not time out")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Atomics.wait took %v, want close to its own 50ms timeout", elapsed)
	}
}

// TestWorkerPostMessageDoesNotBlockItsOwnWakeUp reproduces go-wasmcontainers'
// own confirmed shape (engine#342 fallout): an "engine worker" creates a
// container Worker and posts it one setup message; the CONTAINER's own
// onmessage handler calls Atomics.wait SYNCHRONOUSLY, right there (worker.js
// from github.com/go-wasmcontainers/engine's own runtime/browser package,
// confirmed by reading its real source — not a hypothetical shape), relying
// on the CREATOR eventually calling Atomics.notify on the SAME buffer. This
// is valid, spec-compliant code: Worker.postMessage is defined as
// asynchronous (it queues a task and returns immediately) in every real
// browser, so the creator is never blocked and stays free to answer that
// notify. postToWorker's own historical doc comment admits this engine's
// postMessage is "deliberately synchronous/blocking" instead (a documented
// trade-off for deterministic tests, round M2) — which turns this exact,
// valid pattern into a real deadlock: the creator's postMessage call blocks
// waiting for the container to finish, but the container is itself blocked
// waiting for a notify only the (blocked) creator could ever send.
// TestWorkerPostMessageDoesNotBlockItsOwnWakeUp confirms a child blocked in
// Atomics.wait inside its own onmessage handler does not stop its parent's
// event loop: the parent's own answer (Atomics.notify, from ITS OWN
// onmessage handler reacting to the child's "wake" doorbell) must still get
// a chance to run and reach the child, entirely through the two workers'
// own normal, independent event loops — postMessage itself is async now and
// returns immediately, so this is driven by polling RunPending rather than
// observed synchronously right after postMessage, as round 17's own version
// of this test (written when postMessage still blocked the sender) did.
func TestWorkerPostMessageDoesNotBlockItsOwnWakeUp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`
			onmessage = function(e) {
				var i32 = new Int32Array(e.data.sab);
				// The "doorbell", posted BEFORE blocking (matching worker.js's
				// own call(): write args, postMessage to wake the creator,
				// THEN block) — the message the creator's own onmessage
				// handler must answer with Atomics.notify, on its own event
				// loop, independent of whatever this goroutine does next.
				postMessage('wake');
				var r = Atomics.wait(i32, 0, 1, 10000);
				postMessage('child:' + r);
			};
		`))
	}))
	defer srv.Close()

	root, err := dom.Parse(page(`
		var sab = new SharedArrayBuffer(4);
		var i32 = new Int32Array(sab);
		Atomics.store(i32, 0, 1);
		var w = new Worker('` + srv.URL + `');
		w.onmessage = function(e) {
			if (e.data === 'wake') {
				// Dispatched from the creator's OWN event loop (a normal
				// drainWorkerInboxes pass, via RunPending below), entirely
				// independent of the child's own run goroutine — exactly
				// where go-wasmcontainers' real onMessage answers a
				// container's "call" with Atomics.notify.
				document.body.setAttribute('data-woke', 'yes');
				Atomics.store(i32, 0, 2);
				Atomics.notify(i32, 0);
				return;
			}
			document.body.setAttribute('data-child', e.data);
		};
		w.postMessage({sab: sab});
	`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var logs []string
	sess := Begin(root, Options{
		PageURL:             testURL,
		UserAgent:           "TestUA/1.0",
		Client:              http.DefaultClient,
		Timeout:             2 * time.Second,
		Log:                 func(s string) { logs = append(logs, s) },
		CrossOriginIsolated: true,
	})
	defer sess.Close()
	sess.RunInitial()
	if res := sess.Result(); res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}

	start := time.Now()
	deadline := start.Add(2 * time.Second)
	for {
		if got, _ := dom.Find(root, "body").Attribute("data-child"); got == "child:ok" {
			break
		}
		if time.Now().After(deadline) {
			woke, _ := dom.Find(root, "body").Attribute("data-woke")
			child, _ := dom.Find(root, "body").Attribute("data-child")
			t.Fatalf("timed out waiting for data-child=%q: data-woke=%q data-child=%q (logs=%v)", "child:ok", woke, child, logs)
		}
		sess.RunPending()
		time.Sleep(workerTickInterval)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("round trip took %v, want well under the 2s deadman — the doorbell/notify handshake should settle in a couple of worker ticks, not the whole budget", elapsed)
	}
	if got, _ := dom.Find(root, "body").Attribute("data-woke"); got != "yes" {
		t.Fatalf("data-woke = %q, want %q: the creator's own event loop never answered its child's wake-up doorbell", got, "yes")
	}
}
