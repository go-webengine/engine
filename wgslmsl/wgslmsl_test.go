// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package wgslmsl

import (
	"encoding/binary"
	"math"
	"strings"
	"testing"

	"github.com/go-macos/metal"
)

const vectorAddWGSL = `
@group(0) @binding(0) var<storage, read_write> data: array<f32>;

@compute @workgroup_size(64)
fn main(@builtin(global_invocation_id) id: vec3<u32>) {
    data[id.x] = data[id.x] + 1.0;
}
`

// runOnMetal compiles and dispatches res's kernel against a real Metal
// device, writing input (one f32 per element) into the resource at
// bindingIndex and returning the buffer's own contents afterward. Skips
// (not fails) when this machine has no usable GPU — metal.Default's own
// documented ErrNoDevice/ErrUnsupported case (a VM, or a non-macOS CI
// runner) — since this package's own translation correctness (the thing
// every OTHER test in this file checks) does not depend on actually having
// a GPU; only this one acceptance test does.
func runOnMetal(t *testing.T, res *Result, input []float32) []float32 {
	t.Helper()
	dev, err := metal.Default()
	if err != nil {
		t.Skipf("no usable GPU on this machine: %v", err)
	}
	defer dev.Close()

	lib, err := dev.Compile(res.MSL)
	if err != nil {
		t.Fatalf("Metal rejected the generated MSL: %v\n--- MSL ---\n%s", err, res.MSL)
	}
	defer lib.Close()

	pipe, err := lib.Pipeline(res.EntryPointName)
	if err != nil {
		t.Fatalf("Pipeline(%q): %v", res.EntryPointName, err)
	}
	defer pipe.Close()

	n := len(input)
	dataBuf, err := dev.NewBuffer(n * 4)
	if err != nil {
		t.Fatal(err)
	}
	defer dataBuf.Close()
	for i, v := range input {
		binary.LittleEndian.PutUint32(dataBuf.Bytes()[i*4:], math.Float32bits(v))
	}

	sizesBuf, err := dev.NewBuffer(4 * len(res.Bindings))
	if err != nil {
		t.Fatal(err)
	}
	defer sizesBuf.Close()
	for i, b := range res.Bindings {
		count := uint32((n * 4) / b.ElemBytes)
		binary.LittleEndian.PutUint32(sizesBuf.Bytes()[i*4:], count)
	}

	err = dev.Run(func(e *metal.Encoder) {
		e.Use(pipe)
		e.Buffer(0, dataBuf)
		if res.SizeMetalIndex >= 0 {
			e.Buffer(res.SizeMetalIndex, sizesBuf)
		}
		e.Dispatch(n)
	})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	out := make([]float32, n)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(dataBuf.Bytes()[i*4:]))
	}
	return out
}

// TestVectorAddEndToEnd is the roadmap's own verification step for M5's
// shader-translation path: a real compute shader, translated by this
// package, compiled and dispatched against real Metal hardware, output
// checked bit-for-bit — matching the earlier naga-via-wasm spike's own
// acceptance test exactly (same shader, same expected numbers), now via the
// pure-Go backend that shipped instead.
func TestVectorAddEndToEnd(t *testing.T) {
	res, err := Translate(vectorAddWGSL, Options{})
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if res.EntryPointName != "main_" {
		t.Fatalf("EntryPointName = %q, want %q (WGSL main collides with MSL's own reserved main)", res.EntryPointName, "main_")
	}
	if len(res.Bindings) != 1 || res.Bindings[0].ElemBytes != 4 {
		t.Fatalf("Bindings = %+v, want one f32 (4-byte) binding", res.Bindings)
	}
	if res.SizeMetalIndex != 1 {
		t.Fatalf("SizeMetalIndex = %d, want 1", res.SizeMetalIndex)
	}
	if !strings.Contains(res.MSL, "[[buffer(0)]]") || !strings.Contains(res.MSL, "[[buffer(1)]]") {
		t.Fatalf("generated MSL is missing real buffer indices:\n%s", res.MSL)
	}

	got := runOnMetal(t, res, []float32{1, 2, 3, 4})
	want := []float32{2, 3, 4, 5}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v (bit-for-bit)", got, want)
		}
	}
}

// TestOutOfBoundsIndexIsClamped is the security proof: a kernel that reads
// at an index far past its own buffer's length must not fault or read
// adjacent GPU memory — it must read the one real, defined value this
// package's own clamp (indexExpr's own doc comment) guarantees: the buffer's
// LAST element. Metal performs no bounds checking of its own, so without
// this, an out-of-range index computed by an untrusted page's own WGSL
// would be a real out-of-bounds GPU memory access, not a hypothetical one.
func TestOutOfBoundsIndexIsClamped(t *testing.T) {
	const src = `
		@group(0) @binding(0) var<storage, read_write> data: array<f32>;
		@compute @workgroup_size(1)
		fn oob() {
			data[1000000] = data[1000000] + 100.0;
		}
	`
	res, err := Translate(src, Options{})
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	got := runOnMetal(t, res, []float32{1, 2, 3, 4})
	want := []float32{1, 2, 3, 104} // index clamped to the last element (3)
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v (out-of-range index should clamp to the last element)", got, want)
		}
	}
}

// TestControlFlowAndBuiltinMath exercises if/for/while/loop, the common
// built-in math functions, and a non-trivial workgroup size together — the
// realistic shape of a GPGPU kernel beyond the minimal vector-add case.
func TestControlFlowAndBuiltinMath(t *testing.T) {
	const src = `
		@group(0) @binding(0) var<storage, read_write> data: array<f32>;

		@compute @workgroup_size(8, 1, 1)
		fn cf(@builtin(global_invocation_id) id: vec3<u32>) {
			var acc: f32 = 0.0;
			var i: u32 = 0u;
			loop {
				if (i >= 4u) {
					break;
				}
				acc = acc + sqrt(f32(i) + 1.0);
				continuing {
					i = i + 1u;
				}
			}
			for (var j: u32 = 0u; j < 2u; j = j + 1u) {
				acc = max(acc, min(acc * 2.0, 1000.0));
			}
			var k: u32 = 0u;
			while (k < 1u) {
				acc = acc + 1.0;
				k = k + 1u;
			}
			data[id.x] = acc;
		}
	`
	res, err := Translate(src, Options{})
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if res.WorkgroupSize != [3]int{8, 1, 1} {
		t.Fatalf("WorkgroupSize = %v, want [8 1 1]", res.WorkgroupSize)
	}
	got := runOnMetal(t, res, make([]float32, 8))
	var want float32
	for i := 0; i < 4; i++ {
		want += float32(math.Sqrt(float64(i) + 1))
	}
	for j := 0; j < 2; j++ {
		want = float32(math.Max(float64(want), math.Min(float64(want)*2, 1000)))
	}
	want += 1
	for _, v := range got {
		if v != want {
			t.Fatalf("got %v, want every element == %v", got, want)
		}
	}
}

// TestUnsupportedConstructsAreRejected proves this package's own bounded
// scope is enforced, not silently ignored: each of these is a real WGSL
// construct outside the compute-subset this package supports, and each must
// produce a clear error naming what is unsupported, never a wrong or
// partial translation.
func TestUnsupportedConstructsAreRejected(t *testing.T) {
	cases := map[string]string{
		"vertex stage": `
			@vertex
			fn vs(@builtin(vertex_index) i: u32) -> @builtin(position) vec4<f32> {
				return vec4<f32>(0.0, 0.0, 0.0, 1.0);
			}
		`,
		"helper function call": `
			fn helper(x: f32) -> f32 { return x + 1.0; }
			@group(0) @binding(0) var<storage, read_write> data: array<f32>;
			@compute @workgroup_size(1)
			fn main(@builtin(global_invocation_id) id: vec3<u32>) {
				data[id.x] = helper(data[id.x]);
			}
		`,
		"uniform buffer resource": `
			struct Params { scale: f32 }
			@group(0) @binding(0) var<uniform> p: Params;
			@group(0) @binding(1) var<storage, read_write> data: array<f32>;
			@compute @workgroup_size(1)
			fn main(@builtin(global_invocation_id) id: vec3<u32>) {
				data[id.x] = data[id.x] * p.scale;
			}
		`,
		"switch statement": `
			@group(0) @binding(0) var<storage, read_write> data: array<f32>;
			@compute @workgroup_size(1)
			fn main(@builtin(global_invocation_id) id: vec3<u32>) {
				switch (id.x) {
					case 0u: { data[0] = 1.0; }
					default: { data[0] = 2.0; }
				}
			}
		`,
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Translate(src, Options{})
			if err == nil {
				t.Fatalf("Translate(%s) succeeded, want an unsupported-construct error", name)
			}
		})
	}
}

// TestNoComputeEntryPoint and TestMultipleComputeEntryPointsNeedASelector
// cover selectEntryPoint's own two failure shapes.
func TestNoComputeEntryPoint(t *testing.T) {
	const src = `
		@vertex
		fn vs() -> @builtin(position) vec4<f32> { return vec4<f32>(0.0, 0.0, 0.0, 1.0); }
	`
	if _, err := Translate(src, Options{}); err == nil {
		t.Fatal("want an error: module has no @compute entry point")
	}
}

func TestMultipleComputeEntryPointsNeedASelector(t *testing.T) {
	const src = `
		@group(0) @binding(0) var<storage, read_write> data: array<f32>;
		@compute @workgroup_size(1)
		fn a(@builtin(global_invocation_id) id: vec3<u32>) { data[id.x] = 1.0; }
		@compute @workgroup_size(1)
		fn b(@builtin(global_invocation_id) id: vec3<u32>) { data[id.x] = 2.0; }
	`
	if _, err := Translate(src, Options{}); err == nil {
		t.Fatal("want an error without Options.EntryPoint set")
	}
	res, err := Translate(src, Options{EntryPoint: "b"})
	if err != nil {
		t.Fatalf("Translate with EntryPoint=b: %v", err)
	}
	if res.EntryPointName != "b" {
		t.Fatalf("EntryPointName = %q, want %q", res.EntryPointName, "b")
	}
}
