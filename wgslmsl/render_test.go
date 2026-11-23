// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package wgslmsl

import (
	"strings"
	"testing"
	"unsafe"

	"github.com/go-macos/metal"
)

// renderDevice returns a real Metal device, skipping (not failing) when
// this machine has no usable GPU — the same documented ErrNoDevice/
// ErrUnsupported skip every other acceptance test in this package uses
// (runOnMetal's own doc comment).
func renderDevice(t *testing.T) *metal.Device {
	t.Helper()
	dev, err := metal.Default()
	if err != nil {
		t.Skipf("no usable GPU on this machine: %v", err)
	}
	return dev
}

func putFloat32s(dst []byte, src []float32) {
	for i, v := range src {
		*(*float32)(unsafe.Pointer(&dst[i*4])) = v
	}
}

func putUint32s(dst []byte, src []uint32) {
	for i, v := range src {
		*(*uint32)(unsafe.Pointer(&dst[i*4])) = v
	}
}

// vertexFragmentWGSL is the shape engine#415 M7 names for a render
// pipeline: a vertex entry taking a @location position and the
// instance_index builtin, outputting a struct that mixes @builtin(position)
// with a flat @location varying; a fragment entry taking that struct as its
// own input and writing a single @location(0) u32 output. It deliberately
// exercises every shape TranslateRender claims to support: a @location
// vertex input (grouped into a synthesized [[stage_in]] struct), a builtin
// vertex input, a struct-typed vertex OUTPUT / fragment INPUT (the one
// struct type this package recognizes at all), and a bare fragment output
// riding Metal's own implicit color(0) default.
const vertexFragmentWGSL = `
struct VertexOut {
    @builtin(position) position: vec4<f32>,
    @location(0) @interpolate(flat) value: u32,
}

@vertex
fn vmain(@location(0) pos: vec2<f32>, @builtin(instance_index) iid: u32) -> VertexOut {
    var out: VertexOut;
    out.position = vec4<f32>(pos, 0.0, 1.0);
    out.value = iid;
    return out;
}

@fragment
fn fmain(in: VertexOut) -> @location(0) u32 {
    return in.value;
}
`

// TestTranslateRenderVertexFragmentEndToEnd runs TranslateRender's output
// through a real Metal render pass end to end: a cleared R8Uint target, a
// pipeline built from the vertex-input shape TranslateRender reports back
// (VertexInputs), two triangles sharing four vertices (quadBuffers' own
// shape, go-macos/metal's render_darwin_test.go), one DrawIndexed call with
// a non-zero firstInstance, read back. Every pixel is covered by the quad,
// so a correct result is uniform firstInstance everywhere — which also
// means a wrong [[stage_in]] struct (an attribute at the wrong offset, a
// field that resolved to the wrong buffer) or a wrong struct-output/
// struct-input crossing (fmain reading the wrong field, or the wrong one
// entirely) would leave the clear colour showing through, or read back
// some other value, rather than firstInstance everywhere.
func TestTranslateRenderVertexFragmentEndToEnd(t *testing.T) {
	res, err := TranslateRender(vertexFragmentWGSL, RenderOptions{})
	if err != nil {
		t.Fatalf("TranslateRender: %v", err)
	}
	if len(res.VertexInputs) != 1 {
		t.Fatalf("VertexInputs = %v, want exactly 1", res.VertexInputs)
	}
	vin := res.VertexInputs[0]
	if vin.Location != 0 || vin.NComponents != 2 {
		t.Fatalf("VertexInputs[0] = %+v, want Location 0, NComponents 2", vin)
	}

	d := renderDevice(t)
	defer d.Close()

	combinedMSL := res.VertexMSL + "\n" + res.FragmentMSL
	lib, err := d.Compile(combinedMSL)
	if err != nil {
		t.Fatalf("Metal rejected the generated MSL: %v\n--- vertex ---\n%s\n--- fragment ---\n%s", err, res.VertexMSL, res.FragmentMSL)
	}
	defer lib.Close()

	pipe, err := lib.RenderPipeline(metal.RenderPipelineDescriptor{
		VertexFunction:   res.VertexEntryPointName,
		FragmentFunction: res.FragmentEntryPointName,
		Attributes: []metal.VertexAttribute{
			{Index: 0, Format: metal.VertexFormatFloat32x2, Offset: 0, BufferIndex: 0},
		},
		Layouts:     []metal.VertexLayout{{BufferIndex: 0, Stride: 8}},
		ColorFormat: metal.PixelFormatR8Uint,
	})
	if err != nil {
		t.Fatalf("RenderPipeline: %v", err)
	}
	defer pipe.Close()

	vb, err := d.NewBuffer(4 * 8) // 4 vertices, float2 each
	if err != nil {
		t.Fatal(err)
	}
	defer vb.Close()
	putFloat32s(vb.Bytes(), []float32{-1, -1, 1, -1, -1, 1, 1, 1})

	ib, err := d.NewBuffer(6 * 4) // 2 triangles, uint32 each
	if err != nil {
		t.Fatal(err)
	}
	defer ib.Close()
	putUint32s(ib.Bytes(), []uint32{0, 1, 2, 2, 1, 3})

	const w, h = 4, 4
	tex, err := d.NewTexture(w, h, metal.PixelFormatR8Uint, metal.TextureUsageRenderTarget|metal.TextureUsageShaderRead)
	if err != nil {
		t.Fatal(err)
	}
	defer tex.Close()

	const firstInstance = 7
	err = d.RunRender(metal.RenderPassDescriptor{
		Target: tex,
		Load:   metal.LoadActionClear,
		Store:  metal.StoreActionStore,
		Clear:  [4]float64{5, 5, 5, 5},
	}, func(e *metal.RenderEncoder) {
		e.Use(pipe)
		e.VertexBuffer(0, vb, 0)
		e.DrawIndexed(6, 1, ib, 0, 0, firstInstance)
	})
	if err != nil {
		t.Fatalf("RunRender: %v", err)
	}

	got := make([]byte, w*h)
	if err := tex.GetBytes(got); err != nil {
		t.Fatal(err)
	}
	for i, v := range got {
		if v != firstInstance {
			t.Fatalf("pixel %d = %d, want %d (clear colour 5 would mean the quad did not cover it, any other value means the struct output/input crossing read the wrong thing)", i, v, firstInstance)
		}
	}
}

// textureLoadComputeWGSL is engine#415's own named shape for a texture read
// from a COMPUTE shader (render.go's own doc comment: "A fragment entry
// point may read ONE resource kind beyond uniform/storage... through
// textureLoad only") — exercised here through Translate's @compute path,
// which resolveResources shares with TranslateRender, since a compute
// kernel is the simplest real host-side harness for it (go-macos/metal's
// own Encoder.Texture, not RenderEncoder's fragment-only texture binding).
const textureLoadComputeWGSL = `
@group(0) @binding(0) var tex: texture_2d<u32>;
@group(0) @binding(1) var<storage, read_write> out: array<u32>;

@compute @workgroup_size(1)
fn main(@builtin(global_invocation_id) id: vec3<u32>) {
    let v = textureLoad(tex, vec2<i32>(i32(id.x), i32(id.y)), 0);
    out[id.y * 4u + id.x] = v.r;
}
`

// TestTranslateTextureLoadEndToEnd runs a textureLoad kernel through a real
// Metal compute dispatch: a 4x2 R8Uint texture filled with known values,
// read back through the generated MSL's own tex.read(uint2(...), uint(...))
// call. A wrong coordinate order or a wrong texture/buffer binding index
// would read back something other than the source pixel at (x, y).
func TestTranslateTextureLoadEndToEnd(t *testing.T) {
	res, err := Translate(textureLoadComputeWGSL, Options{})
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	var texBinding *Binding
	for i := range res.Bindings {
		if res.Bindings[i].Kind == BindingTexture {
			texBinding = &res.Bindings[i]
		}
	}
	if texBinding == nil {
		t.Fatalf("no BindingTexture in %+v", res.Bindings)
	}

	d := renderDevice(t)
	defer d.Close()

	lib, err := d.Compile(res.MSL)
	if err != nil {
		t.Fatalf("Metal rejected the generated MSL: %v\n--- MSL ---\n%s", err, res.MSL)
	}
	defer lib.Close()

	pipe, err := lib.Pipeline(res.EntryPointName)
	if err != nil {
		t.Fatalf("Pipeline(%q): %v", res.EntryPointName, err)
	}
	defer pipe.Close()

	const w, h = 4, 2
	tex, err := d.NewTexture(w, h, metal.PixelFormatR8Uint, metal.TextureUsageShaderRead)
	if err != nil {
		t.Fatal(err)
	}
	defer tex.Close()
	in := make([]byte, w*h)
	for i := range in {
		in[i] = byte(40 + i)
	}
	if err := tex.Replace(in); err != nil {
		t.Fatal(err)
	}

	var outBinding *Binding
	for i := range res.Bindings {
		if res.Bindings[i].Kind == BindingStorageArray {
			outBinding = &res.Bindings[i]
		}
	}
	if outBinding == nil {
		t.Fatalf("no BindingStorageArray in %+v", res.Bindings)
	}
	outBuf, err := d.NewBuffer(w * h * 4)
	if err != nil {
		t.Fatal(err)
	}
	defer outBuf.Close()

	var sizesBuf *metal.Buffer
	if res.SizeMetalIndex >= 0 {
		sizesBuf, err = d.NewBuffer(4 * res.SizesCount)
		if err != nil {
			t.Fatal(err)
		}
		defer sizesBuf.Close()
		putUint32s(sizesBuf.Bytes()[outBinding.SizesIndex*4:], []uint32{w * h})
	}

	if err := d.Run(func(e *metal.Encoder) {
		e.Use(pipe)
		e.Texture(texBinding.MetalIndex, tex)
		e.Buffer(outBinding.MetalIndex, outBuf)
		if sizesBuf != nil {
			e.Buffer(res.SizeMetalIndex, sizesBuf)
		}
		e.Dispatch(w, h)
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	for i := 0; i < w*h; i++ {
		got := *(*uint32)(unsafe.Pointer(&outBuf.Bytes()[i*4]))
		if want := uint32(40 + i); got != want {
			t.Fatalf("pixel %d = %d, want %d", i, got, want)
		}
	}
}

// TestTranslateRenderRejectsVertexTexture confirms render.go's own
// documented limit: go-macos/metal's RenderEncoder has no vertex-stage
// texture binding, so a @vertex entry reading a texture_2d<u32> resource is
// a named rejection, not a silently wrong translation.
func TestTranslateRenderRejectsVertexTexture(t *testing.T) {
	const src = `
		@group(0) @binding(0) var tex: texture_2d<u32>;
		@vertex
		fn vmain(@builtin(vertex_index) i: u32) -> @builtin(position) vec4<f32> {
			let v = textureLoad(tex, vec2<i32>(0, 0), 0);
			return vec4<f32>(f32(v.r), 0.0, 0.0, 1.0);
		}
		@fragment
		fn fmain() -> @location(0) u32 { return 0u; }
	`
	if _, err := TranslateRender(src, RenderOptions{}); err == nil {
		t.Fatal("TranslateRender succeeded, want a rejection: vertex stage cannot read a texture")
	}
}

// TestTranslateRenderRejectsNonZeroBareFragmentLocation confirms the other
// honest limit this round's own measurement against the real Metal
// compiler found: a non-struct fragment return has no attribute syntax at
// all beyond Metal's own implicit color(0) default, so @location(1) on a
// bare fragment output is a named rejection, not silently mapped to the
// wrong attachment.
func TestTranslateRenderRejectsNonZeroBareFragmentLocation(t *testing.T) {
	const src = `
		@vertex
		fn vmain(@builtin(vertex_index) i: u32) -> @builtin(position) vec4<f32> {
			return vec4<f32>(0.0, 0.0, 0.0, 1.0);
		}
		@fragment
		fn fmain() -> @location(1) u32 { return 0u; }
	`
	if _, err := TranslateRender(src, RenderOptions{}); err == nil {
		t.Fatal("TranslateRender succeeded, want a rejection: bare fragment output must be @location(0)")
	}
}

// fragmentStub is the simplest valid @fragment entry point, for a test
// whose own focus is entirely on the @vertex side — TranslateRender always
// needs both.
const fragmentStub = `
@fragment
fn fmain() -> @location(0) u32 { return 0u; }
`

// vertexStub is the simplest valid @vertex entry point, the counterpart to
// fragmentStub for a test focused entirely on the @fragment side.
const vertexStub = `
@vertex
fn vmain() -> @builtin(position) vec4<f32> { return vec4<f32>(0.0, 0.0, 0.0, 1.0); }
`

// TestTranslateRenderRejectsParseError confirms TranslateRender's own parse
// step (wgsl.Parse) surfaces a syntactically malformed source as a
// rejection, not a panic.
func TestTranslateRenderRejectsParseError(t *testing.T) {
	// A deleted guard at render.go:117 lets a nil *wgsl.Module reach
	// wgsl.Check below it, which crashes the whole test binary before
	// this test's own assertion ever runs (mutsweep would read that as
	// "caught by panic", luck rather than a test). This local recover
	// converts such a crash into an ordinary failed assertion instead.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("TranslateRender panicked: %v", r)
		}
	}()
	if _, err := TranslateRender("not valid wgsl {{{", RenderOptions{}); err == nil {
		t.Fatal("TranslateRender succeeded, want a parse error")
	}
}

// TestTranslateRenderRejectsCheckError confirms a syntactically valid but
// semantically invalid source (an undefined identifier) is rejected by
// TranslateRender's own wgsl.Check step, distinct from the parse step above.
func TestTranslateRenderRejectsCheckError(t *testing.T) {
	src := `
		@vertex
		fn vmain() -> @builtin(position) vec4<f32> { return undefinedThing; }
	` + fragmentStub
	if _, err := TranslateRender(src, RenderOptions{}); err == nil {
		t.Fatal("TranslateRender succeeded, want a check error (undefined identifier)")
	}
}

// TestTranslateRenderRejectsMissingVertexEntryPoint and
// TestTranslateRenderRejectsMissingFragmentEntryPoint cover findEntryPoint's
// own "module has no @<stage> entry point" case for each stage
// independently — TranslateRender needs both, so a module missing either
// one is its own distinct rejection.
func TestTranslateRenderRejectsMissingVertexEntryPoint(t *testing.T) {
	// A deleted guard at render.go:126 lets a nil vep reach
	// vtr.translateStageEntry below it, crashing the whole test binary
	// before the assertion runs — see TestTranslateRenderRejectsParseError's
	// own recover comment for why this matters.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("TranslateRender panicked: %v", r)
		}
	}()
	if _, err := TranslateRender(fragmentStub, RenderOptions{}); err == nil {
		t.Fatal("TranslateRender succeeded, want a rejection: no @vertex entry point")
	}
}

func TestTranslateRenderRejectsMissingFragmentEntryPoint(t *testing.T) {
	// Same reasoning, for the deleted guard at render.go:130 and fep.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("TranslateRender panicked: %v", r)
		}
	}()
	if _, err := TranslateRender(vertexStub, RenderOptions{}); err == nil {
		t.Fatal("TranslateRender succeeded, want a rejection: no @fragment entry point")
	}
}

// twoVertexEntriesWGSL declares two @vertex entry points and one @fragment
// entry point — the shape findEntryPoint's own explicit-name selection
// needs: RenderOptions.VertexEntryPoint picks one by name, and the wrong
// name (one that exists but as the OTHER stage) is its own distinct
// rejection.
const twoVertexEntriesWGSL = `
@vertex
fn vmainA() -> @builtin(position) vec4<f32> { return vec4<f32>(0.0, 0.0, 0.0, 1.0); }
@vertex
fn vmainB() -> @builtin(position) vec4<f32> { return vec4<f32>(1.0, 0.0, 0.0, 1.0); }
@fragment
fn fmain() -> @location(0) u32 { return 0u; }
`

// TestTranslateRenderSelectsVertexEntryPointByName confirms
// RenderOptions.VertexEntryPoint picks the NAMED entry point specifically
// (not just "some" entry point of the right stage): vmainB, never vmainA,
// which only an exact-name match (not a negated/forced boolean) can tell
// apart.
func TestTranslateRenderSelectsVertexEntryPointByName(t *testing.T) {
	res, err := TranslateRender(twoVertexEntriesWGSL, RenderOptions{VertexEntryPoint: "vmainB"})
	if err != nil {
		t.Fatalf("TranslateRender: %v", err)
	}
	if res.VertexEntryPointName != "vmainB" {
		t.Fatalf("VertexEntryPointName = %q, want %q", res.VertexEntryPointName, "vmainB")
	}
}

// TestTranslateRenderRejectsWrongStageEntryPointName confirms naming an
// entry point of the WRONG stage in RenderOptions.VertexEntryPoint is its
// own rejection ("is a @fragment shader, not @vertex"), distinct from the
// name not existing at all.
func TestTranslateRenderRejectsWrongStageEntryPointName(t *testing.T) {
	_, err := TranslateRender(twoVertexEntriesWGSL, RenderOptions{VertexEntryPoint: "fmain"})
	if err == nil {
		t.Fatal("TranslateRender succeeded, want a rejection: fmain is a @fragment shader, not @vertex")
	}
	if !strings.Contains(err.Error(), "is a @fragment shader") {
		t.Fatalf("error = %v, want it to mention the wrong-stage shape", err)
	}
}

// TestTranslateRenderRejectsFragmentOutputBuiltin confirms outputIOAttr
// rejects a struct-output builtin other than position (here
// @builtin(frag_depth), a genuinely valid WGSL fragment-output builtin) —
// this package's own bounded scope only ever recognizes position.
func TestTranslateRenderRejectsFragmentOutputBuiltin(t *testing.T) {
	// A deleted guard at render.go:232 falls through to `*io.Location`
	// below it, which is nil for a builtin-typed IOVar — a nil-pointer
	// panic that crashes the whole test binary before this test's own
	// assertions run. Same reasoning as the recover in
	// TestTranslateRenderRejectsParseError.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("TranslateRender panicked: %v", r)
		}
	}()
	src := vertexStub + `
		struct FOut {
			@location(0) color: u32,
			@builtin(frag_depth) depth: f32,
		}
		@fragment
		fn fmain() -> FOut {
			var out: FOut;
			out.color = 1u;
			out.depth = 0.5;
			return out;
		}
	`
	_, err := TranslateRender(src, RenderOptions{})
	if err == nil {
		t.Fatal("TranslateRender succeeded, want a rejection: unsupported output builtin frag_depth")
	}
	if !strings.Contains(err.Error(), "output builtin") {
		t.Fatalf("error = %v, want it to mention the output builtin", err)
	}
}

// TestTranslateRenderStructFragmentOutputUsesColorAttribute confirms
// outputIOAttr's fragment branch: a struct-typed fragment output's own
// @location(N) field is emitted as Metal's [[color(N)]] attribute — a shape
// no other existing test exercises (vertexFragmentWGSL's own fragment
// output is a bare, non-struct value).
func TestTranslateRenderStructFragmentOutputUsesColorAttribute(t *testing.T) {
	src := vertexStub + `
		struct FOut {
			@location(0) color: vec4<f32>,
		}
		@fragment
		fn fmain() -> FOut {
			var out: FOut;
			out.color = vec4<f32>(1.0, 0.0, 0.0, 1.0);
			return out;
		}
	`
	res, err := TranslateRender(src, RenderOptions{})
	if err != nil {
		t.Fatalf("TranslateRender: %v", err)
	}
	if !strings.Contains(res.FragmentMSL, "color(0)") {
		t.Fatalf("FragmentMSL = %s, want it to contain color(0)", res.FragmentMSL)
	}
}

// TestTranslateRenderRejectsF16UniformResource confirms the asymmetry this
// round measured directly: go-gfx/wgsl's own Check ACCEPTS f16 as a
// var<uniform> resource type, but this package's own mslType (shared by
// resourceParamDecl, through resourceEntryParams) only ever accepts
// bool/i32/u32/f32 — so an f16 uniform resource used by a @fragment entry
// is rejected downstream of resolveResources, not by it.
func TestTranslateRenderRejectsF16UniformResource(t *testing.T) {
	// A deleted guard at render.go:436 (translateStageEntry's own
	// resolveResources error check) lets translation continue past an
	// unresolved resource: x is never added to tr.bindings/tr.resources,
	// so a later lookup like tr.bindings[tr.resources[x]] panics with an
	// out-of-range index instead of this test's own assertions ever
	// running. Same reasoning as the recover in
	// TestTranslateRenderRejectsParseError.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("TranslateRender panicked: %v", r)
		}
	}()
	src := `
		enable f16;
	` + vertexStub + `
		@group(0) @binding(0) var<uniform> x: f16;
		@fragment
		fn fmain() -> @location(0) u32 { return u32(x); }
	`
	_, err := TranslateRender(src, RenderOptions{})
	if err == nil {
		t.Fatal("TranslateRender succeeded, want a rejection: f16 uniform resource")
	}
	if strings.Contains(err.Error(), "parse:") {
		t.Fatalf("error = %v, want a resource-type rejection, not a parse error", err)
	}
}

// TestTranslateRenderRejectsF16VertexInput confirms the same f16 asymmetry
// on the vertex-input side: Check accepts f16 as a @location vertex input's
// scalar type, but stageEntryParams's own mslTypeTR (mslType underneath)
// rejects it.
func TestTranslateRenderRejectsF16VertexInput(t *testing.T) {
	src := `
		enable f16;
		@vertex
		fn vmain(@location(0) v: f16) -> @builtin(position) vec4<f32> {
			return vec4<f32>(f32(v), 0.0, 0.0, 1.0);
		}
	` + fragmentStub
	if _, err := TranslateRender(src, RenderOptions{}); err == nil {
		t.Fatal("TranslateRender succeeded, want a rejection: f16 vertex input")
	}
}

// TestTranslateRenderRejectsF16FragmentBareOutput confirms the same f16
// asymmetry once more on a bare (non-struct) fragment return type: Check
// accepts f16 as a @location(0) bare output's type, but
// translateStageEntry's own "m, err := mslType(ep.Func.Ret); if err !=
// nil" check (render.go) rejects it. This check runs while retMSL is
// still being determined, before tr.stmtBlock(ep.Func.Decl.Body) ever
// translates the body's own "return 1.0h" literal — which independently
// fails the same way once this check is bypassed (hand-mutation-confirmed:
// neutralizing it still fails the same input, with "literal of scalar
// kind 6" instead of this check's own "scalar type 6 (only
// bool/i32/u32/f32)" via mslScalar). The specific message distinguishes
// them: only this check's own mslType(ep.Func.Ret) call produces
// "only bool/i32/u32/f32" for this input, so asserting it (not just
// err != nil) isolates render.go's own propagation line from the body's
// later, independent literal-translation rejection.
func TestTranslateRenderRejectsF16FragmentBareOutput(t *testing.T) {
	src := `
		enable f16;
	` + vertexStub + `
		@fragment
		fn fmain() -> @location(0) f16 { return 1.0h; }
	`
	_, err := TranslateRender(src, RenderOptions{})
	if err == nil {
		t.Fatal("TranslateRender succeeded, want a rejection: f16 bare fragment output")
	}
	if !strings.Contains(err.Error(), "only bool/i32/u32/f32") {
		t.Fatalf("error = %v, want mslType(ep.Func.Ret)'s own rejection, not a later one", err)
	}
}

// TestTranslateRenderRejectsF16StructOutputField confirms emitIOStruct's
// own "fieldMSL, err := mslType(io.Type); if err != nil" check (render.go)
// is independently reachable, unlike
// TestTranslateRenderRejectsF16FragmentBareOutput's bare-return shape: a
// struct-typed fragment output's field types are all emitted by
// emitIOStruct while translateStageEntry is still building retMSL, before
// tr.stmtBlock(ep.Func.Decl.Body) ever translates a single body statement
// — so an f16 struct field fails here first, with nothing in the body
// (which never assigns the field at all) able to trip any other f16
// rejection ahead of it.
func TestTranslateRenderRejectsF16StructOutputField(t *testing.T) {
	src := `
		enable f16;
	` + vertexStub + `
		struct FOut {
			@location(0) v: f16,
		}
		@fragment
		fn fmain() -> FOut {
			var out: FOut;
			return out;
		}
	`
	_, err := TranslateRender(src, RenderOptions{})
	if err == nil {
		t.Fatal("TranslateRender succeeded, want a rejection: f16 struct output field")
	}
	if !strings.Contains(err.Error(), "only bool/i32/u32/f32") {
		t.Fatalf("error = %v, want the mslScalar f16 rejection", err)
	}
}

// TestTranslateRenderRejectsSwitchInHelperFunction confirms
// translateStageEntry's own "if err := tr.emitHelperFuncs(ep); err != nil"
// propagation (render.go) is independently reachable — unlike
// TestTranslateRenderRejectsMatrixHelperFunctionParam's helper-parameter
// shape (hand-mutation-confirmed redundant with the entry body's own
// matrix-constructor translation), a switch statement is unsupported only
// inside a function BODY (stmt.go's own stmt dispatch), never as part of a
// parameter or return type a caller's own expression translation would
// also have to check — so a helper whose own body uses switch, called
// from a vertex entry whose own body never does, exercises this
// propagation line and nothing else: emitHelperFuncs runs (and fails)
// before the entry's own body is ever translated.
func TestTranslateRenderRejectsSwitchInHelperFunction(t *testing.T) {
	src := `
		fn helper(x: u32) -> f32 {
			var out: f32 = 0.0;
			switch (x) {
				case 0u: { out = 1.0; }
				default: { out = 2.0; }
			}
			return out;
		}
		@vertex
		fn vmain() -> @builtin(position) vec4<f32> {
			let y = helper(0u);
			return vec4<f32>(y, 0.0, 0.0, 1.0);
		}
	` + fragmentStub
	_, err := TranslateRender(src, RenderOptions{})
	if err == nil {
		t.Fatal("TranslateRender succeeded, want a rejection: switch statement in helper function")
	}
	if !strings.Contains(err.Error(), "unsupported: statement") {
		t.Fatalf("error = %v, want stmt's own unsupported-statement rejection", err)
	}
}

// TestTranslateRenderRejectsMatrixHelperFunctionParam confirms a helper
// function called from a @vertex entry is translated through the SAME
// emitHelperFuncs this package's @compute path uses — and a helper
// parameter type outside this package's scalar/vector scope (a matrix,
// valid as an ordinary WGSL function parameter, just not as entry IO) is
// rejected there. It does NOT isolate render.go's own
// "if err := tr.emitHelperFuncs(ep); err != nil" propagation
// (hand-mutation-confirmed: neutralizing that check still fails the same
// input, with the SAME "type mat2x2<f32> (only scalars and vectors)"
// message, now wrapped as "vertex entry \"vmain\": ..." instead): any
// matrix value a helper call needs is itself an expression this
// package's shared mslType gate rejects when translating the entry's own
// body, including when the matrix is an inline constructor passed
// directly as the call argument (no local `let` binding required) — so
// a matrix-typed helper parameter can never reach emitHelperFuncs without
// the entry body independently tripping the same rejection first. This
// propagation line is left untested as a structural invariant, not a gap.
func TestTranslateRenderRejectsMatrixHelperFunctionParam(t *testing.T) {
	src := `
		fn helper(m: mat2x2<f32>) -> f32 { return m[0][0]; }
		@vertex
		fn vmain() -> @builtin(position) vec4<f32> {
			let m = mat2x2<f32>(1.0, 0.0, 0.0, 1.0);
			let y = helper(m);
			return vec4<f32>(y, 0.0, 0.0, 1.0);
		}
	` + fragmentStub
	if _, err := TranslateRender(src, RenderOptions{}); err == nil {
		t.Fatal("TranslateRender succeeded, want a rejection: matrix helper function parameter")
	}
}

// TestTranslateRenderRejectsFixedSizeStorageArray confirms resolveResources'
// own "fixed-size storage array" rejection (it only ever supports a
// runtime-sized one) is reachable from a render-stage entry point too —
// distinct from TestTranslateRenderRejectsRuntimeStorageArray below, which
// is this package's OWN render-specific rejection of a resource
// resolveResources itself accepts just fine.
func TestTranslateRenderRejectsFixedSizeStorageArray(t *testing.T) {
	src := vertexStub + `
		@group(0) @binding(0) var<storage, read> buf: array<f32, 4>;
		@fragment
		fn fmain() -> @location(0) u32 { return u32(buf[0]); }
	`
	if _, err := TranslateRender(src, RenderOptions{}); err == nil {
		t.Fatal("TranslateRender succeeded, want a rejection: fixed-size storage array")
	}
}

// TestTranslateRenderRejectsRuntimeStorageArray confirms
// translateStageEntry's own render-specific rejection: a runtime-sized
// storage array resource resolves fine (resolveResources accepts it, the
// same as the @compute path), but this package's bounds-check machinery
// (Result.SizeMetalIndex) is a @compute-only concept, so render.go rejects
// it outright instead.
func TestTranslateRenderRejectsRuntimeStorageArray(t *testing.T) {
	src := vertexStub + `
		@group(0) @binding(0) var<storage, read> buf: array<f32>;
		@fragment
		fn fmain() -> @location(0) u32 { return u32(buf[0]); }
	`
	_, err := TranslateRender(src, RenderOptions{})
	if err == nil {
		t.Fatal("TranslateRender succeeded, want a rejection: runtime-sized storage array in a render stage")
	}
	if !strings.Contains(err.Error(), "runtime-sized storage array") {
		t.Fatalf("error = %v, want it to mention the runtime-sized storage array", err)
	}
}

// TestTranslateRenderRejectsConstructingAnUnrecognizedStruct confirms
// memberExpr/callExpr's own struct-recognition scope: the ONE struct type
// this package ever recognizes is an entry point's own output struct
// (tr.outputStructType) — a second, unrelated struct declared in the same
// module is still rejected, whether constructed directly or (as here)
// through a field access on that construction, inside the @vertex entry's
// own body (reached through stmtBlock, not through any resource or
// parameter-list path).
func TestTranslateRenderRejectsConstructingAnUnrecognizedStruct(t *testing.T) {
	src := `
		struct Aux { v: f32, w: f32 }
		@vertex
		fn vmain() -> @builtin(position) vec4<f32> {
			let a = Aux(1.0, 2.0).v;
			return vec4<f32>(a, 0.0, 0.0, 1.0);
		}
	` + fragmentStub
	_, err := TranslateRender(src, RenderOptions{})
	if err == nil {
		t.Fatal("TranslateRender succeeded, want a rejection: constructing an unrecognized struct type")
	}
	if !strings.Contains(err.Error(), "constructing struct type") {
		t.Fatalf("error = %v, want it to mention constructing the struct type", err)
	}
}

// textureLoadFragmentStub is the shared @group(0)@binding(0) texture_2d<u32>
// resource and FOut output struct every textureLoad-focused test below
// reuses, paired with a plain vertexStub (none of these need the vertex
// side at all).
const textureLoadFragmentFOut = `struct FOut { @location(0) c: u32 }`

// TestTranslateRenderRejectsTextureLoadPoisonedCoords and
// TestTranslateRenderRejectsTextureLoadPoisonedLevel each poison exactly
// ONE of textureLoad's own coords/level sub-expressions at a time (the
// SAME unrecognized-Aux-struct rejection as the test above, nested inside a
// cast) — never both in the same test, since a single test poisoning both
// could not tell which specific line's error check actually fired: an
// earlier check deleted by a mutant would still be masked by the later one
// independently failing for its own reason.
func TestTranslateRenderRejectsTextureLoadPoisonedCoords(t *testing.T) {
	src := vertexStub + `
		struct Aux { v: f32, w: f32 }
		@group(0) @binding(0) var tex: texture_2d<u32>;
		` + textureLoadFragmentFOut + `
		@fragment
		fn fmain() -> FOut {
			var out: FOut;
			out.c = textureLoad(tex, vec2<i32>(i32(Aux(1.0, 2.0).v), 0), 0).x;
			return out;
		}
	`
	if _, err := TranslateRender(src, RenderOptions{}); err == nil {
		t.Fatal("TranslateRender succeeded, want a rejection: poisoned textureLoad coords")
	}
}

func TestTranslateRenderRejectsTextureLoadPoisonedLevel(t *testing.T) {
	src := vertexStub + `
		struct Aux { v: f32, w: f32 }
		@group(0) @binding(0) var tex: texture_2d<u32>;
		` + textureLoadFragmentFOut + `
		@fragment
		fn fmain() -> FOut {
			var out: FOut;
			out.c = textureLoad(tex, vec2<i32>(0, 0), i32(Aux(1.0, 2.0).v)).x;
			return out;
		}
	`
	if _, err := TranslateRender(src, RenderOptions{}); err == nil {
		t.Fatal("TranslateRender succeeded, want a rejection: poisoned textureLoad level")
	}
}

// TestTranslateRenderRejectsTextureLoadOnLocalAlias confirms a WGSL
// `let t = tex;` local alias of a texture resource is rejected — but NOT,
// as the resourceOf/textureLoadExpr code might suggest, by their own
// "tracked texture resource" check (expr.go:452): emitting the `let`
// statement itself needs t's own MSL type first, and mslType (shared with
// every other scalar/vector-only site in this package) has no texture
// case, so the rejection fires there, before textureLoadExpr is ever
// reached. Confirmed by reading the actual error text this produces
// ("type texture_2d<u32> (only scalars and vectors)"), not by assuming it
// from go-gfx/wgsl's own Check accepting the shape. expr.go:452 itself
// stays unreached by any WGSL this package's Check-gated scope admits: any
// identifier that type-checks as textureLoad's first argument is either
// the module-scope resource var itself (always tracked, via ep.Resources)
// or a local/parameter binding of texture type, which mslType rejects
// earlier, exactly as this test demonstrates.
func TestTranslateRenderRejectsTextureLoadOnLocalAlias(t *testing.T) {
	src := vertexStub + `
		@group(0) @binding(0) var tex: texture_2d<u32>;
		` + textureLoadFragmentFOut + `
		@fragment
		fn fmain() -> FOut {
			let t = tex;
			var out: FOut;
			out.c = textureLoad(t, vec2<i32>(0, 0), 0).x;
			return out;
		}
	`
	_, err := TranslateRender(src, RenderOptions{})
	if err == nil {
		t.Fatal("TranslateRender succeeded, want a rejection: textureLoad on a local texture alias")
	}
	if !strings.Contains(err.Error(), "only scalars and vectors") {
		t.Fatalf("error = %v, want the let-statement's own texture-type rejection", err)
	}
}

// TestTranslateRenderRejectsSamplerResource confirms resolveResources'
// ASHandle case rejects a `sampler` resource outright (declared BEFORE the
// texture it samples, so it is the first ep.Resources entry resolveResources
// reaches): this package's bounded scope never recognizes textureSample or
// any sampler at all, only textureLoad against a texture_2d<u32>.
func TestTranslateRenderRejectsSamplerResource(t *testing.T) {
	// A deleted guard at translator.go:588 (resolveResources's own
	// "is this handle actually a texture?" type assertion) lets `tex`
	// stay nil and falls through to `tex.Kind` right below it — a
	// nil-pointer panic inside resolveResources itself, before this
	// test's own assertion runs. Same reasoning as the recover in
	// TestTranslateRenderRejectsParseError.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("TranslateRender panicked: %v", r)
		}
	}()
	src := vertexStub + `
		@group(0) @binding(0) var samp: sampler;
		@group(0) @binding(1) var tex: texture_2d<f32>;
		struct FOut { @location(0) c: vec4<f32> }
		@fragment
		fn fmain() -> FOut {
			var out: FOut;
			out.c = textureSample(tex, samp, vec2<f32>(0.0, 0.0));
			return out;
		}
	`
	if _, err := TranslateRender(src, RenderOptions{}); err == nil {
		t.Fatal("TranslateRender succeeded, want a rejection: sampler resource")
	}
}

// builtinStructMemberWGSL is engine#447 review bug #3's exact
// reproduction: the vertex entry's own instance_index builtin is a FIELD
// of a struct parameter (VertexIn), accessed in the body via `in.iid` —
// never a bare top-level parameter the way every other render test in
// this file writes it. Before the fix, stageEntryParams registered no
// accessor for a struct-typed builtin field (only for a @location one),
// so memberExpr fell through to the generic struct-member case and
// emitted `in.iid` using the struct parameter's own WGSL identifier
// `in` — never declared as an MSL parameter, since instance_index is
// flattened into its own bare [[instance_id]] parameter instead. Real
// Metal rejected the result outright ("use of undeclared identifier
// 'in'"), which is exactly what TestTranslateRenderBuiltinStructMemberCompiles
// below proves no longer happens.
const builtinStructMemberWGSL = `
struct VertexIn {
    @builtin(instance_index) iid: u32,
    @location(0) pos: vec2<f32>,
}

struct VertexOut {
    @builtin(position) position: vec4<f32>,
    @location(0) @interpolate(flat) value: u32,
}

@vertex
fn vmain(in: VertexIn) -> VertexOut {
    var out: VertexOut;
    out.position = vec4<f32>(in.pos, 0.0, 1.0);
    out.value = in.iid;
    return out;
}

@fragment
fn fmain(in: VertexOut) -> @location(0) u32 {
    return in.value;
}
`

// TestTranslateRenderBuiltinStructMemberCompiles is the direct
// regression test for engine#447 review bug #3: builtinStructMemberWGSL
// must both translate AND compile against a real Metal device — before
// the fix, translation itself succeeded (no WGSL-level error), and only
// the real Metal compiler rejected the generated MSL, so this test must
// run the fixture all the way through TranslateRender -> device.Compile
// to actually exercise the bug, a pure-Go assertion on TranslateRender's
// error return would not have caught it.
func TestTranslateRenderBuiltinStructMemberCompiles(t *testing.T) {
	res, err := TranslateRender(builtinStructMemberWGSL, RenderOptions{})
	if err != nil {
		t.Fatalf("TranslateRender: %v", err)
	}

	d := renderDevice(t)
	defer d.Close()

	combinedMSL := res.VertexMSL + "\n" + res.FragmentMSL
	lib, err := d.Compile(combinedMSL)
	if err != nil {
		t.Fatalf("Metal rejected the generated MSL: %v\n--- vertex ---\n%s\n--- fragment ---\n%s", err, res.VertexMSL, res.FragmentMSL)
	}
	defer lib.Close()
}

// sharedHelperWGSL is engine#447 review bug #4's exact reproduction: a
// plain helper function (scaleUp) called from BOTH the vertex and the
// fragment entry point. Before the fix, TranslateRender built vtr/ftr as
// two fully independent *translator instances with no shared
// "already emitted" state at all (TranslateRender's own doc comment
// now explains why sharedHelpers exists) — each one's own
// emitHelperFuncs emitted scaleUp's full MSL text unconditionally, so
// the combined MSL (every real caller's own compile unit — see
// gpuCreateRenderPipeline, TestTranslateRenderVertexFragmentEndToEnd)
// contained TWO textually identical "float scaleUp(float v) { ... }"
// definitions, which real Metal rejects outright as a redefinition.
const sharedHelperWGSL = `
fn scaleUp(v: f32) -> f32 {
    return v * 2.0;
}

struct VertexOut {
    @builtin(position) position: vec4<f32>,
    @location(0) @interpolate(flat) value: u32,
}

@vertex
fn vmain(@location(0) pos: vec2<f32>) -> VertexOut {
    var out: VertexOut;
    out.position = vec4<f32>(scaleUp(pos.x), scaleUp(pos.y), 0.0, 1.0);
    out.value = 0u;
    return out;
}

@fragment
fn fmain(in: VertexOut) -> @location(0) u32 {
    return u32(scaleUp(1.0));
}
`

// TestTranslateRenderSharedHelperEmittedOnce is the direct regression
// test for engine#447 review bug #4: sharedHelperWGSL's own scaleUp
// helper, called from both stages, must appear in the combined MSL
// exactly once (a textual count, proving the dedup itself happened, not
// merely that Metal tolerated something else) AND the combined MSL must
// still compile against a real Metal device (the actual failure mode
// before the fix: a function redefinition error).
func TestTranslateRenderSharedHelperEmittedOnce(t *testing.T) {
	res, err := TranslateRender(sharedHelperWGSL, RenderOptions{})
	if err != nil {
		t.Fatalf("TranslateRender: %v", err)
	}

	combinedMSL := res.VertexMSL + "\n" + res.FragmentMSL
	if n := strings.Count(combinedMSL, "float scaleUp("); n != 1 {
		t.Fatalf("combined MSL defines scaleUp %d times, want exactly 1:\n%s", n, combinedMSL)
	}

	d := renderDevice(t)
	defer d.Close()

	lib, err := d.Compile(combinedMSL)
	if err != nil {
		t.Fatalf("Metal rejected the generated MSL: %v\n--- combined ---\n%s", err, combinedMSL)
	}
	defer lib.Close()
}
