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

	var sizesBuf *metal.Buffer
	if res.SizeMetalIndex >= 0 {
		sizesBuf, err = dev.NewBuffer(4 * res.SizesCount)
		if err != nil {
			t.Fatal(err)
		}
		defer sizesBuf.Close()
		for _, b := range res.Bindings {
			if b.Kind != BindingStorageArray {
				continue
			}
			count := uint32((n * 4) / b.ElemBytes)
			binary.LittleEndian.PutUint32(sizesBuf.Bytes()[b.SizesIndex*4:], count)
		}
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

// TestHelperFunctionsEndToEnd reproduces go-wasmcontainers' own reported
// shape (go-webengine/engine#391 follow-up): a compute kernel's own
// "index math, activation" helper functions, called from the entry point
// and from EACH OTHER — a two-level chain (main -> scaleAndRelu -> relu),
// proving both that a call works at all and that emitHelperFuncs' own
// callee-before-caller ordering is right (get it wrong and Metal's
// compiler itself refuses the MSL: a function used before its own
// declaration). relu also calls a built-in (max) from INSIDE a helper, not
// just the entry body, since mslBuiltinFn's own callExpr path must work
// there too.
func TestHelperFunctionsEndToEnd(t *testing.T) {
	const src = `
		fn relu(x: f32) -> f32 {
			return max(x, 0.0);
		}
		fn scaleAndRelu(x: f32, scale: f32) -> f32 {
			return relu(x * scale);
		}
		@group(0) @binding(0) var<storage, read_write> data: array<f32>;
		@compute @workgroup_size(64)
		fn main(@builtin(global_invocation_id) id: vec3<u32>) {
			data[id.x] = scaleAndRelu(data[id.x], 2.0);
		}
	`
	res, err := Translate(src, Options{})
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if i, j := strings.Index(res.MSL, "float relu("), strings.Index(res.MSL, "float scaleAndRelu("); i < 0 || j < 0 || i > j {
		t.Fatalf("relu must be emitted before scaleAndRelu (its own caller):\n%s", res.MSL)
	}
	if k := strings.Index(res.MSL, "kernel void"); k < strings.Index(res.MSL, "float relu(") || k < strings.Index(res.MSL, "float scaleAndRelu(") {
		t.Fatalf("the kernel itself must be emitted after both helpers:\n%s", res.MSL)
	}

	got := runOnMetal(t, res, []float32{-3, -1, 0, 1, 2, 5})
	want := []float32{0, 0, 0, 2, 4, 10} // max(x*2, 0)
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v (bit-for-bit)", got, want)
		}
	}
}

// TestHelperFunctionCalledFromInsideAForLoop proves funcCallees' own walk
// (walkFuncBody/walkFuncStmt) finds a call nested inside a for-loop body,
// not only at a helper's own top level — the one shape TestHelperFunctions-
// EndToEnd's straight-line bodies do not exercise. square is also declared
// AFTER its own caller, sumSquares, in the source: WGSL allows calling a
// function declared later (unlike MSL/C++), the exact reason emitHelperFuncs
// cannot just emit functions in source order.
func TestHelperFunctionCalledFromInsideAForLoop(t *testing.T) {
	const src = `
		fn sumSquares(n: u32) -> f32 {
			var total: f32 = 0.0;
			for (var i: u32 = 0u; i < n; i = i + 1u) {
				total = total + square(f32(i) + 1.0);
			}
			return total;
		}
		fn square(x: f32) -> f32 {
			return x * x;
		}
		@group(0) @binding(0) var<storage, read_write> data: array<f32>;
		@compute @workgroup_size(1)
		fn main(@builtin(global_invocation_id) id: vec3<u32>) {
			data[id.x] = sumSquares(4u);
		}
	`
	res, err := Translate(src, Options{})
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	got := runOnMetal(t, res, []float32{0})
	want := float32(1*1 + 2*2 + 3*3 + 4*4) // 30
	if got[0] != want {
		t.Fatalf("got %v, want %v (bit-for-bit)", got[0], want)
	}
}

// TestForLoopContinueStillRunsUpdate is the decisive proof for the silent
// infinite-loop bug go-wasmcontainers' own host-side loop-budget guard
// caught (never a hang on THEIR side, since that guard forcibly truncates
// — but every one of their padded conv/pool kernels, which skip
// out-of-range input positions with `continue`, came back wrong instead of
// timing out, which is what first pointed at this): forStmt used to lower
// `continue` to a bare MSL `continue`, which jumps straight back to the
// `while(true)` wrapper WITHOUT running update — the loop variable never
// advances past the first `continue`, hanging forever (bounded, on real
// hardware, only by whatever watchdog is watching the GPU — never
// something this test should rely on). Exercises continue AND break
// together, in the same loop, proving the do{}while(false)+flag fix (no
// goto: Metal has no labeled statements) keeps each one's own distinct
// meaning — continue still advances i, break still exits without running
// update one more time.
func TestForLoopContinueStillRunsUpdate(t *testing.T) {
	const src = `
		@group(0) @binding(0) var<storage, read_write> data: array<f32>;
		@compute @workgroup_size(1)
		fn main() {
			var sum: f32 = 0.0;
			for (var i: u32 = 0u; i < 10u; i = i + 1u) {
				if (i == 3u) {
					continue;
				}
				if (i == 7u) {
					break;
				}
				sum = sum + 1.0;
			}
			data[0] = sum;
		}
	`
	res, err := Translate(src, Options{})
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	got := runOnMetal(t, res, []float32{0})
	want := float32(6) // i=0,1,2,4,5,6 increment sum (3 skipped, 7 breaks first)
	if got[0] != want {
		t.Fatalf("got %v, want %v (bit-for-bit)", got[0], want)
	}
}

// TestLoopContinueStillRunsContinuingBlock is forStmt's own decisive proof
// (TestForLoopContinueStillRunsUpdate), but for `loop { … continuing { … }
// }` instead of `for` — go-wasmcontainers' own report flagged this as the
// identical risk ("same issue would hit loop... if continuing is inlined
// at the body end"), confirmed directly: loopStmt had the exact same
// bare-continue-skips-cleanup shape before this fix, for the identical
// reason.
func TestLoopContinueStillRunsContinuingBlock(t *testing.T) {
	const src = `
		@group(0) @binding(0) var<storage, read_write> data: array<f32>;
		@compute @workgroup_size(1)
		fn main() {
			var sum: f32 = 0.0;
			var i: u32 = 0u;
			loop {
				if (i >= 10u) {
					break;
				}
				if (i == 3u) {
					continue;
				}
				sum = sum + 1.0;
				continuing {
					i = i + 1u;
				}
			}
			data[0] = sum;
		}
	`
	res, err := Translate(src, Options{})
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	got := runOnMetal(t, res, []float32{0})
	want := float32(9) // i=0..9 except i=3 increment sum (9 increments)
	if got[0] != want {
		t.Fatalf("got %v, want %v (bit-for-bit)", got[0], want)
	}
}

// TestArrayLengthReturnsRealElementCount is go-wasmcontainers' own exact
// reported shape — their batch/batch-grant kernel's ONLY use of `&`, the
// narrow slice of pointer support confirmed actually needed (general
// `&`/ptr<function,T> can wait until a real kernel needs it): a
// runtime-sized storage array's own real element count via
// `arrayLength(&out)`, gating which threads may write at all.
//
// Dispatched with n=37 — NOT a multiple of the kernel's own
// @workgroup_size(64) — so go-macos/metal's own Dispatch necessarily runs
// extra "overhang" threads with id.x in [37, 63] too (confirmed directly:
// this is how go-macos/metal picks a threadgroup shape for a thread count
// that isn't a whole multiple of it). Every element starts at a sentinel
// bit pattern; a correct arrayLength gates every overhang thread's own
// write out via the kernel's own `if (id.x < arrayLength(&out))`, so ANY
// sentinel surviving in the readback (not just a wrong formula result)
// would mean arrayLength returned something other than the real count.
func TestArrayLengthReturnsRealElementCount(t *testing.T) {
	const src = `
		@group(0) @binding(0) var<storage, read_write> out: array<u32>;
		@compute @workgroup_size(64) fn main(@builtin(global_invocation_id) id: vec3<u32>) {
			if (id.x < arrayLength(&out)) { out[id.x] = id.x * 2654435761u + 7u; }
		}
	`
	res, err := Translate(src, Options{})
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if !strings.Contains(res.MSL, "_sizes.s0") {
		t.Fatalf("generated MSL does not read the bounds-check sizes buffer for arrayLength:\n%s", res.MSL)
	}
	const n = 37
	const sentinel = 0xDEADBEEF
	input := make([]float32, n)
	for i := range input {
		input[i] = math.Float32frombits(sentinel)
	}
	got := runOnMetal(t, res, input)
	for i := range got {
		want := math.Float32frombits(uint32(i)*2654435761 + 7)
		if got[i] != want {
			t.Fatalf("out[%d] (bits %#x) = %v, want %v (bits %#x) — a wrong arrayLength would leave this at the sentinel or race with an overhang thread", i, math.Float32bits(input[i]), got[i], want, math.Float32bits(want))
		}
	}
}

// TestBitcastReinterpretsBitsNotNumericValue is the decisive proof for a
// silent-correctness bug go-wasmcontainers' own oracle caught on v0.45.4:
// bitcast<f32>(u32) was translated as a numeric conversion (float(x)) —
// 1u became 1.0f — instead of a bit reinterpretation (the actual bits of
// 1u, 1.1754944e-38, a subnormal). Confirmed in their own Gemm/MatMul
// kernels, which decode an f32 alpha stored as raw bits in a u32 uniform:
// every result came back ~1e9 too large (0x3F800000, 1.0f's own bits, read
// as an integer). Covers both scalar (f32<->u32) and vector
// (vec4<f32><->vec4<u32>, go-wasmcontainers' own explicit ask) bitcasts in
// one kernel, through a storage array declared array<u32> — the host-side
// harness (runOnMetal) writes/reads raw IEEE-754 bit patterns regardless of
// the WGSL-declared element type, so a correct answer here is only
// possible if the GPU itself reinterpreted bits, never computed one.
func TestBitcastReinterpretsBitsNotNumericValue(t *testing.T) {
	const src = `
		@group(0) @binding(0) var<storage, read_write> data: array<u32>;
		@compute @workgroup_size(1)
		fn main() {
			let a = bitcast<f32>(data[0]);
			let b = bitcast<f32>(data[1]);
			data[0] = bitcast<u32>(a * b);

			let v = bitcast<vec4<f32>>(vec4<u32>(data[2], data[3], data[4], data[5]));
			let w = v * 2.0;
			let wu = bitcast<vec4<u32>>(w);
			data[2] = wu.x;
			data[3] = wu.y;
			data[4] = wu.z;
			data[5] = wu.w;
		}
	`
	res, err := Translate(src, Options{})
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if !strings.Contains(res.MSL, "as_type<") {
		t.Fatalf("generated MSL has no as_type<> bit reinterpretation:\n%s", res.MSL)
	}
	got := runOnMetal(t, res, []float32{3, 2, 1, 2, 3, 4})
	want := []float32{6, 2, 2, 4, 6, 8} // data[0] = 3*2; data[2:6] = [1,2,3,4] * 2
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v (bit-for-bit) — a numeric conversion instead of a bit reinterpretation would give wildly different numbers, not merely imprecise ones", got, want)
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

// TestUniformBudgetAndAtomicFlag is the roadmap's own next confirmed need
// (engine#345 follow-up, go-wasmcontainers' batch/loopbudget.go pattern:
// every looping kernel in their batch path rewrites each loop to count
// against a `var<uniform> budget: u32` and sets a
// `var<storage, read_write> flag: atomic<u32>` via atomicStore before
// breaking). Exercises both new resource kinds together, AND the peer's own
// explicit question — an explicit group 3 layout alongside the guest's own
// group 0 — in one real, dispatched kernel.
func TestUniformBudgetAndAtomicFlag(t *testing.T) {
	const src = `
		@group(0) @binding(0) var<storage, read_write> data: array<f32>;
		@group(3) @binding(0) var<storage, read_write> flag: atomic<u32>;
		@group(3) @binding(1) var<uniform> budget: u32;

		@compute @workgroup_size(1)
		fn main(@builtin(global_invocation_id) id: vec3<u32>) {
			var i: u32 = 0u;
			loop {
				if (i >= budget) {
					atomicStore(&flag, 1u);
					break;
				}
				data[id.x] = data[id.x] + 1.0;
				i = i + 1u;
			}
			let n = atomicLoad(&flag);
			if (n == 1u) {
				data[id.x] = data[id.x] + 1000.0;
			}
		}
	`
	res, err := Translate(src, Options{})
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if len(res.Bindings) != 3 {
		t.Fatalf("Bindings = %+v, want 3 (data, flag, budget)", res.Bindings)
	}
	var dataB, flagB, budgetB *Binding
	for i := range res.Bindings {
		b := &res.Bindings[i]
		switch b.Name {
		case "data":
			dataB = b
		case "flag":
			flagB = b
		case "budget":
			budgetB = b
		}
	}
	if dataB == nil || flagB == nil || budgetB == nil {
		t.Fatalf("missing an expected binding: %+v", res.Bindings)
	}
	if dataB.Kind != BindingStorageArray || dataB.SizesIndex != 0 {
		t.Fatalf("data binding = %+v, want Kind=BindingStorageArray SizesIndex=0", dataB)
	}
	if flagB.Kind != BindingAtomic || flagB.SizesIndex != -1 {
		t.Fatalf("flag binding = %+v, want Kind=BindingAtomic SizesIndex=-1", flagB)
	}
	if budgetB.Kind != BindingUniform || budgetB.SizesIndex != -1 {
		t.Fatalf("budget binding = %+v, want Kind=BindingUniform SizesIndex=-1", budgetB)
	}
	if budgetB.Group != 3 || flagB.Group != 3 || dataB.Group != 0 {
		t.Fatalf("group assignment wrong: data.Group=%d flag.Group=%d budget.Group=%d, want 0,3,3", dataB.Group, flagB.Group, budgetB.Group)
	}
	if res.SizesCount != 1 {
		t.Fatalf("SizesCount = %d, want 1 (only the one storage-array binding needs bounds accounting)", res.SizesCount)
	}

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

	dataBuf, err := dev.NewBuffer(4)
	if err != nil {
		t.Fatal(err)
	}
	defer dataBuf.Close()

	flagBuf, err := dev.NewBuffer(4)
	if err != nil {
		t.Fatal(err)
	}
	defer flagBuf.Close()

	budgetBuf, err := dev.NewBuffer(4)
	if err != nil {
		t.Fatal(err)
	}
	defer budgetBuf.Close()
	binary.LittleEndian.PutUint32(budgetBuf.Bytes(), 5)

	sizesBuf, err := dev.NewBuffer(4)
	if err != nil {
		t.Fatal(err)
	}
	defer sizesBuf.Close()
	binary.LittleEndian.PutUint32(sizesBuf.Bytes(), 1) // data: 1 element

	err = dev.Run(func(e *metal.Encoder) {
		e.Use(pipe)
		e.Buffer(dataB.MetalIndex, dataBuf)
		e.Buffer(flagB.MetalIndex, flagBuf)
		e.Buffer(budgetB.MetalIndex, budgetBuf)
		e.Buffer(res.SizeMetalIndex, sizesBuf)
		e.Dispatch(1)
	})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	gotData := math.Float32frombits(binary.LittleEndian.Uint32(dataBuf.Bytes()))
	gotFlag := binary.LittleEndian.Uint32(flagBuf.Bytes())
	// The loop runs 5 times (budget), then sets flag=1 and breaks; the
	// kernel then reads the flag back and, seeing 1, adds 1000.
	wantData := float32(1005)
	if gotData != wantData || gotFlag != 1 {
		t.Fatalf("got data=%v flag=%v, want data=%v flag=1", gotData, gotFlag, wantData)
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

// TestHelperFunctionThreadsResourceTransitively is the structural proof for
// go-wasmcontainers' own exact reported shape (kernels.go:96's `wm`): a
// helper that reads a module-scope resource DIRECTLY (wm reads prm), called
// from ANOTHER helper that never references prm itself (wmPlusOne only
// calls wm) — wmPlusOne must still receive prm (and _sizes, since prm is a
// storage array) as its own extra parameter, purely to pass it on, or wm's
// own call inside it would reference an identifier nothing declared.
// Checked on the generated MSL text, not dispatched: this is a claim about
// which parameters exist and which names appear in which call, exactly
// what a translation-level test should assert directly, matching this
// file's own TestUniformBudgetAndAtomicFlag. A real, dispatched GPU
// end-to-end of the same shape lives at js/webgpu_test.go's own level
// (TestComputePipelineWithHelperFunctionReadingAResource), where a second
// storage buffer can actually be bound.
func TestHelperFunctionThreadsResourceTransitively(t *testing.T) {
	const src = `
		@group(0) @binding(0) var<storage, read> prm: array<u32>;
		@group(0) @binding(1) var<storage, read_write> data: array<f32>;

		fn wm(f: u32, d: u32) -> u32 {
			return prm[f * 3u + d];
		}
		fn wmPlusOne(f: u32, d: u32) -> u32 {
			return wm(f, d) + 1u;
		}

		@compute @workgroup_size(1)
		fn main(@builtin(global_invocation_id) id: vec3<u32>) {
			data[id.x] = f32(wmPlusOne(id.x, 0u));
		}
	`
	res, err := Translate(src, Options{})
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if !strings.Contains(res.MSL, "wm(uint f, uint d, const device uint* prm, constant _Sizes& _sizes)") {
		t.Fatalf("wm's own signature is missing prm/_sizes:\n%s", res.MSL)
	}
	if !strings.Contains(res.MSL, "wmPlusOne(uint f, uint d, const device uint* prm, constant _Sizes& _sizes)") {
		t.Fatalf("wmPlusOne's own signature is missing prm/_sizes (it never reads prm itself, only wm does — must still receive it to pass on):\n%s", res.MSL)
	}
	if !strings.Contains(res.MSL, "wm(f, d, prm, _sizes)") {
		t.Fatalf("wmPlusOne's own call to wm is missing the forwarded prm/_sizes arguments:\n%s", res.MSL)
	}
	if !strings.Contains(res.MSL, "wmPlusOne(id.x, 0u, prm, _sizes)") {
		t.Fatalf("main's own call to wmPlusOne is missing the forwarded prm/_sizes arguments:\n%s", res.MSL)
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
		"struct-typed uniform buffer resource": `
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
