// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package js

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/dop251/goja"
)

// defaultMaxOPFSBytes bounds a single origin's OPFS tree when
// Options.MaxOPFSBytes is zero — the "zero selects a sane default, negative
// disables the guard" convention MaxWasmMemoryBytes/MaxJSHeapBytes already
// use. Generous for real per-origin caches/fixtures, small enough that a
// page cannot force unbounded disk growth just by writing in a loop.
const defaultMaxOPFSBytes = 50 << 20

// opfsInvalidName reports whether name fails the File System Standard's own
// valid-file-name test (not empty, not "." or ".."), PLUS this binding's own
// stricter, cross-platform additions: no "/", no "\" (the spec notes other
// platforms than Windows "might" allow a backslash; this binding refuses it
// everywhere rather than behave differently by host OS) and no NUL byte (not
// a path separator anywhere, but a known path-injection vector against
// C-string-based OS calls). Checking the WHOLE name against this set — never
// just rejecting ".." on its own — is what makes every
// getFileHandle/getDirectoryHandle/removeEntry call structurally
// traversal-safe: a name that passes can never contain a path separator, so
// filepath.Join(parent, name) can never leave parent, with no denylist of
// specific traversal sequences to keep in sync.
func opfsInvalidName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return true
	}
	return strings.ContainsAny(name, "/\\\x00")
}

// opfsOrigin reduces pageURL to its origin (scheme://host[:port]) the same
// way COOP/COEP isolation already scopes cross-origin-isolation decisions. A
// URL that fails to parse, or has no scheme/host (a file: document, a
// synthetic test PageURL with no real origin) still gets a stable, non-empty
// string distinct from every real origin, so OPFS isolation degrades to
// "scoped to this exact PageURL" rather than ever accidentally collapsing
// every opaque-origin page into one shared directory.
func opfsOrigin(pageURL string) string {
	if u, err := url.Parse(pageURL); err == nil && u.Scheme != "" && u.Host != "" {
		return u.Scheme + "://" + u.Host
	}
	return "opaque:" + pageURL
}

// opfsOriginDir resolves, creating if necessary, the real on-disk directory
// Options.OPFSRoot scopes this binder's own origin's OPFS tree under — one
// subdirectory per origin, named by a SHA-256 hash of the origin string
// rather than the origin text itself, so a hostname can never collide with
// or escape the scoping (no allowlist of safe characters needed to be sure)
// and strict origin isolation — confirmed required by the roadmap this
// shipped from (engine#344) — falls out structurally: two different origins
// always hash to two different, non-overlapping directories.
func (b *binder) opfsOriginDir() (string, error) {
	sum := sha256.Sum256([]byte(opfsOrigin(b.opt.PageURL)))
	dir := filepath.Join(b.opt.OPFSRoot, hex.EncodeToString(sum[:]))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// opfsMaxBytes resolves Options.MaxOPFSBytes per the zero/negative
// convention documented on engine.Engine.MaxOPFSBytes.
func (b *binder) opfsMaxBytes() int64 {
	switch {
	case b.opt.MaxOPFSBytes < 0:
		return -1
	case b.opt.MaxOPFSBytes == 0:
		return defaultMaxOPFSBytes
	default:
		return b.opt.MaxOPFSBytes
	}
}

// opfsUsage/opfsUsageMu track each origin directory's total byte usage,
// package-level (keyed by the origin directory's own absolute path, which is
// already globally unique for a given OPFSRoot) rather than per-binder —
// the same reasoning sharedBufferRegistry (atomics.go) documents: two
// renders or workers sharing one OPFSRoot must see the SAME quota ledger, not
// one each.
var (
	opfsUsageMu sync.Mutex
	opfsUsage   = map[string]int64{}
)

// opfsUsageLocked returns dir's total usage, walking it (once) the first
// time it's seen — a durable OPFSRoot may already hold bytes written by a
// previous process, so the ledger cannot simply start every origin at zero.
// Caller must hold opfsUsageMu.
func opfsUsageLocked(dir string) int64 {
	if v, ok := opfsUsage[dir]; ok {
		return v
	}
	total := opfsTreeBytes(dir)
	opfsUsage[dir] = total
	return total
}

// opfsTreeBytes sums the real on-disk size of every regular file under path
// (or path's own size, if it is itself a file); 0 for anything that cannot
// be statted (already gone, or never existed).
func opfsTreeBytes(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	if !info.IsDir() {
		return info.Size()
	}
	var total int64
	_ = filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if fi, ierr := d.Info(); ierr == nil {
			total += fi.Size()
		}
		return nil
	})
	return total
}

// opfsReserve accounts for a net change of delta bytes (positive = growth,
// negative = shrink or delete) against maxBytes (negative disables the
// guard), returning false and making NO change when a positive delta would
// exceed the quota — the caller must then not perform the underlying write.
// Checking and updating under the same lock closes the TOCTOU window a
// separate check-then-write would leave between two goroutines (two renders,
// or a render and a worker) sharing one origin directory. A negative delta
// (freeing space) always succeeds.
func opfsReserve(dir string, delta int64, maxBytes int64) bool {
	opfsUsageMu.Lock()
	defer opfsUsageMu.Unlock()
	cur := opfsUsageLocked(dir)
	if delta > 0 && maxBytes >= 0 && cur+delta > maxBytes {
		return false
	}
	next := cur + delta
	if next < 0 {
		next = 0
	}
	opfsUsage[dir] = next
	return true
}

// opfsLocks/opfsLocksMu back createSyncAccessHandle's own exclusive-lock
// requirement (File System Standard: "take a lock; if it fails, reject with
// NoModificationAllowedError") — package-level, keyed by the file's absolute
// path, for the same sharing reason opfsUsage is: two workers (or a worker
// and a future main-thread writable stream) opening the SAME file must see
// the SAME lock, not one each.
var (
	opfsLocksMu sync.Mutex
	opfsLocks   = map[string]bool{}
)

func opfsTryLock(path string) bool {
	opfsLocksMu.Lock()
	defer opfsLocksMu.Unlock()
	if opfsLocks[path] {
		return false
	}
	opfsLocks[path] = true
	return true
}

func opfsUnlock(path string) {
	opfsLocksMu.Lock()
	delete(opfsLocks, path)
	opfsLocksMu.Unlock()
}

// domException builds a real DOMException instance (name/message/code) via
// the DOMException constructor installInterfaces already registered as a
// global — going through b.vm.New (the same mechanism clone.go's
// reconstructValue uses to invoke a TypedArray/DataView constructor from Go)
// rather than hand-building a lookalike object, so `e instanceof
// DOMException` holds for an OPFS-thrown error exactly as it would for any
// other DOMException a script constructs itself.
func (b *binder) domException(name, message string) goja.Value {
	if ctor, ok := b.vm.Get("DOMException").(*goja.Object); ok {
		if v, err := b.vm.New(ctor, b.vm.ToValue(message), b.vm.ToValue(name)); err == nil {
			return v
		}
	}
	return b.vm.NewTypeError(name + ": " + message)
}

// bufferSourceBytes returns the real, zero-copy backing slice an ArrayBuffer
// or a TypedArray/DataView VIEW over one actually wraps (goja's own
// ArrayBuffer.Bytes(), the same zero-copy mechanism wasm.go's
// wrapWasmMemory and atomics.go's wrapSharedArrayBuffer both rely on) — so
// FileSystemSyncAccessHandle.read() writing into the returned slice is
// immediately visible through the script's own view, exactly as the spec's
// in-place copy is, with no intermediate Go-side buffer to keep in sync.
// Reuses clone.go's own isTypedArrayOrDataViewKind table to recognise a view
// by its constructor name, the same check that table already backs for
// postMessage.
func bufferSourceBytes(v goja.Value) ([]byte, bool) {
	obj, ok := v.(*goja.Object)
	if !ok {
		return nil, false
	}
	if ab, ok := obj.Export().(goja.ArrayBuffer); ok {
		return ab.Bytes(), true
	}
	ctor, _ := obj.Get("constructor").(*goja.Object)
	if ctor == nil || !isTypedArrayOrDataViewKind(ctor.Get("name").String()) {
		return nil, false
	}
	bufObj, _ := obj.Get("buffer").(*goja.Object)
	if bufObj == nil {
		return nil, false
	}
	ab, ok := bufObj.Export().(goja.ArrayBuffer)
	if !ok {
		return nil, false
	}
	data := ab.Bytes()
	off := int(obj.Get("byteOffset").ToInteger())
	length := int(obj.Get("byteLength").ToInteger())
	if off < 0 || length < 0 || off+length > len(data) {
		return nil, false
	}
	return data[off : off+length], true
}

// readWriteAt reads FileSystemReadWriteOptions.at, returning def (meaning
// "use the handle's own cursor") when v carries no object or no `at`.
func readWriteAt(v goja.Value, def int64) int64 {
	obj, ok := v.(*goja.Object)
	if !ok {
		return def
	}
	at := obj.Get("at")
	if at == nil || goja.IsUndefined(at) {
		return def
	}
	n := at.ToInteger()
	if n < 0 {
		n = 0
	}
	return n
}

// newStorageManager builds navigator.storage. getDirectory is wired only
// when Options.OPFSRoot is set — matching this package's own "nothing
// installed at all" convention for an opt-in capability (CrossOriginIsolated,
// EnableDoneSignal) rather than installing a function that always rejects:
// a script's own `'getDirectory' in navigator.storage` feature-detects
// correctly either way.
func (b *binder) newStorageManager() goja.Value {
	o := b.vm.NewObject()
	if b.opt.OPFSRoot != "" {
		o.Set("getDirectory", func(call goja.FunctionCall) goja.Value {
			promise, resolve, reject := b.vm.NewPromise()
			dir, err := b.opfsOriginDir()
			if err != nil {
				_ = reject(b.domException("InvalidStateError", err.Error()))
				return b.vm.ToValue(promise)
			}
			_ = resolve(b.newDirectoryHandle(dir, dir, ""))
			return b.vm.ToValue(promise)
		})
	}
	return o
}

// newDirectoryHandle wraps path (an already-validated, real directory inside
// originDir) as a FileSystemDirectoryHandle. originDir is threaded through
// unchanged from getDirectory() — every quota check under this handle's own
// subtree is always accounted against the ORIGIN's total, not this
// directory's own, matching the spec's own per-origin (not per-directory)
// quota model.
func (b *binder) newDirectoryHandle(path, originDir, name string) *goja.Object {
	o := b.vm.NewObject()
	o.Set("kind", "directory")
	o.Set("name", name)
	o.Set("getFileHandle", func(call goja.FunctionCall) goja.Value {
		return b.opfsGetEntry(path, originDir, call, false)
	})
	o.Set("getDirectoryHandle", func(call goja.FunctionCall) goja.Value {
		return b.opfsGetEntry(path, originDir, call, true)
	})
	o.Set("removeEntry", func(call goja.FunctionCall) goja.Value {
		return b.opfsRemoveEntry(path, originDir, call)
	})
	return o
}

// newFileHandle wraps path as a FileSystemFileHandle. createSyncAccessHandle
// is wired ONLY when b.isWorker — [Exposed=DedicatedWorker] in the spec —
// so a handle obtained from the main document's own navigator.storage simply
// has no such method, matching a real browser exactly rather than throwing a
// bespoke error.
func (b *binder) newFileHandle(path, originDir, name string) *goja.Object {
	o := b.vm.NewObject()
	o.Set("kind", "file")
	o.Set("name", name)
	if b.isWorker {
		o.Set("createSyncAccessHandle", func(call goja.FunctionCall) goja.Value {
			return b.opfsCreateSyncAccessHandle(path, originDir)
		})
	}
	return o
}

// opfsGetEntry implements getFileHandle/getDirectoryHandle (wantDir
// distinguishes them) — the File System Standard's own algorithm: validate
// the name, then look up or (if create is set) create the child, rejecting
// with TypeMismatchError when an entry of the OTHER kind already owns that
// name.
func (b *binder) opfsGetEntry(parentDir, originDir string, call goja.FunctionCall, wantDir bool) goja.Value {
	name := call.Argument(0).String()
	opts, _ := call.Argument(1).(*goja.Object)
	create := optBool(opts, "create")
	promise, resolve, reject := b.vm.NewPromise()
	if opfsInvalidName(name) {
		_ = reject(b.vm.NewTypeError("invalid file name: " + name))
		return b.vm.ToValue(promise)
	}
	child := filepath.Join(parentDir, name)
	info, err := os.Stat(child)
	switch {
	case err == nil && info.IsDir() != wantDir:
		_ = reject(b.domException("TypeMismatchError", name+" exists as the wrong kind of entry"))
	case err == nil && wantDir:
		_ = resolve(b.newDirectoryHandle(child, originDir, name))
	case err == nil:
		_ = resolve(b.newFileHandle(child, originDir, name))
	case os.IsNotExist(err) && !create:
		_ = reject(b.domException("NotFoundError", name+" does not exist"))
	case os.IsNotExist(err) && wantDir:
		if mkErr := os.Mkdir(child, 0o700); mkErr != nil {
			_ = reject(b.domException("InvalidStateError", mkErr.Error()))
		} else {
			_ = resolve(b.newDirectoryHandle(child, originDir, name))
		}
	case os.IsNotExist(err):
		f, cErr := os.OpenFile(child, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if cErr != nil {
			_ = reject(b.domException("InvalidStateError", cErr.Error()))
		} else {
			f.Close()
			_ = resolve(b.newFileHandle(child, originDir, name))
		}
	default:
		_ = reject(b.domException("InvalidStateError", err.Error()))
	}
	return b.vm.ToValue(promise)
}

// opfsRemoveEntry implements FileSystemDirectoryHandle.removeEntry: a
// non-empty directory without recursive rejects with
// InvalidModificationError; the removed subtree's bytes are given back to
// the origin's quota ledger (a shrink/delete, so opfsReserve never fails).
func (b *binder) opfsRemoveEntry(parentDir, originDir string, call goja.FunctionCall) goja.Value {
	name := call.Argument(0).String()
	opts, _ := call.Argument(1).(*goja.Object)
	recursive := optBool(opts, "recursive")
	promise, resolve, reject := b.vm.NewPromise()
	if opfsInvalidName(name) {
		_ = reject(b.vm.NewTypeError("invalid file name: " + name))
		return b.vm.ToValue(promise)
	}
	child := filepath.Join(parentDir, name)
	info, err := os.Stat(child)
	switch {
	case os.IsNotExist(err):
		_ = reject(b.domException("NotFoundError", name+" does not exist"))
		return b.vm.ToValue(promise)
	case err != nil:
		_ = reject(b.domException("InvalidStateError", err.Error()))
		return b.vm.ToValue(promise)
	}
	if info.IsDir() && !recursive {
		if entries, _ := os.ReadDir(child); len(entries) > 0 {
			_ = reject(b.domException("InvalidModificationError", name+" is not empty"))
			return b.vm.ToValue(promise)
		}
	}
	freed := opfsTreeBytes(child)
	var rmErr error
	if info.IsDir() {
		rmErr = os.RemoveAll(child)
	} else {
		rmErr = os.Remove(child)
	}
	if rmErr != nil {
		_ = reject(b.domException("InvalidStateError", rmErr.Error()))
		return b.vm.ToValue(promise)
	}
	opfsReserve(originDir, -freed, -1)
	_ = resolve(goja.Undefined())
	return b.vm.ToValue(promise)
}

// opfsAccessHandle is the Go-level state behind one
// FileSystemSyncAccessHandle: a real *os.File plus the handle's own cursor
// (File System Standard's "file position cursor") and closed flag. mu
// serialises every operation against this one handle — createSyncAccessHandle's
// own exclusive lock (opfsLocks) already guarantees no OTHER handle exists
// for the same path concurrently, but a script can still call read/write
// from more than one place if it shares the handle object around within one
// VM, so this stays correct either way.
type opfsAccessHandle struct {
	mu        sync.Mutex
	f         *os.File
	path      string
	originDir string
	maxBytes  int64
	cursor    int64
	closed    bool
}

// errOPFSHandleClosed is InvalidStateError's own underlying cause for every
// opfsAccessHandle method called after close().
var errOPFSHandleClosed = errors.New("access handle is closed")

// opfsQuotaError marks a failure specifically caused by MaxOPFSBytes, so the
// caller can map it to QuotaExceededError instead of the generic
// InvalidStateError every other failure gets.
type opfsQuotaError struct{ msg string }

func (e *opfsQuotaError) Error() string { return e.msg }

func (h *opfsAccessHandle) read(buf []byte, at int64) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return 0, errOPFSHandleClosed
	}
	pos := h.cursor
	if at >= 0 {
		pos = at
	}
	fi, err := h.f.Stat()
	if err != nil {
		return 0, err
	}
	size := fi.Size()
	if pos >= size {
		h.cursor = size
		return 0, nil
	}
	n, err := h.f.ReadAt(buf, pos)
	if errors.Is(err, io.EOF) {
		// A short read at EOF is expected, not a failure: n already holds
		// how much was actually there, matching the spec's own "copy what's
		// there, no error" contract for a read that reaches the end.
		err = nil
	}
	h.cursor = pos + int64(n)
	return n, err
}

// write implements FileSystemSyncAccessHandle.write: a write past the
// current end of file is allowed (the OS creates a sparse hole, matching the
// spec's own "pad with NUL bytes" requirement without this code needing to
// write them itself), bounded by the origin's own MaxOPFSBytes quota — a
// write that would grow the file past the quota is refused ENTIRELY (no
// partial write), unlike a real short write, since this binding has no
// reason to perform a write it knows up front cannot be honoured in full.
func (h *opfsAccessHandle) write(buf []byte, at int64) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return 0, errOPFSHandleClosed
	}
	pos := h.cursor
	if at >= 0 {
		pos = at
	}
	fi, err := h.f.Stat()
	if err != nil {
		return 0, err
	}
	oldSize := fi.Size()
	newEnd := pos + int64(len(buf))
	if newEnd > oldSize {
		if !opfsReserve(h.originDir, newEnd-oldSize, h.maxBytes) {
			return 0, &opfsQuotaError{"write would exceed the OPFS quota for this origin"}
		}
	}
	n, err := h.f.WriteAt(buf, pos)
	h.cursor = pos + int64(n)
	if err != nil && newEnd > oldSize {
		// Reconcile the optimistic reservation down to what the file's size
		// actually became, giving back whatever wasn't written.
		actualEnd := oldSize
		if grown := pos + int64(n); grown > actualEnd {
			actualEnd = grown
		}
		opfsReserve(h.originDir, actualEnd-newEnd, -1)
	}
	return n, err
}

// truncate implements FileSystemSyncAccessHandle.truncate: growing is quota
// checked exactly like write(); shrinking always succeeds and gives the
// freed bytes back to the origin's ledger.
func (h *opfsAccessHandle) truncate(newSize int64) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return errOPFSHandleClosed
	}
	if newSize < 0 {
		return errors.New("truncate: negative size")
	}
	fi, err := h.f.Stat()
	if err != nil {
		return err
	}
	oldSize := fi.Size()
	if newSize > oldSize {
		if !opfsReserve(h.originDir, newSize-oldSize, h.maxBytes) {
			return &opfsQuotaError{"truncate would exceed the OPFS quota for this origin"}
		}
	}
	if err := h.f.Truncate(newSize); err != nil {
		if newSize > oldSize {
			opfsReserve(h.originDir, oldSize-newSize, -1)
		}
		return err
	}
	if newSize < oldSize {
		opfsReserve(h.originDir, newSize-oldSize, -1)
	}
	if h.cursor > newSize {
		h.cursor = newSize
	}
	return nil
}

func (h *opfsAccessHandle) getSize() (int64, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return 0, errOPFSHandleClosed
	}
	fi, err := h.f.Stat()
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

func (h *opfsAccessHandle) flush() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return errOPFSHandleClosed
	}
	return h.f.Sync()
}

// close is idempotent (spec: "if already closed, return") and releases the
// exclusive lock so a later createSyncAccessHandle on the same path can
// succeed.
func (h *opfsAccessHandle) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	h.f.Close()
	opfsUnlock(h.path)
}

// opfsCreateSyncAccessHandle implements FileSystemFileHandle.createSyncAccessHandle:
// take the exclusive lock first (NoModificationAllowedError if another
// handle already holds it), then open — never the other order, so a failed
// open cannot leave a lock nobody will ever release.
func (b *binder) opfsCreateSyncAccessHandle(path, originDir string) goja.Value {
	promise, resolve, reject := b.vm.NewPromise()
	if !opfsTryLock(path) {
		_ = reject(b.domException("NoModificationAllowedError", "a sync access handle is already open for this file"))
		return b.vm.ToValue(promise)
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		opfsUnlock(path)
		_ = reject(b.domException("NotFoundError", err.Error()))
		return b.vm.ToValue(promise)
	}
	h := &opfsAccessHandle{f: f, path: path, originDir: originDir, maxBytes: b.opfsMaxBytes()}
	_ = resolve(b.wrapAccessHandle(h))
	return b.vm.ToValue(promise)
}

// wrapAccessHandle builds the synchronous (non-Promise-returning, per spec)
// JS surface over h. Every failure panics with a real DOMException/TypeError
// — the same panic-as-throw convention atomics.go's atomicsTypeError and
// every other binder method in this package already use — rather than
// returning one, since none of these six methods are spec'd to return a
// rejected promise.
func (b *binder) wrapAccessHandle(h *opfsAccessHandle) *goja.Object {
	throwFor := func(err error) {
		var qe *opfsQuotaError
		if errors.As(err, &qe) {
			panic(b.domException("QuotaExceededError", qe.msg))
		}
		panic(b.domException("InvalidStateError", err.Error()))
	}
	o := b.vm.NewObject()
	o.Set("read", func(call goja.FunctionCall) goja.Value {
		buf, ok := bufferSourceBytes(call.Argument(0))
		if !ok {
			panic(b.vm.NewTypeError("read: expected an ArrayBuffer or a typed array/DataView view"))
		}
		n, err := h.read(buf, readWriteAt(call.Argument(1), -1))
		if err != nil {
			throwFor(err)
		}
		return b.vm.ToValue(int64(n))
	})
	o.Set("write", func(call goja.FunctionCall) goja.Value {
		buf, ok := bufferSourceBytes(call.Argument(0))
		if !ok {
			panic(b.vm.NewTypeError("write: expected an ArrayBuffer or a typed array/DataView view"))
		}
		n, err := h.write(buf, readWriteAt(call.Argument(1), -1))
		if err != nil {
			throwFor(err)
		}
		return b.vm.ToValue(int64(n))
	})
	o.Set("truncate", func(call goja.FunctionCall) goja.Value {
		if err := h.truncate(call.Argument(0).ToInteger()); err != nil {
			throwFor(err)
		}
		return goja.Undefined()
	})
	o.Set("getSize", func(call goja.FunctionCall) goja.Value {
		n, err := h.getSize()
		if err != nil {
			throwFor(err)
		}
		return b.vm.ToValue(n)
	})
	o.Set("flush", func(call goja.FunctionCall) goja.Value {
		if err := h.flush(); err != nil {
			throwFor(err)
		}
		return goja.Undefined()
	})
	o.Set("close", func(call goja.FunctionCall) goja.Value {
		h.close()
		return goja.Undefined()
	})
	return o
}
