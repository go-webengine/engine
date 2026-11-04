// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package js

import (
	"encoding/binary"
	"sync"
	"time"

	"github.com/dop251/goja"
)

// maxSharedBufferBytes bounds a single new SharedArrayBuffer(n) call — the
// same "always bounded" convention as maxWasmMemoryPages/maxCloneNodes/
// maxRequests. Fixed rather than an Engine-level configurable field for this
// first round: no confirmed workload has needed more than a modest ring
// buffer; open an issue if one does.
const maxSharedBufferBytes = 64 << 20

// sharedBuffer is the Go-level identity a SharedArrayBuffer actually is: one
// real []byte, one real sync.Mutex guarding every read/write/Atomics op
// against it, and the registered Atomics.wait waiters. Every VM's own JS
// ArrayBuffer-classed wrapper object — the one new SharedArrayBuffer(n)
// itself creates, and one newly built by reconstructValue on each receiving
// VM after a postMessage — points at the SAME *sharedBuffer, which is what
// makes a write on one goroutine visible on another: unlike every other
// postMessage payload (clone.go), this is the one case that is NOT copied.
//
// The mutex exists for a reason beyond convenience: two real goroutines
// doing unsynchronized reads/writes of the SAME []byte is a Go-level data
// race (not just a JS-level one) that `go test -race` correctly flags on
// ANY access, synchronized or not, at the byte-slice level — routing every
// Atomics.* operation through this one mutex is what makes the
// IMPLEMENTATION itself race-free; a script's own un-Atomics'd access to the
// same buffer from two agents is still the script's own race, exactly as in
// a real engine (this is precisely what Atomics/SharedArrayBuffer exist to
// let a correct script avoid).
type sharedBuffer struct {
	mu      sync.Mutex
	data    []byte
	waiters map[int]map[int]chan struct{} // byte offset -> waiter id -> wake channel
	nextID  int
}

// addWaiter registers a new waiter at byte offset off. Caller must hold sb.mu.
func (sb *sharedBuffer) addWaiter(off int) (int, chan struct{}) {
	if sb.waiters == nil {
		sb.waiters = map[int]map[int]chan struct{}{}
	}
	if sb.waiters[off] == nil {
		sb.waiters[off] = map[int]chan struct{}{}
	}
	sb.nextID++
	id := sb.nextID
	ch := make(chan struct{})
	sb.waiters[off][id] = ch
	return id, ch
}

// removeWaiter drops a waiter that timed out before being woken — a no-op if
// notify already woke (and removed) it first, which is why this never
// double-closes a channel. Caller must hold sb.mu.
func (sb *sharedBuffer) removeWaiter(off, id int) {
	delete(sb.waiters[off], id)
}

// wake closes up to count waiters registered at off (FIFO by registration —
// Go map iteration order is NOT that, but the spec does not require a
// specific wake order among multiple waiters either, only that at most
// count are woken), removing each from the registry as it is woken so a
// racing removeWaiter (a wait that is ALSO timing out right now) never
// double-closes it. Caller must hold sb.mu.
func (sb *sharedBuffer) wake(off int, count int) int {
	m := sb.waiters[off]
	woken := 0
	for id, ch := range m {
		if woken >= count {
			break
		}
		close(ch)
		delete(m, id)
		woken++
	}
	return woken
}

// sharedBufferRegistry maps each VM-local ArrayBuffer wrapper object (one per
// VM a given SharedArrayBuffer has been created in or postMessage'd to) to
// the one Go-level sharedBuffer it shares with every other VM's own wrapper
// of the SAME buffer. A package-level registry (not a per-binder one) is
// deliberate: a *goja.Object pointer is already globally unique for the
// life of the process, so a single map correctly serves every binder's own
// objects without needing to thread a *binder through clone.go's otherwise
// binder-agnostic cloneValue/reconstructValue. Entries are removed by
// forgetSharedBuffers (called from Session.Close/stopWorkers) so a closed
// render's objects do not keep the whole registry — and the Go-level []byte
// AND goja.Object they key off — alive forever.
var (
	sharedBufferRegistryMu sync.Mutex
	sharedBufferRegistry   = map[*goja.Object]*sharedBuffer{}
)

func registerSharedBuffer(obj *goja.Object, sb *sharedBuffer) {
	sharedBufferRegistryMu.Lock()
	sharedBufferRegistry[obj] = sb
	sharedBufferRegistryMu.Unlock()
}

func lookupSharedBuffer(obj *goja.Object) (*sharedBuffer, bool) {
	sharedBufferRegistryMu.Lock()
	sb, ok := sharedBufferRegistry[obj]
	sharedBufferRegistryMu.Unlock()
	return sb, ok
}

// forgetSharedBuffers removes every object this binder ever registered (its
// own new SharedArrayBuffer(n) calls, and every postMessage'd buffer
// reconstructed into this binder's own VM) from the global registry.
func (b *binder) forgetSharedBuffers() {
	if len(b.ownedSharedBuffers) == 0 {
		return
	}
	sharedBufferRegistryMu.Lock()
	for _, obj := range b.ownedSharedBuffers {
		delete(sharedBufferRegistry, obj)
	}
	sharedBufferRegistryMu.Unlock()
	b.ownedSharedBuffers = nil
}

// sharedArrayBufferPrototype returns (building once, lazily, per binder/VM —
// a goja.Object belongs to one Runtime, so this cannot be shared across
// binders) the prototype every SharedArrayBuffer-wrapper object in THIS VM
// is stamped with: a plain object whose own prototype is this VM's real
// ArrayBuffer.prototype (inheriting slice/byteLength and every other real
// ArrayBuffer behaviour for free, since the wrapper genuinely IS one under
// the hood — see wrapSharedArrayBuffer), with Symbol.toStringTag overridden
// to "SharedArrayBuffer" via DefineDataPropertySymbol specifically (NOT
// Object.SetSymbol's ordinary [[Set]] semantics, which — confirmed directly
// against this pinned goja build — correctly REFUSES to shadow
// ArrayBuffer.prototype's own non-writable toStringTag, the same "can't
// shadow a non-writable inherited data property via plain assignment" rule
// real JS itself follows; DefineDataPropertySymbol uses [[DefineOwnProperty]]
// instead, which is allowed to). Makes `x instanceof SharedArrayBuffer` and
// `Object.prototype.toString.call(x) === "[object SharedArrayBuffer]"` both
// true — confirmed needed directly by go-wasmcontainers: real code (including
// their own probe) branches on `instanceof SharedArrayBuffer` to choose a
// shared-memory path over a copy path, so reporting plain ArrayBuffer here
// would send such code down the WRONG path even though the memory actually
// is shared. Stated, still-remaining simplification: `x instanceof
// ArrayBuffer` is ALSO true (sabProto's own prototype chain includes real
// ArrayBuffer.prototype), where the spec's two classes are siblings, not
// related by inheritance — no confirmed need for a script to tell those
// apart once `instanceof SharedArrayBuffer` itself is checked first, the
// pattern go-wasmcontainers' own code (and the common `if (x instanceof
// SharedArrayBuffer) … else if (x instanceof ArrayBuffer) …` idiom) uses.
func (b *binder) sharedArrayBufferPrototype() *goja.Object {
	if b.sabProto != nil {
		return b.sabProto
	}
	abProto, _ := b.vm.Get("ArrayBuffer").(*goja.Object).Get("prototype").(*goja.Object)
	proto := b.vm.NewObject()
	_ = proto.SetPrototype(abProto)
	_ = proto.DefineDataPropertySymbol(goja.SymToStringTag, b.vm.ToValue("SharedArrayBuffer"), goja.FLAG_FALSE, goja.FLAG_FALSE, goja.FLAG_TRUE)
	b.sabProto = proto
	return proto
}

// wrapSharedArrayBuffer builds a new ArrayBuffer-backed JS object in b's own
// VM over sb's own []byte (goja's real, zero-copy Runtime.NewArrayBuffer —
// the same mechanism wasm.go's wrapWasmMemory already relies on, which is
// why slicing/DataView/TypedArray construction all work for free), stamps
// it with this VM's own sharedArrayBufferPrototype, registers it in
// sharedBufferRegistry against sb, and records it on b.ownedSharedBuffers
// for later cleanup (forgetSharedBuffers). The one place every
// SharedArrayBuffer wrapper this binder ever creates — its own `new
// SharedArrayBuffer(n)`, or one reconstructed from an incoming postMessage
// (clone.go) — goes through, so all of them get the identical treatment.
func (b *binder) wrapSharedArrayBuffer(sb *sharedBuffer) *goja.Object {
	obj, _ := b.vm.ToValue(b.vm.NewArrayBuffer(sb.data)).(*goja.Object)
	_ = obj.SetPrototype(b.sharedArrayBufferPrototype())
	registerSharedBuffer(obj, sb)
	b.ownedSharedBuffers = append(b.ownedSharedBuffers, obj)
	return obj
}

// installSharedArrayBuffer wires the SharedArrayBuffer global, including its
// own `.prototype` (sharedArrayBufferPrototype) and that prototype's own
// `.constructor` back-reference, so `new SharedArrayBuffer(n).constructor
// === SharedArrayBuffer` holds like any other real constructor.
func (b *binder) installSharedArrayBuffer(g *goja.Object) {
	proto := b.sharedArrayBufferPrototype()
	ctor := func(call goja.ConstructorCall) *goja.Object {
		n := int(call.Argument(0).ToInteger())
		if n < 0 {
			n = 0
		}
		if n > maxSharedBufferBytes {
			panic(b.vm.NewTypeError("SharedArrayBuffer: requested size exceeds the limit"))
		}
		sb := &sharedBuffer{data: make([]byte, n)}
		return b.wrapSharedArrayBuffer(sb)
	}
	ctorVal := b.vm.ToValue(ctor)
	if ctorObj, ok := ctorVal.(*goja.Object); ok {
		// Without this, .name reads the wrapped Go closure's own derived
		// name (e.g. "installSharedArrayBuffer.func1") instead of
		// "SharedArrayBuffer" — confirmed directly, not assumed.
		_ = ctorObj.DefineDataProperty("name", b.vm.ToValue("SharedArrayBuffer"), goja.FLAG_FALSE, goja.FLAG_TRUE, goja.FLAG_FALSE)
		_ = ctorObj.Set("prototype", proto)
		_ = proto.DefineDataProperty("constructor", ctorVal, goja.FLAG_TRUE, goja.FLAG_FALSE, goja.FLAG_FALSE)
	}
	g.Set("SharedArrayBuffer", ctorVal)
}

// atomicsElemKind maps a TypedArray's own constructor name (read via the
// standard `.constructor.name` property every TypedArray exposes, rather
// than any goja-internal introspection — there is no public API for the
// latter) to its element size and signedness. Deliberately covers only the
// plain 8/16/32-bit integer kinds: BigInt64Array/BigUint64Array (64-bit) and
// Uint8ClampedArray/Float32Array/Float64Array (not Atomics-eligible per
// spec, or no confirmed 64-bit-counter need yet) are out of scope for this
// round — the ring-buffer control-word pattern this was built for
// (go-wasmcontainers, engine#343) uses Int32Array.
func atomicsElemKind(ctorName string) (size int, signed, ok bool) {
	switch ctorName {
	case "Int8Array":
		return 1, true, true
	case "Uint8Array":
		return 1, false, true
	case "Int16Array":
		return 2, true, true
	case "Uint16Array":
		return 2, false, true
	case "Int32Array":
		return 4, true, true
	case "Uint32Array":
		return 4, false, true
	}
	return 0, false, false
}

// resolveAtomicTarget resolves a (typedArray, index) Atomics argument pair
// into the shared buffer and absolute byte offset to act on. ok is false for
// anything this binding does not support: not a typed array, not one of the
// integer kinds atomicsElemKind knows, an out-of-range index, or — the one
// deliberate restriction — a typed array NOT backed by a SharedArrayBuffer
// this process has registered. Real engines allow Atomics.load/store/etc.
// (not wait/notify) on a plain ArrayBuffer too; this binding does not, since
// the one confirmed need for Atomics at all IS cross-VM shared memory — a
// same-VM-only caller has no reason to prefer it over plain indexing.
func (b *binder) resolveAtomicTarget(ta, idx goja.Value) (sb *sharedBuffer, byteOff, size int, signed, ok bool) {
	obj, isObj := ta.(*goja.Object)
	if !isObj {
		return nil, 0, 0, false, false
	}
	bufObj, _ := obj.Get("buffer").(*goja.Object)
	if bufObj == nil {
		return nil, 0, 0, false, false
	}
	sb, found := lookupSharedBuffer(bufObj)
	if !found {
		return nil, 0, 0, false, false
	}
	ctorName := ""
	if ctor, _ := obj.Get("constructor").(*goja.Object); ctor != nil {
		ctorName = ctor.Get("name").String()
	}
	size, signed, ok = atomicsElemKind(ctorName)
	if !ok {
		return nil, 0, 0, false, false
	}
	length := int(obj.Get("length").ToInteger())
	index := int(idx.ToInteger())
	if index < 0 || index >= length {
		return nil, 0, 0, false, false
	}
	byteOff = int(obj.Get("byteOffset").ToInteger()) + index*size
	return sb, byteOff, size, signed, true
}

// readElem/writeElem decode/encode one integer element at a byte offset,
// using the platform's native byte order — the same order goja's own
// TypedArray element access uses internally, since this reads/writes the
// identical underlying []byte a script's own `ta[i]` would.
func readElem(data []byte, off, size int, signed bool) int64 {
	switch size {
	case 1:
		v := data[off]
		if signed {
			return int64(int8(v))
		}
		return int64(v)
	case 2:
		v := binary.NativeEndian.Uint16(data[off : off+2])
		if signed {
			return int64(int16(v))
		}
		return int64(v)
	case 4:
		v := binary.NativeEndian.Uint32(data[off : off+4])
		if signed {
			return int64(int32(v))
		}
		return int64(v)
	}
	return 0
}

func writeElem(data []byte, off, size int, v int64) {
	switch size {
	case 1:
		data[off] = byte(v)
	case 2:
		binary.NativeEndian.PutUint16(data[off:off+2], uint16(v))
	case 4:
		binary.NativeEndian.PutUint32(data[off:off+4], uint32(v))
	}
}

// atomicsTypeError reports the one error shape every Atomics.* op in this
// binding throws for an unsupported target, naming the actual requirement
// rather than leaving a caller to guess from a generic "not supported".
func (b *binder) atomicsTypeError() goja.Value {
	panic(b.vm.NewTypeError("Atomics: requires an Int8/Uint8/Int16/Uint16/Int32/Uint32 Array backed by a SharedArrayBuffer"))
}

// installAtomics wires the Atomics global: load/store/add/compareExchange
// for the plain integer TypedArray kinds, wait/notify restricted to
// Int32Array (spec also allows BigInt64Array; out of scope, see
// atomicsElemKind). isLockFree always reports true — this binding's own
// mutex-per-buffer implementation is an internal detail a conformant script
// cannot observe either way, matching what every modern real engine reports
// for these sizes on common platforms.
func (b *binder) installAtomics(g *goja.Object) {
	a := b.vm.NewObject()
	a.Set("load", func(call goja.FunctionCall) goja.Value {
		sb, off, size, signed, ok := b.resolveAtomicTarget(call.Argument(0), call.Argument(1))
		if !ok {
			return b.atomicsTypeError()
		}
		sb.mu.Lock()
		v := readElem(sb.data, off, size, signed)
		sb.mu.Unlock()
		return b.vm.ToValue(v)
	})
	a.Set("store", func(call goja.FunctionCall) goja.Value {
		sb, off, size, _, ok := b.resolveAtomicTarget(call.Argument(0), call.Argument(1))
		if !ok {
			return b.atomicsTypeError()
		}
		v := call.Argument(2).ToInteger()
		sb.mu.Lock()
		writeElem(sb.data, off, size, v)
		sb.mu.Unlock()
		return call.Argument(2)
	})
	a.Set("add", func(call goja.FunctionCall) goja.Value {
		sb, off, size, signed, ok := b.resolveAtomicTarget(call.Argument(0), call.Argument(1))
		if !ok {
			return b.atomicsTypeError()
		}
		delta := call.Argument(2).ToInteger()
		sb.mu.Lock()
		old := readElem(sb.data, off, size, signed)
		writeElem(sb.data, off, size, old+delta)
		sb.mu.Unlock()
		return b.vm.ToValue(old)
	})
	a.Set("compareExchange", func(call goja.FunctionCall) goja.Value {
		sb, off, size, signed, ok := b.resolveAtomicTarget(call.Argument(0), call.Argument(1))
		if !ok {
			return b.atomicsTypeError()
		}
		expect := call.Argument(2).ToInteger()
		replacement := call.Argument(3).ToInteger()
		sb.mu.Lock()
		old := readElem(sb.data, off, size, signed)
		if old == expect {
			writeElem(sb.data, off, size, replacement)
		}
		sb.mu.Unlock()
		return b.vm.ToValue(old)
	})
	a.Set("isLockFree", func(goja.FunctionCall) goja.Value { return b.vm.ToValue(true) })
	a.Set("wait", b.atomicsWait)
	a.Set("notify", b.atomicsNotify)
	g.Set("Atomics", a)
}

// atomicsWait implements Atomics.wait(int32Array, index, value, timeout).
// Blocks the CALLING GOROUTINE (this worker's own, by construction — see
// jsWorker's own doc comment: one binder/goja.Runtime per goroutine) until a
// matching Atomics.notify wakes it or timeout milliseconds elapse (undefined
// timeout means wait forever, bounded only by the session's own JSTimeout
// deadman via the watchdog's Interrupt, same as any other long-running
// script). Must be called from the worker's own autonomous script/timer
// execution, never synchronously from within an onmessage handler
// delivering a main->worker postMessage — doing so would hold that
// postMessage call's own blocking round trip (worker.go's postToWorker)
// hostage waiting for a notify the blocked sender can no longer send. See
// README's own documented interaction for the recommended pattern
// (setTimeout(fn, 0) to yield before waiting).
func (b *binder) atomicsWait(call goja.FunctionCall) goja.Value {
	ta, _ := call.Argument(0).(*goja.Object)
	var ctorName string
	if ta != nil {
		if ctor, _ := ta.Get("constructor").(*goja.Object); ctor != nil {
			ctorName = ctor.Get("name").String()
		}
	}
	if ctorName != "Int32Array" {
		panic(b.vm.NewTypeError("Atomics.wait requires an Int32Array"))
	}
	sb, off, _, _, ok := b.resolveAtomicTarget(call.Argument(0), call.Argument(1))
	if !ok {
		panic(b.vm.NewTypeError("Atomics.wait requires an Int32Array backed by a SharedArrayBuffer"))
	}
	expect := int32(call.Argument(2).ToInteger())
	hasTimeout := call.Argument(3) != nil && !goja.IsUndefined(call.Argument(3))
	var timeout time.Duration
	if hasTimeout {
		ms := call.Argument(3).ToFloat()
		if ms < 0 {
			ms = 0
		}
		timeout = time.Duration(ms * float64(time.Millisecond))
	}

	sb.mu.Lock()
	if readElem(sb.data, off, 4, true) != int64(expect) {
		sb.mu.Unlock()
		return b.vm.ToValue("not-equal")
	}
	id, ch := sb.addWaiter(off)
	sb.mu.Unlock()

	if !hasTimeout {
		<-ch
		return b.vm.ToValue("ok")
	}
	select {
	case <-ch:
		return b.vm.ToValue("ok")
	case <-time.After(timeout):
		sb.mu.Lock()
		sb.removeWaiter(off, id)
		sb.mu.Unlock()
		return b.vm.ToValue("timed-out")
	}
}

// atomicsNotify implements Atomics.notify(int32Array, index, count),
// returning how many waiters were actually woken. An undefined count wakes
// every waiter at that offset (Infinity, per spec); resolveAtomicTarget
// failing (not a registered SharedArrayBuffer view) reports 0 woken, per
// spec, rather than throwing — notify on a non-shared buffer is a real,
// defined (if useless) no-op, unlike wait which requires one.
func (b *binder) atomicsNotify(call goja.FunctionCall) goja.Value {
	sb, off, _, _, ok := b.resolveAtomicTarget(call.Argument(0), call.Argument(1))
	if !ok {
		return b.vm.ToValue(0)
	}
	count := int(^uint(0) >> 1) // max int: "wake everyone" default
	if cv := call.Argument(2); cv != nil && !goja.IsUndefined(cv) {
		n := cv.ToInteger()
		if n < 0 {
			n = 0
		}
		count = int(n)
	}
	sb.mu.Lock()
	woken := sb.wake(off, count)
	sb.mu.Unlock()
	return b.vm.ToValue(woken)
}
