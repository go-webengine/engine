// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package js

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-webengine/engine/dom"
)

// runOPFS is runJS with OPFSRoot/MaxOPFSBytes set — every OPFS test needs
// OPFSRoot (the feature is off by default, see Options.OPFSRoot's own doc
// comment).
func runOPFS(t *testing.T, htmlSrc, root string, maxBytes int64) (*dom.Node, []string, Result) {
	t.Helper()
	r, err := dom.Parse(htmlSrc)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var logs []string
	res := Run(r, Options{
		PageURL:      testURL,
		UserAgent:    "TestUA/1.0",
		Client:       http.DefaultClient,
		Timeout:      3 * time.Second,
		Log:          func(s string) { logs = append(logs, s) },
		OPFSRoot:     root,
		MaxOPFSBytes: maxBytes,
	})
	return r, logs, res
}

// TestOPFSDisabledByDefault proves Options.OPFSRoot's own "zero behaviour
// change" contract: with it empty, navigator.storage exists (matching a real
// StorageManager's own general presence) but getDirectory is not installed
// at all — a script's own `'getDirectory' in navigator.storage` feature
// detection reads false, exactly like a browser with no persistent-storage
// access granted, rather than a function that always rejects.
func TestOPFSDisabledByDefault(t *testing.T) {
	_, logs, res := runJS(t, page(`
		console.log('hasStorage=' + (typeof navigator.storage));
		console.log('hasGetDirectory=' + ('getDirectory' in navigator.storage));
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	mustHave(t, logs, "hasStorage=object", "hasGetDirectory=false")
}

// TestOPFSGetFileHandleAndDirectoryHandleRoundTrip exercises the roadmap's
// own listed mapping (getDirectory -> getFileHandle/getDirectoryHandle) on
// the MAIN thread — these are NOT Worker-only (only createSyncAccessHandle
// is) — and proves a file handle obtained here has no createSyncAccessHandle
// at all (see TestOPFSSyncAccessHandleRequiresWorker's own doc comment for
// why).
func TestOPFSGetFileHandleAndDirectoryHandleRoundTrip(t *testing.T) {
	_, logs, res := runOPFS(t, page(`
		navigator.storage.getDirectory().then(function(root) {
			console.log('rootKind=' + root.kind + ' rootName="' + root.name + '"');
			return root.getDirectoryHandle('sub', {create: true});
		}).then(function(dir) {
			console.log('dirKind=' + dir.kind + ' dirName=' + dir.name);
			return dir.getFileHandle('a.txt', {create: true});
		}).then(function(fh) {
			console.log('fileKind=' + fh.kind + ' fileName=' + fh.name);
			console.log('hasSync=' + (typeof fh.createSyncAccessHandle));
		}).catch(function(e) { console.log('ERR:' + e); });
	`), t.TempDir(), 0)
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	mustHave(t, logs,
		`rootKind=directory rootName=""`,
		"dirKind=directory dirName=sub",
		"fileKind=file fileName=a.txt",
		"hasSync=undefined",
	)
}

// TestOPFSGetFileHandleNotFoundWithoutCreate proves the File System
// Standard's own NotFoundError for a missing entry with create unset.
func TestOPFSGetFileHandleNotFoundWithoutCreate(t *testing.T) {
	_, logs, res := runOPFS(t, page(`
		navigator.storage.getDirectory().then(function(root) {
			return root.getFileHandle('missing.txt');
		}).then(function() {
			console.log('should-not-resolve');
		}, function(e) {
			console.log('rejected=' + e.name);
		});
	`), t.TempDir(), 0)
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	mustHave(t, logs, "rejected=NotFoundError")
}

// TestOPFSTypeMismatchBothWays proves getFileHandle on an existing
// directory's name, and getDirectoryHandle on an existing file's name, each
// reject with TypeMismatchError rather than silently returning the wrong
// kind of handle.
func TestOPFSTypeMismatchBothWays(t *testing.T) {
	_, logs, res := runOPFS(t, page(`
		var root;
		navigator.storage.getDirectory().then(function(r) {
			root = r;
			return root.getDirectoryHandle('d', {create: true});
		}).then(function() {
			return root.getFileHandle('f', {create: true});
		}).then(function() {
			return root.getFileHandle('d');
		}).then(function() {
			console.log('file-on-dir=should-not-resolve');
		}, function(e) {
			console.log('file-on-dir=' + e.name);
		}).then(function() {
			return root.getDirectoryHandle('f');
		}).then(function() {
			console.log('dir-on-file=should-not-resolve');
		}, function(e) {
			console.log('dir-on-file=' + e.name);
		});
	`), t.TempDir(), 0)
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	mustHave(t, logs, "file-on-dir=TypeMismatchError", "dir-on-file=TypeMismatchError")
}

// TestOPFSInvalidNamesRejected is the security pass's own core proof: every
// name the File System Standard (plus this binding's own stricter,
// cross-platform additions — see opfsInvalidName's own doc comment) deems
// invalid is rejected with a TypeError, INCLUDING a path-traversal attempt
// ("../escaped") — never silently joined into a path that could leave the
// origin's own directory.
func TestOPFSInvalidNamesRejected(t *testing.T) {
	opfsRoot := t.TempDir()
	_, logs, res := runOPFS(t, page(`
		var root, names = ['', '.', '..', '../escaped', 'a/b', 'a\\b'];
		navigator.storage.getDirectory().then(function(r) {
			root = r;
			var p = Promise.resolve();
			names.forEach(function(n) {
				p = p.then(function() {
					return root.getFileHandle(n, {create: true}).then(
						function() { console.log('name:' + JSON.stringify(n) + '=accepted'); },
						function(e) { console.log('name:' + JSON.stringify(n) + '=' + e.name); }
					);
				});
			});
			return p;
		});
	`), opfsRoot, 0)
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	for _, n := range []string{`""`, `"."`, `".."`, `"../escaped"`, `"a/b"`, `"a\\b"`} {
		mustHave(t, logs, "name:"+n+"=TypeError")
	}
	// Stronger than the rejection alone: the "../escaped" attempt must not
	// have actually created anything outside the per-origin directory —
	// neither directly under OPFSRoot nor one level above it.
	for _, p := range []string{
		filepath.Join(opfsRoot, "escaped"),
		filepath.Join(filepath.Dir(opfsRoot), "escaped"),
	} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("path-traversal escaped the OPFS root: %s exists (err=%v)", p, err)
		}
	}
}

// TestOPFSRemoveEntryRecursive proves the File System Standard's own
// InvalidModificationError for a non-empty directory without recursive, and
// a successful recursive removal, plus NotFoundError for a name that never
// existed.
func TestOPFSRemoveEntryRecursive(t *testing.T) {
	_, logs, res := runOPFS(t, page(`
		var root;
		navigator.storage.getDirectory().then(function(r) {
			root = r;
			return root.getDirectoryHandle('d', {create: true});
		}).then(function(d) {
			return d.getFileHandle('inner.txt', {create: true});
		}).then(function() {
			return root.removeEntry('d');
		}).then(function() {
			console.log('non-recursive=should-not-resolve');
		}, function(e) {
			console.log('non-recursive=' + e.name);
		}).then(function() {
			return root.removeEntry('d', {recursive: true});
		}).then(function() {
			console.log('recursive=ok');
			return root.removeEntry('d', {recursive: true});
		}).then(function() {
			console.log('double-remove=should-not-resolve');
		}, function(e) {
			console.log('double-remove=' + e.name);
		});
	`), t.TempDir(), 0)
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	mustHave(t, logs, "non-recursive=InvalidModificationError", "recursive=ok", "double-remove=NotFoundError")
}

// TestOPFSOriginIsolation proves the roadmap's own stated requirement:
// "two different pages fetched by the same Engine instance must never see
// each other's OPFS directory." Two renders share the SAME OPFSRoot but
// have different PageURLs (different origins); a file created under one
// origin is invisible (NotFoundError, not silently shared) to the other.
func TestOPFSOriginIsolation(t *testing.T) {
	sharedRoot := t.TempDir()

	writeDoc, err := dom.Parse(page(`
		navigator.storage.getDirectory().then(function(root) {
			return root.getFileHandle('secret.txt', {create: true});
		}).then(function() { console.log('written'); });
	`))
	if err != nil {
		t.Fatal(err)
	}
	var writeLogs []string
	writeRes := Run(writeDoc, Options{
		PageURL:   "https://origin-a.example/page",
		UserAgent: "TestUA/1.0",
		Client:    http.DefaultClient,
		Timeout:   3 * time.Second,
		Log:       func(s string) { writeLogs = append(writeLogs, s) },
		OPFSRoot:  sharedRoot,
	})
	if writeRes.ScriptsFailed != 0 {
		t.Fatalf("origin A script failed: %+v logs=%v", writeRes, writeLogs)
	}
	mustHave(t, writeLogs, "written")

	readDoc, err := dom.Parse(page(`
		navigator.storage.getDirectory().then(function(root) {
			return root.getFileHandle('secret.txt');
		}).then(function() {
			console.log('visible-from-origin-b');
		}, function(e) {
			console.log('hidden-from-origin-b=' + e.name);
		});
	`))
	if err != nil {
		t.Fatal(err)
	}
	var readLogs []string
	readRes := Run(readDoc, Options{
		PageURL:   "https://origin-b.example/page",
		UserAgent: "TestUA/1.0",
		Client:    http.DefaultClient,
		Timeout:   3 * time.Second,
		Log:       func(s string) { readLogs = append(readLogs, s) },
		OPFSRoot:  sharedRoot,
	})
	if readRes.ScriptsFailed != 0 {
		t.Fatalf("origin B script failed: %+v logs=%v", readRes, readLogs)
	}
	mustHave(t, readLogs, "hidden-from-origin-b=NotFoundError")
}

// opfsWorkerServer serves a worker script verbatim, like worker_test.go's
// own workerServer, for the tests below that need createSyncAccessHandle
// ([Exposed=DedicatedWorker] — meaningless outside a worker, see
// newFileHandle's own doc comment).
func opfsWorkerServer(t *testing.T, script string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(script))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestOPFSSyncAccessHandleRequiresWorker proves createSyncAccessHandle is
// simply absent from a FileSystemFileHandle obtained on the main document —
// matching a real browser's [Exposed=DedicatedWorker] restriction exactly,
// rather than a bespoke runtime error.
func TestOPFSSyncAccessHandleRequiresWorker(t *testing.T) {
	root, logs, res := runOPFS(t, page(`
		navigator.storage.getDirectory().then(function(root) {
			return root.getFileHandle('x.txt', {create: true});
		}).then(function(fh) {
			document.body.setAttribute('data-has-sync', String(typeof fh.createSyncAccessHandle));
		});
	`), t.TempDir(), 0)
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	if got, _ := dom.Find(root, "body").Attribute("data-has-sync"); got != "undefined" {
		t.Fatalf("data-has-sync = %q, want %q", got, "undefined")
	}
}

// TestOPFSSyncAccessHandleReadWriteRoundTrip is the roadmap's own M4
// verification item: a real file round-trip (write via
// createSyncAccessHandle, read back after close) against a t.TempDir()
// -scoped OPFSRoot, run inside a Worker since that is the only context the
// method exists in.
func TestOPFSSyncAccessHandleReadWriteRoundTrip(t *testing.T) {
	srv := opfsWorkerServer(t, `
		navigator.storage.getDirectory().then(function(root) {
			return root.getFileHandle('data.bin', {create: true});
		}).then(function(fh) {
			return fh.createSyncAccessHandle();
		}).then(function(h) {
			var out = new Uint8Array([72, 101, 108, 108, 111]); // "Hello"
			var written = h.write(out, {at: 0});
			h.flush();
			var size = h.getSize();
			var buf = new Uint8Array(5);
			var read = h.read(buf, {at: 0});
			h.truncate(3);
			var sizeAfterTruncate = h.getSize();
			h.close();
			var text = '';
			for (var i = 0; i < buf.length; i++) text += String.fromCharCode(buf[i]);
			postMessage('written=' + written + ' size=' + size + ' read=' + read +
				' text=' + text + ' truncated=' + sizeAfterTruncate);
		}).catch(function(e) { postMessage('ERR:' + e); });
	`)

	root, err := dom.Parse(page(`
		var w = new Worker('` + srv.URL + `/w.js');
		w.onmessage = function(e) { document.body.setAttribute('data-reply', e.data); };
	`))
	if err != nil {
		t.Fatal(err)
	}
	var logs []string
	res := Run(root, Options{
		PageURL:   testURL,
		Client:    http.DefaultClient,
		Timeout:   3 * time.Second,
		Log:       func(s string) { logs = append(logs, s) },
		OPFSRoot:  t.TempDir(),
	})
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	want := "written=5 size=5 read=5 text=Hello truncated=3"
	if got, _ := dom.Find(root, "body").Attribute("data-reply"); got != want {
		t.Fatalf("data-reply = %q, want %q (logs=%v)", got, want, logs)
	}
}

// TestOPFSSyncAccessHandleExclusiveLock proves createSyncAccessHandle's own
// exclusive lock: a second attempt on the SAME file while the first handle
// is still open rejects with NoModificationAllowedError, and closing the
// first lets a subsequent attempt succeed.
func TestOPFSSyncAccessHandleExclusiveLock(t *testing.T) {
	srv := opfsWorkerServer(t, `
		var fh;
		navigator.storage.getDirectory().then(function(root) {
			return root.getFileHandle('lock.bin', {create: true});
		}).then(function(f) {
			fh = f;
			return fh.createSyncAccessHandle();
		}).then(function(h1) {
			return fh.createSyncAccessHandle().then(
				function() { postMessage('second-open=unexpected-success'); },
				function(e) {
					postMessage('second-open=' + e.name);
					h1.close();
					return fh.createSyncAccessHandle().then(function(h3) {
						postMessage('reopen-after-close=ok');
						h3.close();
					});
				}
			);
		}).catch(function(e) { postMessage('ERR:' + e); });
	`)

	root, err := dom.Parse(page(`
		var w = new Worker('` + srv.URL + `/w.js');
		var replies = [];
		w.onmessage = function(e) {
			replies.push(e.data);
			document.body.setAttribute('data-replies', replies.join(','));
		};
	`))
	if err != nil {
		t.Fatal(err)
	}
	var logs []string
	res := Run(root, Options{
		PageURL:  testURL,
		Client:   http.DefaultClient,
		Timeout:  3 * time.Second,
		Log:      func(s string) { logs = append(logs, s) },
		OPFSRoot: t.TempDir(),
	})
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	want := "second-open=NoModificationAllowedError,reopen-after-close=ok"
	if got, _ := dom.Find(root, "body").Attribute("data-replies"); got != want {
		t.Fatalf("data-replies = %q, want %q (logs=%v)", got, want, logs)
	}
}

// TestOPFSSyncAccessHandleQuotaExceeded proves MaxOPFSBytes is actually
// enforced on write(): a write that would grow the file past a tiny quota
// throws QuotaExceededError synchronously (per spec, these six methods are
// NOT Promise-returning — the throw is a real, synchronous JS exception),
// and the file is left at its PRE-write size (no partial write accepted).
func TestOPFSSyncAccessHandleQuotaExceeded(t *testing.T) {
	srv := opfsWorkerServer(t, `
		navigator.storage.getDirectory().then(function(root) {
			return root.getFileHandle('big.bin', {create: true});
		}).then(function(fh) {
			return fh.createSyncAccessHandle();
		}).then(function(h) {
			var name = '';
			try {
				h.write(new Uint8Array(10), {at: 0});
				name = 'no-error';
			} catch (e) {
				name = e.name;
			}
			var size = h.getSize();
			h.close();
			postMessage('name=' + name + ' size=' + size);
		}).catch(function(e) { postMessage('ERR:' + e); });
	`)

	root, err := dom.Parse(page(`
		var w = new Worker('` + srv.URL + `/w.js');
		w.onmessage = function(e) { document.body.setAttribute('data-reply', e.data); };
	`))
	if err != nil {
		t.Fatal(err)
	}
	var logs []string
	res := Run(root, Options{
		PageURL:      testURL,
		Client:       http.DefaultClient,
		Timeout:      3 * time.Second,
		Log:          func(s string) { logs = append(logs, s) },
		OPFSRoot:     t.TempDir(),
		MaxOPFSBytes: 4, // smaller than the 10-byte write
	})
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	want := "name=QuotaExceededError size=0"
	if got, _ := dom.Find(root, "body").Attribute("data-reply"); got != want {
		t.Fatalf("data-reply = %q, want %q (logs=%v)", got, want, logs)
	}
}
