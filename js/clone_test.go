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
