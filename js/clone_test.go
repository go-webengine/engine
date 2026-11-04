// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package js

import (
	"fmt"
	"testing"

	"github.com/dop251/goja"
)

// TestCloneRoundTripPrimitivesArraysObjects proves cloneValue/reconstructValue
// together carry the "basic" structured-clone subset M2 needs across TWO
// independent goja.Runtimes (standing in for the main and worker VMs), with
// null and undefined (an own key whose value is undefined) kept distinct —
// the two properties `e.data === null` vs. `e.data === undefined` scripts
// commonly branch on.
func TestCloneRoundTripPrimitivesArraysObjects(t *testing.T) {
	src := goja.New()
	dst := &binder{vm: goja.New()}

	v, err := src.RunString(`({a: 1, b: "s", c: [1, 2, [3, 4]], d: null, e: undefined, f: true})`)
	if err != nil {
		t.Fatalf("source eval: %v", err)
	}
	cv, err := cloneValue(v)
	if err != nil {
		t.Fatalf("cloneValue: %v", err)
	}
	dst.vm.GlobalObject().Set("got", reconstructValue(dst, cv))
	check, err := dst.vm.RunString(`JSON.stringify([got.a, got.b, got.c, got.d, ('e' in got), got.f])`)
	if err != nil {
		t.Fatalf("dest eval: %v", err)
	}
	if want := `[1,"s",[1,2,[3,4]],null,true,true]`; check.String() != want {
		t.Fatalf("round trip = %s, want %s", check.String(), want)
	}
}

// TestCloneRoundTripsSharedTypedArrayView proves cloneValue/reconstructValue
// carry a typed-array VIEW over a SharedArrayBuffer by reference (not by
// value) — confirmed needed directly by go-wasmcontainers: their ring posts
// the Uint8Array view itself, not the raw buffer. The reconstructed view
// must both read the ORIGINAL bytes correctly (kind, length, values) and
// WRITE through to the exact same underlying []byte — proving this is
// genuinely shared memory, not a copy that merely looks right once.
func TestCloneRoundTripsSharedTypedArrayView(t *testing.T) {
	src := goja.New()
	dst := &binder{vm: goja.New()}

	data := make([]byte, 8)
	sb := &sharedBuffer{data: data}
	srcBufObj, _ := src.ToValue(src.NewArrayBuffer(data)).(*goja.Object)
	registerSharedBuffer(srcBufObj, sb)
	src.Set("sab", srcBufObj)

	v, err := src.RunString(`
		var view = new Uint8Array(sab, 2, 4);
		view[0] = 42;
		view[1] = 7;
		view;
	`)
	if err != nil {
		t.Fatalf("source eval: %v", err)
	}
	cv, err := cloneValue(v)
	if err != nil {
		t.Fatalf("cloneValue: %v", err)
	}
	reconstructed := reconstructValue(dst, cv)
	dst.vm.GlobalObject().Set("got", reconstructed)

	check, err := dst.vm.RunString(`JSON.stringify([got.constructor.name, got.length, got.byteOffset, got[0], got[1]])`)
	if err != nil {
		t.Fatalf("dest eval: %v", err)
	}
	if want := `["Uint8Array",4,2,42,7]`; check.String() != want {
		t.Fatalf("reconstructed view = %s, want %s", check.String(), want)
	}

	// Write through the RECONSTRUCTED view and confirm it lands in the
	// SAME underlying []byte the source VM's own view still reads from —
	// the whole point of SharedArrayBuffer, not just a correct-looking copy.
	if _, err := dst.vm.RunString(`got[2] = 99;`); err != nil {
		t.Fatalf("write through reconstructed view: %v", err)
	}
	if data[2+2] != 99 { // byteOffset 2 + index 2
		t.Fatalf("data[4] = %d, want 99 (write through the reconstructed view should hit the original []byte)", data[4])
	}
}

// TestCloneRoundTripsSharedDataView covers the OTHER branch
// reconstructValue's clonedSharedView case has: DataView, which (unlike
// every TypedArray kind) is constructed from a BYTE length directly, not an
// element count — a real, distinct code path, not just the same logic under
// a different name.
func TestCloneRoundTripsSharedDataView(t *testing.T) {
	src := goja.New()
	dst := &binder{vm: goja.New()}

	data := make([]byte, 8)
	sb := &sharedBuffer{data: data}
	srcBufObj, _ := src.ToValue(src.NewArrayBuffer(data)).(*goja.Object)
	registerSharedBuffer(srcBufObj, sb)
	src.Set("sab", srcBufObj)

	v, err := src.RunString(`
		var dv = new DataView(sab, 1, 4);
		dv.setUint8(0, 7);
		dv;
	`)
	if err != nil {
		t.Fatalf("source eval: %v", err)
	}
	cv, err := cloneValue(v)
	if err != nil {
		t.Fatalf("cloneValue: %v", err)
	}
	reconstructed := reconstructValue(dst, cv)
	dst.vm.GlobalObject().Set("got", reconstructed)

	check, err := dst.vm.RunString(`JSON.stringify([got.constructor.name, got.byteLength, got.byteOffset, got.getUint8(0)])`)
	if err != nil {
		t.Fatalf("dest eval: %v", err)
	}
	if want := `["DataView",4,1,7]`; check.String() != want {
		t.Fatalf("reconstructed DataView = %s, want %s", check.String(), want)
	}

	if _, err := dst.vm.RunString(`got.setUint8(1, 55);`); err != nil {
		t.Fatalf("write through reconstructed DataView: %v", err)
	}
	if data[1+1] != 55 { // byteOffset 1 + index 1
		t.Fatalf("data[2] = %d, want 55 (write through the reconstructed DataView should hit the original []byte)", data[2])
	}
}

// TestCloneRejectsFunction proves a function throws a DataCloneError-shaped
// Go error rather than silently vanishing or being miscloned as `{}`.
func TestCloneRejectsFunction(t *testing.T) {
	vm := goja.New()
	v, err := vm.RunString(`(function(){})`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cloneValue(v); err == nil {
		t.Fatal("cloneValue(function) should error, got nil")
	}
}

// TestCloneRejectsUnsupportedClass proves a Date (today's complete subset
// does not cover Date/RegExp/Map/Set/TypedArray) errors explicitly instead of
// silently falling through the plain-object path and losing its value.
func TestCloneRejectsUnsupportedClass(t *testing.T) {
	vm := goja.New()
	v, err := vm.RunString(`new Date(0)`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cloneValue(v); err == nil {
		t.Fatal("cloneValue(Date) should error, got nil")
	}
}

// TestCloneRejectsMapSetAndTypedArray covers a real, confirmed defect found
// while wiring SharedArrayBuffer through this same code (M3, engine#343):
// ClassName() reports "Object" — not a distinct name — for a Map, a Set, a
// plain (non-shared) ArrayBuffer, AND every TypedArray kind in this pinned
// goja build (confirmed directly via a small Go probe against the actual
// build, not assumed from the spec). Before the Export()-type-based check
// cloneAt now does, each of these silently cloned as an empty-ish plain
// object via the Keys()-based path instead of this file's own documented
// explicit rejection — losing the value's real content without even an
// error to notice by. Each case here must error, and specifically must NOT
// come back as a clonedObject with zero keys (the silent-empty-clone shape
// the bug actually produced).
func TestCloneRejectsMapSetAndTypedArray(t *testing.T) {
	vm := goja.New()
	cases := []string{
		"new Map([['a', 1]])",
		"new Set([1, 2, 3])",
		"new ArrayBuffer(8)",
		"new Int32Array(4)",
	}
	for _, src := range cases {
		v, err := vm.RunString(src)
		if err != nil {
			t.Fatalf("%s: eval: %v", src, err)
		}
		cv, err := cloneValue(v)
		if err == nil {
			t.Fatalf("%s: cloneValue should error, got a clone instead: %#v", src, cv)
		}
	}
}

// TestCloneDepthBound proves a deeply nested object cannot recurse past
// cloneMaxDepth.
func TestCloneDepthBound(t *testing.T) {
	vm := goja.New()
	script := fmt.Sprintf(`
		var o = {}, cur = o;
		for (var i = 0; i < %d; i++) { cur.next = {}; cur = cur.next; }
		o;
	`, cloneMaxDepth+5)
	v, err := vm.RunString(script)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cloneValue(v); err == nil {
		t.Fatal("cloneValue of an over-deep object should error, got nil")
	}
}

// TestCloneValuesBound proves a wide (many-keyed) object cannot allocate past
// cloneMaxValues, the same "always bounded" guarantee as maxCloneNodes.
func TestCloneValuesBound(t *testing.T) {
	vm := goja.New()
	script := fmt.Sprintf(`
		var o = {};
		for (var i = 0; i < %d; i++) { o['k'+i] = i; }
		o;
	`, cloneMaxValues+100)
	v, err := vm.RunString(script)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cloneValue(v); err == nil {
		t.Fatal("cloneValue of an over-wide object should error, got nil")
	}
}
