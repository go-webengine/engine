// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package wgslmsl

import (
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
