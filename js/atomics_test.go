// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package js

import (
	"net/http"
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
