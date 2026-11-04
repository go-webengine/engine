// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package js

import (
	"fmt"
	"strconv"

	"github.com/dop251/goja"
)

// cloneMaxDepth / cloneMaxValues bound a structured-clone walk so a
// pathological or cyclic value cannot recurse or allocate without limit —
// the same "always bounded" convention as maxCloneNodes (dom.go) and
// maxRequests (net.go), applied here to a cross-VM postMessage payload
// (worker.go). A real cycle is caught by cloneMaxValues (each visited
// property consumes one of the budget, so a self-referencing object keeps
// growing the count instead of looping forever) rather than a separate
// visited-set, since this format has no identity to preserve across the
// clone (two clones of the same object are not the same object, matching
// the real algorithm's own by-value semantics once cycles are excluded).
const (
	cloneMaxDepth  = 64
	cloneMaxValues = 20000
)

// clonedValue is a VM-agnostic intermediate form of one cloned JS value: nil
// (JS undefined), cloneNull{} (JS null), bool, int64, float64, string,
// clonedArray, or clonedObject. It holds no goja.Value anywhere, so it is
// safe to build on one goroutine/VM (cloneValue) and reconstruct on a
// different one (reconstructValue) — the bridge every cross-VM feature
// (Worker.postMessage today; SharedArrayBuffer/Atomics later) needs, per
// goja's own documented one-VM-per-goroutine, no-cross-VM-object-values
// contract.
type clonedValue interface{}

// cloneNull marks a cloned JS null, kept distinct from untyped nil (JS
// undefined) so a reconstructed value is `=== null` or `=== undefined`
// exactly as the original was — real script commonly branches on that
// distinction (e.g. `e.data === null` for an explicit end-of-stream vs. an
// omitted message body).
type cloneNull struct{}

type clonedArray []clonedValue

// clonedObject is a plain object's own enumerable string-keyed properties,
// in insertion order (keys), so reconstruction produces the same property
// enumeration order a real structured clone would.
type clonedObject struct {
	keys   []string
	values map[string]clonedValue
}

// clonedSharedBuffer carries a *sharedBuffer (atomics.go) THROUGH the clone
// rather than copying its bytes — the one deliberate exception to this
// file's own "clone copies, never shares" rule, because a SharedArrayBuffer
// is defined by being the SAME memory on every side, not a copy of it.
type clonedSharedBuffer struct{ sb *sharedBuffer }

// cloneErr reports a value this round's structured-clone subset cannot
// represent — a function, Symbol, or an unsupported object class (Date,
// RegExp, Map, Set, ArrayBuffer/TypedArray — SharedArrayBuffer transfer is
// milestone 3's own job), or a value exceeding cloneMaxDepth/cloneMaxValues.
// Matches the real algorithm's own DataCloneError contract: a thrown
// exception, never a silently-dropped or silently-truncated clone a caller
// could mistake for a complete one.
type cloneErr struct{ msg string }

func (e *cloneErr) Error() string { return e.msg }

// cloneValue walks v (live in its OWN vm/goroutine) and produces a
// VM-agnostic copy, for a caller about to hand it to a value on a different
// goja.Runtime (see reconstructValue). It supports exactly the "basic"
// subset a first Worker postMessage round-trip needs: undefined, null,
// booleans, numbers, strings, plain Array, and plain Object.
func cloneValue(v goja.Value) (clonedValue, error) {
	n := 0
	return cloneAt(v, 0, &n)
}

func cloneAt(v goja.Value, depth int, n *int) (clonedValue, error) {
	if v == nil || goja.IsUndefined(v) {
		return nil, nil
	}
	if goja.IsNull(v) {
		return cloneNull{}, nil
	}
	if depth > cloneMaxDepth {
		return nil, &cloneErr{"structured clone: nesting too deep"}
	}
	*n++
	if *n > cloneMaxValues {
		return nil, &cloneErr{"structured clone: too many values"}
	}

	if obj, ok := v.(*goja.Object); ok {
		if _, isFn := goja.AssertFunction(v); isFn {
			return nil, &cloneErr{"structured clone: a function cannot be cloned"}
		}
		switch obj.ClassName() {
		case "Array":
			length := int(obj.Get("length").ToInteger())
			out := make(clonedArray, length)
			for i := 0; i < length; i++ {
				cv, err := cloneAt(obj.Get(strconv.Itoa(i)), depth+1, n)
				if err != nil {
					return nil, err
				}
				out[i] = cv
			}
			return out, nil
		case "Object":
			// ClassName()=="Object" is NOT reliable evidence this is a plain
			// object in this goja version — confirmed directly (a small Go
			// probe against the pinned build), ArrayBuffer, Map, Set, and
			// EVERY TypedArray kind all report "Object" too (only Array,
			// Date and RegExp get their own distinct name). Export()'s own
			// per-type Go wrapper is what actually tells them apart; without
			// this check, each of those was silently cloning as an empty-ish
			// plain object instead of the explicit rejection this file
			// documents — a real, confirmed bug fixed here, not a
			// hypothetical one (found while wiring SharedArrayBuffer
			// through this same path for M3, engine#343).
			switch ex := obj.Export().(type) {
			case goja.ArrayBuffer:
				// A SharedArrayBuffer (atomics.go) IS, under the hood, a
				// real goja ArrayBuffer — registered in sharedBufferRegistry
				// the moment it was created — so this is the only way to
				// tell it apart from a plain, non-shared ArrayBuffer (still
				// unsupported).
				if sb, ok := lookupSharedBuffer(obj); ok {
					return clonedSharedBuffer{sb: sb}, nil
				}
				return nil, &cloneErr{"structured clone: ArrayBuffer is not supported (use SharedArrayBuffer to share it with a Worker)"}
			case map[string]interface{}:
				keys := obj.Keys()
				co := clonedObject{keys: keys, values: make(map[string]clonedValue, len(keys))}
				for _, k := range keys {
					cv, err := cloneAt(obj.Get(k), depth+1, n)
					if err != nil {
						return nil, err
					}
					co.values[k] = cv
				}
				return co, nil
			default:
				// Map ([][2]interface{}), Set ([]interface{} — real Arrays
				// were already routed to the "Array" case above, so a slice
				// export reaching here cannot be one), every TypedArray kind
				// ([]int8/[]uint8/[]int16/…), or anything else exotic.
				return nil, &cloneErr{fmt.Sprintf("structured clone: unsupported value (%T)", ex)}
			}
		default:
			return nil, &cloneErr{"structured clone: " + obj.ClassName() + " is not supported"}
		}
	}

	switch ev := v.Export().(type) {
	case bool, int64, int, float64, string:
		return ev, nil
	default:
		return nil, &cloneErr{"structured clone: unsupported value"}
	}
}

// reconstructValue builds a value live in b's own VM (a DIFFERENT runtime
// than the one cloneValue read from) out of a VM-agnostic clone. Takes the
// receiving *binder, not a bare *goja.Runtime, because a clonedSharedBuffer
// must register its new per-VM wrapper object in sharedBufferRegistry
// (atomics.go) AND record it on b.ownedSharedBuffers so the registry entry
// is cleaned up once b's own session/worker closes (forgetSharedBuffers) —
// every other case here only ever needs b.vm.
func reconstructValue(b *binder, cv clonedValue) goja.Value {
	switch x := cv.(type) {
	case nil:
		return goja.Undefined()
	case cloneNull:
		return goja.Null()
	case clonedArray:
		items := make([]interface{}, len(x))
		for i, e := range x {
			items[i] = reconstructValue(b, e)
		}
		return b.vm.NewArray(items...)
	case clonedObject:
		o := b.vm.NewObject()
		for _, k := range x.keys {
			o.Set(k, reconstructValue(b, x.values[k]))
		}
		return o
	case clonedSharedBuffer:
		obj, _ := b.vm.ToValue(b.vm.NewArrayBuffer(x.sb.data)).(*goja.Object)
		registerSharedBuffer(obj, x.sb)
		b.ownedSharedBuffers = append(b.ownedSharedBuffers, obj)
		return obj
	default:
		return b.vm.ToValue(x)
	}
}
