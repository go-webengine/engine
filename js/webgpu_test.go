// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package js

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/dop251/goja"
	"github.com/go-webengine/engine/dom"
)

// renderWGSL is engine#415 M7's own minimal vertex+fragment pair: an
// instance-indexed full-screen quad, writing the instance index back as a
// flat u32 fragment output — reused by every render-pipeline/render-pass
// test below so a single known-good shader is translated and compiled
// each time, matching wgslmsl's own
// TestTranslateRenderVertexFragmentEndToEnd shape exactly (same shader
// text, confirmed to translate and compile there).
const renderWGSL = `
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

// renderPipelineSetupJS builds the dev.createShaderModule/createTexture/
// createRenderPipeline JS, shared by every M7 render test below, so a
// single known-good 4x4 r8uint pipeline setup is never retyped with a
// chance of drifting between tests.
func renderPipelineSetupJS() string {
	return fmt.Sprintf(`
		var module = dev.createShaderModule({ code: `+"`%s`"+` });
		var tex = dev.createTexture({
			size: { width: 4, height: 4 },
			format: 'r8uint',
			usage: GPUTextureUsage.RENDER_ATTACHMENT | GPUTextureUsage.COPY_SRC,
		});
		var pipeline = dev.createRenderPipeline({
			layout: 'auto',
			vertex: {
				module: module,
				entryPoint: 'vmain',
				buffers: [{ arrayStride: 8, attributes: [{ shaderLocation: 0, offset: 0, format: 'float32x2' }] }],
			},
			fragment: {
				module: module,
				entryPoint: 'fmain',
				targets: [{ format: 'r8uint' }],
			},
		});
	`, renderWGSL)
}

// runWebGPU is runJS with EnableWebGPU set.
func runWebGPU(t *testing.T, htmlSrc string) (*dom.Node, []string, Result) {
	t.Helper()
	root, err := dom.Parse(htmlSrc)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var logs []string
	res := Run(root, Options{
		PageURL:      testURL,
		UserAgent:    "TestUA/1.0",
		Client:       http.DefaultClient,
		Timeout:      3 * time.Second,
		Log:          func(s string) { logs = append(logs, s) },
		EnableWebGPU: true,
	})
	return root, logs, res
}

// TestWebGPUDisabledByDefault proves Options.EnableWebGPU's own "zero
// behaviour change" contract: with it unset, navigator.gpu and the
// GPUBufferUsage/GPUMapMode/GPUShaderStage globals are not installed at
// all — matching this engine's own established convention for a
// capability granting access to a real host resource
// (CrossOriginIsolated's SharedArrayBuffer, Options.OPFSRoot's real disk).
func TestWebGPUDisabledByDefault(t *testing.T) {
	_, logs, res := runJS(t, page(`
		console.log('hasGPU=' + ('gpu' in navigator));
		console.log('hasUsage=' + (typeof GPUBufferUsage));
		console.log('hasMapMode=' + (typeof GPUMapMode));
		console.log('hasShaderStage=' + (typeof GPUShaderStage));
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	mustHave(t, logs, "hasGPU=false", "hasUsage=undefined", "hasMapMode=undefined", "hasShaderStage=undefined")
}

// TestRequestAdapterNeverRejects proves GPU.requestAdapter()'s own spec
// contract: it ALWAYS resolves, with null when no adapter is available
// (go-macos/metal's own ErrUnsupported on non-darwin, or ErrNoDevice on a
// VM with no usable GPU — ether way, this engine's own CI may run on either
// kind of machine), never with a rejection. When an adapter IS available,
// its own shape (features/limits/info/requestDevice) is also checked here.
func TestRequestAdapterNeverRejects(t *testing.T) {
	root, logs, res := runWebGPU(t, page(`
		navigator.gpu.requestAdapter().then(function(a) {
			if (a === null) {
				document.body.setAttribute('data-adapter', 'null');
				return;
			}
			document.body.setAttribute('data-adapter', 'ok');
			document.body.setAttribute('data-device-name', a.info.device);
			document.body.setAttribute('data-fallback', String(a.info.isFallbackAdapter));
			document.body.setAttribute('data-has-limits', String(typeof a.limits.maxBufferSize === 'number'));
			document.body.setAttribute('data-features-has-x', String(a.features.has('x')));
			document.body.setAttribute('data-features-values', String(Array.from(a.features.values()).length));
			document.body.setAttribute('data-shader-stage-compute', String(GPUShaderStage.COMPUTE));
		}, function(e) {
			document.body.setAttribute('data-adapter', 'REJECTED:' + e);
		});
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	body := dom.Find(root, "body")
	got, _ := body.Attribute("data-adapter")
	if got == "" || strings.HasPrefix(got, "REJECTED:") {
		t.Fatalf("data-adapter = %q, want \"null\" or \"ok\" (requestAdapter must never reject)", got)
	}
	if got == "null" {
		t.Skip("no usable GPU on this machine/platform — requestAdapter() correctly resolved null")
	}
	if v, _ := body.Attribute("data-has-limits"); v != "true" {
		t.Fatalf("data-has-limits = %q, want true", v)
	}
	if v, _ := body.Attribute("data-features-has-x"); v != "false" {
		t.Fatalf("data-features-has-x = %q, want false (no optional features implemented yet)", v)
	}
	// Confirmed directly needed by go-wasmcontainers: go-gfx/webgpu reads
	// adapter.features with .values() then drives the iterator manually —
	// this must not throw "property values is not a function", and since
	// no optional feature is implemented, Array.from(...) over it is
	// correctly empty.
	if v, _ := body.Attribute("data-features-values"); v != "0" {
		t.Fatalf("data-features-values = %q, want 0 (empty, but values() must exist and be iterable)", v)
	}
	if v, _ := body.Attribute("data-shader-stage-compute"); v != "4" {
		t.Fatalf("data-shader-stage-compute = %q, want 4 (GPUShaderStage.COMPUTE, WebGPU spec value)", v)
	}
	if v, _ := body.Attribute("data-fallback"); v != "false" {
		t.Fatalf("data-fallback = %q, want false", v)
	}
	if v, _ := body.Attribute("data-device-name"); v == "" {
		t.Fatal("data-device-name is empty, want the real GPU's name")
	}
}

// TestRequestAdapterForceFallbackResolvesNull is go-wasmcontainers' own
// exact reported gap (TestBatchFallbackRefused, their CI runner only —
// "the browser still offers a hardware adapter: the flag did not force
// SwiftShader"): requestAdapter({forceFallbackAdapter: true}) used to
// ignore its own argument entirely and hand back the real hardware
// adapter regardless. WebGPU §4.4.1 requires forceFallbackAdapter to
// resolve with a SOFTWARE adapter "if one exists, else null" — never the
// hardware one as a substitute. This binding has no software adapter at
// all, so the only spec-correct answer, with or without a real GPU on
// this machine, is null — the one requestAdapter() case this test does
// NOT skip when there is no usable GPU, since null is also exactly what
// a real GPU machine must see.
func TestRequestAdapterForceFallbackResolvesNull(t *testing.T) {
	root, logs, res := runWebGPU(t, page(`
		navigator.gpu.requestAdapter({ forceFallbackAdapter: true }).then(function(a) {
			document.body.setAttribute('data-adapter', a === null ? 'null' : 'ok');
		}, function(e) {
			document.body.setAttribute('data-adapter', 'REJECTED:' + e);
		});
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	body := dom.Find(root, "body")
	if got, _ := body.Attribute("data-adapter"); got != "null" {
		t.Fatalf("data-adapter = %q, want %q (forceFallbackAdapter must resolve null — this binding has no software adapter to hand back instead) logs=%v", got, "null", logs)
	}
}

// TestRequestDeviceIsOneTimePerAdapter proves the spec's own one-time rule:
// a second requestDevice() call on the SAME adapter rejects.
func TestRequestDeviceIsOneTimePerAdapter(t *testing.T) {
	root, logs, res := runWebGPU(t, page(`
		navigator.gpu.requestAdapter().then(function(a) {
			if (a === null) { document.body.setAttribute('data-result', 'skip-no-gpu'); return; }
			return a.requestDevice().then(function() {
				return a.requestDevice();
			}).then(function() {
				document.body.setAttribute('data-result', 'second-call-should-not-resolve');
			}, function(e) {
				document.body.setAttribute('data-result', 'rejected:' + e.name);
			});
		});
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	got, _ := dom.Find(root, "body").Attribute("data-result")
	if got == "skip-no-gpu" {
		t.Skip("no usable GPU on this machine/platform")
	}
	if got != "rejected:OperationError" {
		t.Fatalf("data-result = %q, want %q", got, "rejected:OperationError")
	}
}

// TestGPUBufferLifecycle exercises createBuffer -> mapAsync ->
// getMappedRange (write through the real zero-copy view) -> unmap ->
// queue.writeBuffer -> getMappedRange again (mappedAtCreation) -> destroy,
// against a real device when one is available.
func TestGPUBufferLifecycle(t *testing.T) {
	root, logs, res := runWebGPU(t, page(`
		navigator.gpu.requestAdapter().then(function(a) {
			if (a === null) { document.body.setAttribute('data-result', 'skip-no-gpu'); return; }
			return a.requestDevice();
		}).then(function(dev) {
			if (!dev) return;
			var buf = dev.createBuffer({
				size: 16,
				usage: GPUBufferUsage.MAP_READ | GPUBufferUsage.MAP_WRITE | GPUBufferUsage.COPY_DST,
			});
			document.body.setAttribute('data-size', String(buf.size));
			document.body.setAttribute('data-mapstate-0', buf.mapState);

			return buf.mapAsync(GPUMapMode.WRITE).then(function() {
				document.body.setAttribute('data-mapstate-1', buf.mapState);
				var view = new Uint8Array(buf.getMappedRange());
				for (var i = 0; i < view.length; i++) view[i] = i + 1;
				buf.unmap();
				document.body.setAttribute('data-mapstate-2', buf.mapState);

				// queue.writeBuffer must work on an unmapped buffer.
				dev.queue.writeBuffer(buf, 0, new Uint8Array([100, 101]), 0, 2);

				return buf.mapAsync(GPUMapMode.READ);
			}).then(function() {
				var view2 = new Uint8Array(buf.getMappedRange());
				document.body.setAttribute('data-readback', Array.from(view2).join(','));
				buf.destroy();
				return dev.queue.onSubmittedWorkDone();
			}).then(function() {
				document.body.setAttribute('data-done', 'ok');
			});
		}, function(e) {
			document.body.setAttribute('data-result', 'REJECTED:' + e);
		});
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	body := dom.Find(root, "body")
	if got, _ := body.Attribute("data-result"); got == "skip-no-gpu" {
		t.Skip("no usable GPU on this machine/platform")
	} else if got != "" {
		t.Fatalf("data-result = %q, want empty (no rejection)", got)
	}
	if v, _ := body.Attribute("data-size"); v != "16" {
		t.Fatalf("data-size = %q, want 16", v)
	}
	if v, _ := body.Attribute("data-mapstate-0"); v != "unmapped" {
		t.Fatalf("data-mapstate-0 = %q, want unmapped", v)
	}
	if v, _ := body.Attribute("data-mapstate-1"); v != "mapped" {
		t.Fatalf("data-mapstate-1 = %q, want mapped", v)
	}
	if v, _ := body.Attribute("data-mapstate-2"); v != "unmapped" {
		t.Fatalf("data-mapstate-2 = %q, want unmapped", v)
	}
	want := "100,101,3,4,5,6,7,8,9,10,11,12,13,14,15,16"
	if v, _ := body.Attribute("data-readback"); v != want {
		t.Fatalf("data-readback = %q, want %q (bytes 1..16 written through the first mapped view, then the first two overwritten by queue.writeBuffer)", v, want)
	}
	if v, _ := body.Attribute("data-done"); v != "ok" {
		t.Fatalf("data-done = %q, want ok", v)
	}
}

// TestGPUDeviceDestroyResolvesLost proves device.destroy() resolves the
// device's own `lost` promise with reason "destroyed".
func TestGPUDeviceDestroyResolvesLost(t *testing.T) {
	root, logs, res := runWebGPU(t, page(`
		navigator.gpu.requestAdapter().then(function(a) {
			if (a === null) { document.body.setAttribute('data-result', 'skip-no-gpu'); return; }
			return a.requestDevice();
		}).then(function(dev) {
			if (!dev) return;
			var lostPromise = dev.lost.then(function(info) {
				document.body.setAttribute('data-lost-reason', info.reason);
			});
			dev.destroy();
			return lostPromise;
		});
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	body := dom.Find(root, "body")
	if got, _ := body.Attribute("data-result"); got == "skip-no-gpu" {
		t.Skip("no usable GPU on this machine/platform")
	}
	if v, _ := body.Attribute("data-lost-reason"); v != "destroyed" {
		t.Fatalf("data-lost-reason = %q, want destroyed", v)
	}
}

// TestComputePipelineEndToEnd is the roadmap's own final M5 verification
// step, now through the FULL navigator.gpu JS surface (not just wgslmsl's
// own Go-level API): a real compute shader, created, compiled, bound and
// dispatched entirely via the public WebGPU-shaped API a real page would
// use, output read back and checked bit-for-bit against real Metal
// hardware when one is available.
func TestComputePipelineEndToEnd(t *testing.T) {
	root, logs, res := runWebGPU(t, page(`
		navigator.gpu.requestAdapter().then(function(a) {
			if (a === null) { document.body.setAttribute('data-result', 'skip-no-gpu'); return; }
			return a.requestDevice();
		}).then(function(dev) {
			if (!dev) return;
			var module = dev.createShaderModule({ code: `+"`"+`
				@group(0) @binding(0) var<storage, read_write> data: array<f32>;
				@compute @workgroup_size(4)
				fn main(@builtin(global_invocation_id) id: vec3<u32>) {
					data[id.x] = data[id.x] + 1.0;
				}
			`+"`"+` });
			var pipeline = dev.createComputePipeline({ layout: 'auto', compute: { module: module } });

			var buf = dev.createBuffer({
				size: 16,
				usage: GPUBufferUsage.STORAGE | GPUBufferUsage.MAP_READ | GPUBufferUsage.MAP_WRITE | GPUBufferUsage.COPY_DST,
			});

			return buf.mapAsync(GPUMapMode.WRITE).then(function() {
				new Float32Array(buf.getMappedRange()).set([1, 2, 3, 4]);
				buf.unmap();

				var bindGroup = dev.createBindGroup({
					layout: pipeline.getBindGroupLayout(0),
					entries: [{ binding: 0, resource: { buffer: buf } }],
				});

				var encoder = dev.createCommandEncoder();
				var pass = encoder.beginComputePass();
				pass.setPipeline(pipeline);
				pass.setBindGroup(0, bindGroup);
				pass.dispatchWorkgroups(1);
				pass.end();
				dev.queue.submit([encoder.finish()]);

				return buf.mapAsync(GPUMapMode.READ);
			}).then(function() {
				var out = Array.from(new Float32Array(buf.getMappedRange()));
				document.body.setAttribute('data-out', out.join(','));
				buf.destroy();
			});
		}, function(e) {
			document.body.setAttribute('data-result', 'REJECTED:' + e);
		});
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	body := dom.Find(root, "body")
	if got, _ := body.Attribute("data-result"); got == "skip-no-gpu" {
		t.Skip("no usable GPU on this machine/platform")
	} else if got != "" {
		t.Fatalf("data-result = %q, want empty (no rejection)", got)
	}
	want := "2,3,4,5"
	if got, _ := body.Attribute("data-out"); got != want {
		t.Fatalf("data-out = %q, want %q (bit-for-bit)", got, want)
	}
}

// TestExplicitBindGroupLayoutEndToEnd reproduces go-wasmcontainers' own
// exact confirmed minimum: a 4-invocation storage-buffer compute shader
// with an EXPLICIT (not "auto") layout — createBindGroupLayout +
// createPipelineLayout, confirmed needed because their own batch path
// binds a loop-budget group next to the guest's own groups, which a
// single pipeline's own "auto"-inferred layout cannot express — read back
// through a SEPARATE buffer via copyBufferToBuffer + mapAsync (the
// storage buffer itself is never mapped, matching real usage: a buffer
// used as a compute target and a buffer that is CPU-mappable are
// typically different buffers). Expects [0, 3, 6, 9], bit-for-bit, on
// real Metal hardware when one is available.
func TestExplicitBindGroupLayoutEndToEnd(t *testing.T) {
	root, logs, res := runWebGPU(t, page(`
		navigator.gpu.requestAdapter().then(function(a) {
			if (a === null) { document.body.setAttribute('data-result', 'skip-no-gpu'); return; }
			return a.requestDevice();
		}).then(function(dev) {
			if (!dev) return;
			var module = dev.createShaderModule({ code: `+"`"+`
				@group(0) @binding(0) var<storage, read_write> data: array<u32>;
				@compute @workgroup_size(1)
				fn main(@builtin(global_invocation_id) id: vec3<u32>) {
					data[id.x] = id.x * 3u;
				}
			`+"`"+` });

			var bindGroupLayout = dev.createBindGroupLayout({
				entries: [{ binding: 0, visibility: GPUShaderStage.COMPUTE, buffer: { type: 'storage' } }],
			});
			var pipelineLayout = dev.createPipelineLayout({ bindGroupLayouts: [bindGroupLayout] });
			var pipeline = dev.createComputePipeline({ layout: pipelineLayout, compute: { module: module } });

			var storageBuf = dev.createBuffer({
				size: 16,
				usage: GPUBufferUsage.STORAGE | GPUBufferUsage.COPY_SRC,
			});
			var readbackBuf = dev.createBuffer({
				size: 16,
				usage: GPUBufferUsage.MAP_READ | GPUBufferUsage.COPY_DST,
			});

			var bindGroup = dev.createBindGroup({
				layout: bindGroupLayout,
				entries: [{ binding: 0, resource: { buffer: storageBuf } }],
			});

			var encoder = dev.createCommandEncoder();
			var pass = encoder.beginComputePass();
			pass.setPipeline(pipeline);
			pass.setBindGroup(0, bindGroup);
			pass.dispatchWorkgroups(4);
			pass.end();
			encoder.copyBufferToBuffer(storageBuf, 0, readbackBuf, 0, 16);
			dev.queue.submit([encoder.finish()]);

			return readbackBuf.mapAsync(GPUMapMode.READ).then(function() {
				var out = Array.from(new Uint32Array(readbackBuf.getMappedRange()));
				document.body.setAttribute('data-out', out.join(','));
				readbackBuf.destroy();
				storageBuf.destroy();
			});
		}, function(e) {
			document.body.setAttribute('data-result', 'REJECTED:' + e);
		});
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	body := dom.Find(root, "body")
	if got, _ := body.Attribute("data-result"); got == "skip-no-gpu" {
		t.Skip("no usable GPU on this machine/platform")
	} else if got != "" {
		t.Fatalf("data-result = %q, want empty (no rejection)", got)
	}
	want := "0,3,6,9"
	if got, _ := body.Attribute("data-out"); got != want {
		t.Fatalf("data-out = %q, want %q (bit-for-bit)", got, want)
	}
}

// TestDevicePushPopErrorScopeAndUncapturedError covers
// pushErrorScope/popErrorScope's own push/pop balance (confirmed directly
// needed by go-wasmcontainers: "Object has no member 'pushErrorScope'"
// blocked their own probe item before it ever reached device.lost/
// queue.onSubmittedWorkDone) and that "uncapturederror" can be registered
// without throwing — see pushErrorScope's own doc comment in webgpu.go
// for why this binding's error scopes always resolve null rather than
// capturing a real error (every error here is a synchronous thrown
// exception, not an async device-timeline event).
func TestDevicePushPopErrorScopeAndUncapturedError(t *testing.T) {
	root, logs, res := runWebGPU(t, page(`
		navigator.gpu.requestAdapter().then(function(a) {
			if (a === null) { document.body.setAttribute('data-result', 'skip-no-gpu'); return; }
			return a.requestDevice();
		}).then(function(dev) {
			if (!dev) return;
			dev.addEventListener('uncapturederror', function(e) {});
			dev.pushErrorScope('validation');
			var buf = dev.createBuffer({ size: 4, usage: GPUBufferUsage.STORAGE });
			return dev.popErrorScope().then(function(err) {
				document.body.setAttribute('data-popped', String(err));
				buf.destroy();
			});
		}, function(e) {
			document.body.setAttribute('data-result', 'REJECTED:' + e);
		});
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	body := dom.Find(root, "body")
	if got, _ := body.Attribute("data-result"); got == "skip-no-gpu" {
		t.Skip("no usable GPU on this machine/platform")
	} else if got != "" {
		t.Fatalf("data-result = %q, want empty", got)
	}
	if got, _ := body.Attribute("data-popped"); got != "null" {
		t.Fatalf("data-popped = %q, want %q (no error occurred in the scope)", got, "null")
	}
}

// TestDeviceDestroyReleasesTheNativeDevice is a regression test for the
// REAL root cause behind a native SIGSEGV go-wasmcontainers reported —
// which TWO earlier, independently-real fixes (an objc.AutoreleasePool
// gap in go-macos/metal v0.6.1, a worker-GPU-teardown race in
// stopWorkers) did NOT resolve, because neither was this bug: their own
// retest on both fixes showed the crash was unaffected, deterministic
// (6/6), and happening mid-render — well before any Session.Close or
// worker teardown — ruling out both prior hypotheses directly.
//
// device.destroy() used to only resolve the `lost` promise — a purely
// JS-visible, cosmetic signal nothing else in this file ever checked. It
// never released the underlying *metal.Device (a real MTLDevice +
// MTLCommandQueue, allocated fresh by every requestAdapter/requestDevice
// call per gpuRequestAdapter's own doc comment, never cached or shared).
// A workload that creates and "destroys" many short-lived devices in
// sequence — go-wasmcontainers' own confirmed shape: a fresh graph and
// device per ONNX model, ~123 of them in one page — leaked that many
// live native devices simultaneously, crashing deep inside Apple's own
// Metal/Foundation frameworks once some OS-level resource ceiling was
// crossed (a real native-process crash no Go-level safety net can catch).
//
// This test cannot safely reproduce the exhaustion itself (that would
// crash the test binary, by design — it's a real native memory-safety
// failure, not a graceful error). Instead it proves the MECHANISM the
// fix relies on: destroy() must make the native device genuinely
// unusable, observable here via createBuffer's own EXISTING "closed
// device" error path (go-macos/metal's Device.NewBuffer already refuses
// a device whose id is 0) now actually firing, captured through an
// out-of-memory error scope exactly as any other createBuffer failure
// already is (see gpuCreateBuffer: every NewBuffer error routes through
// panicOutOfMemory). Before the fix, createBuffer succeeded right after
// destroy(), proving nothing was actually released.
func TestDeviceDestroyReleasesTheNativeDevice(t *testing.T) {
	root, logs, res := runWebGPU(t, page(`
		navigator.gpu.requestAdapter().then(function(a) {
			if (a === null) { document.body.setAttribute('data-result', 'skip-no-gpu'); return; }
			return a.requestDevice();
		}).then(function(dev) {
			if (!dev) return;
			dev.destroy();
			dev.pushErrorScope('out-of-memory');
			dev.createBuffer({ size: 4, usage: GPUBufferUsage.STORAGE });
			return dev.popErrorScope().then(function(err) {
				document.body.setAttribute('data-after-destroy', err ? ('captured:' + err.constructor.name) : 'no-error-captured');
			});
		}, function(e) {
			document.body.setAttribute('data-result', 'REJECTED:' + e);
		});
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	body := dom.Find(root, "body")
	if got, _ := body.Attribute("data-result"); got == "skip-no-gpu" {
		t.Skip("no usable GPU on this machine/platform")
	} else if got != "" {
		t.Fatalf("data-result = %q, want empty", got)
	}
	want := "captured:GPUOutOfMemoryError"
	if got, _ := body.Attribute("data-after-destroy"); got != want {
		t.Fatalf("data-after-destroy = %q, want %q — createBuffer succeeded against a device that was supposed to be destroyed, meaning destroy() did not actually release the native device", got, want)
	}
}

// TestCreateComputePipelineBadWGSLCapturedByErrorScope reproduces
// go-wasmcontainers' own exact first confirmed scenario: bad WGSL must
// come back as a captured, positioned GPUValidationError, not a thrown
// exception that kills the guest.
func TestCreateComputePipelineBadWGSLCapturedByErrorScope(t *testing.T) {
	root, logs, res := runWebGPU(t, page(`
		navigator.gpu.requestAdapter().then(function(a) {
			if (a === null) { document.body.setAttribute('data-result', 'skip-no-gpu'); return; }
			return a.requestDevice();
		}).then(function(dev) {
			if (!dev) return;
			var module = dev.createShaderModule({ code: 'this is not valid WGSL at all {{{' });
			dev.pushErrorScope('validation');
			var threw = false;
			try {
				dev.createComputePipeline({ layout: 'auto', compute: { module: module } });
			} catch (e) { threw = true; }
			document.body.setAttribute('data-threw', String(threw));
			return dev.popErrorScope().then(function(err) {
				document.body.setAttribute('data-captured-name', err ? err.constructor.name : 'null');
			});
		});
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	body := dom.Find(root, "body")
	if skip, _ := body.Attribute("data-result"); skip == "skip-no-gpu" {
		t.Skip("no usable GPU on this machine/platform")
	}
	if got, _ := body.Attribute("data-threw"); got != "false" {
		t.Fatalf("data-threw = %q, want %q (bad WGSL must never throw synchronously when a scope is open)", got, "false")
	}
	if got, _ := body.Attribute("data-captured-name"); got != "GPUValidationError" {
		t.Fatalf("data-captured-name = %q, want %q", got, "GPUValidationError")
	}
}

// TestCreateShaderModuleGenuinelyInvalidWGSLCapturedAtCreationTime
// reproduces go-wasmcontainers' own exact reported gap (batch-shaders,
// their last red case after arrayLength support shipped): a module using
// a reserved word (`meta`) as an identifier is genuinely invalid WGSL by
// spec — every real engine refuses it right at createShaderModule, not
// only later when a pipeline first tries to use it. Also checks
// getCompilationInfo() reports the real line/column go-gfx/wgsl's own
// Validate found, not an unpositioned placeholder.
func TestCreateShaderModuleGenuinelyInvalidWGSLCapturedAtCreationTime(t *testing.T) {
	root, logs, res := runWebGPU(t, page(`
		navigator.gpu.requestAdapter().then(function(a) {
			if (a === null) { document.body.setAttribute('data-result', 'skip-no-gpu'); return; }
			return a.requestDevice();
		}).then(function(dev) {
			if (!dev) return;
			dev.pushErrorScope('validation');
			var module = dev.createShaderModule({ code: `+"`"+`
				@group(0) @binding(0) var<storage, read_write> meta: array<f32>;
				@compute @workgroup_size(1)
				fn main() {
					meta[0] = 1.0;
				}
			`+"`"+` });
			return Promise.all([dev.popErrorScope(), module.getCompilationInfo()]).then(function(r) {
				var err = r[0], info = r[1];
				document.body.setAttribute('data-captured-name', err ? err.constructor.name : 'null');
				document.body.setAttribute('data-message-count', String(info.messages.length));
				if (info.messages.length > 0) {
					document.body.setAttribute('data-line', String(info.messages[0].lineNum));
					document.body.setAttribute('data-has-reserved-word', String(info.messages[0].message.indexOf('meta') !== -1));
				}
			});
		});
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	body := dom.Find(root, "body")
	if skip, _ := body.Attribute("data-result"); skip == "skip-no-gpu" {
		t.Skip("no usable GPU on this machine/platform")
	}
	if got, _ := body.Attribute("data-captured-name"); got != "GPUValidationError" {
		t.Fatalf("data-captured-name = %q, want %q (a reserved word must be captured AT createShaderModule, not deferred) logs=%v", got, "GPUValidationError", logs)
	}
	if got, _ := body.Attribute("data-message-count"); got != "1" {
		t.Fatalf("data-message-count = %q, want %q", got, "1")
	}
	if got, _ := body.Attribute("data-line"); got != "2" {
		t.Fatalf("data-line = %q, want %q (the real line go-gfx/wgsl's own Validate found, not an unpositioned 0)", got, "2")
	}
	if got, _ := body.Attribute("data-has-reserved-word"); got != "true" {
		t.Fatalf("data-has-reserved-word = %q, want %q", got, "true")
	}
}

// TestCreateShaderModuleUnsupportedButValidWGSLNotCapturedAtCreationTime
// is the decisive contrast: a switch statement is genuinely VALID WGSL (a
// real browser compiles it fine) that this translator's own deliberately
// bounded backend does not support yet — that is THIS implementation's
// own limitation, never a spec-level shader-creation error, so it must
// NOT be captured by an open error scope at createShaderModule — only the
// category TestCreateShaderModuleGenuinelyInvalidWGSLCapturedAtCreationTime
// covers (genuinely invalid WGSL) is. Reporting this one the same way
// would wrongly tell real page content its own valid WGSL is broken.
func TestCreateShaderModuleUnsupportedButValidWGSLNotCapturedAtCreationTime(t *testing.T) {
	root, logs, res := runWebGPU(t, page(`
		navigator.gpu.requestAdapter().then(function(a) {
			if (a === null) { document.body.setAttribute('data-result', 'skip-no-gpu'); return; }
			return a.requestDevice();
		}).then(function(dev) {
			if (!dev) return;
			dev.pushErrorScope('validation');
			dev.createShaderModule({ code: `+"`"+`
				@group(0) @binding(0) var<storage, read_write> data: array<f32>;
				@compute @workgroup_size(1)
				fn main(@builtin(global_invocation_id) id: vec3<u32>) {
					switch (id.x) {
						case 0u: { data[0] = 1.0; }
						default: { data[0] = 2.0; }
					}
				}
			`+"`"+` });
			return dev.popErrorScope().then(function(err) {
				document.body.setAttribute('data-captured-name', err ? err.constructor.name : 'null');
			});
		});
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	body := dom.Find(root, "body")
	if skip, _ := body.Attribute("data-result"); skip == "skip-no-gpu" {
		t.Skip("no usable GPU on this machine/platform")
	}
	if got, _ := body.Attribute("data-captured-name"); got != "null" {
		t.Fatalf("data-captured-name = %q, want %q (genuinely valid WGSL this translator merely doesn't support yet must NOT be reported as a shader-creation error) logs=%v", got, "null", logs)
	}
}

// TestQueueWriteBufferOutOfRangeCapturedByErrorScope reproduces
// go-wasmcontainers' own exact third confirmed scenario: an out-of-range
// queue.writeBuffer() is GPU content validation, captured by an open
// error scope, never a thrown TypeError.
func TestQueueWriteBufferOutOfRangeCapturedByErrorScope(t *testing.T) {
	root, logs, res := runWebGPU(t, page(`
		navigator.gpu.requestAdapter().then(function(a) {
			if (a === null) { document.body.setAttribute('data-result', 'skip-no-gpu'); return; }
			return a.requestDevice();
		}).then(function(dev) {
			if (!dev) return;
			var buf = dev.createBuffer({ size: 16, usage: GPUBufferUsage.STORAGE | GPUBufferUsage.COPY_DST });
			dev.pushErrorScope('validation');
			var threw = false;
			try {
				dev.queue.writeBuffer(buf, 0, new Uint8Array(32)); // 32 bytes into a 16-byte buffer
			} catch (e) { threw = true; }
			document.body.setAttribute('data-threw', String(threw));
			return dev.popErrorScope().then(function(err) {
				document.body.setAttribute('data-captured-name', err ? err.constructor.name : 'null');
				buf.destroy();
			});
		});
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	body := dom.Find(root, "body")
	if skip, _ := body.Attribute("data-result"); skip == "skip-no-gpu" {
		t.Skip("no usable GPU on this machine/platform")
	}
	if got, _ := body.Attribute("data-threw"); got != "false" {
		t.Fatalf("data-threw = %q, want %q (an out-of-range writeBuffer must never throw synchronously when a scope is open)", got, "false")
	}
	if got, _ := body.Attribute("data-captured-name"); got != "GPUValidationError" {
		t.Fatalf("data-captured-name = %q, want %q", got, "GPUValidationError")
	}
}

// TestPopErrorScopeWithoutPushRejects proves the one real, spec-correct
// validation this binding's own error scopes DO enforce without capturing
// anything: popErrorScope() with no matching pushErrorScope rejects with
// an OperationError, rather than resolving null for a mismatched call.
func TestPopErrorScopeWithoutPushRejects(t *testing.T) {
	root, logs, res := runWebGPU(t, page(`
		navigator.gpu.requestAdapter().then(function(a) {
			if (a === null) { document.body.setAttribute('data-result', 'skip-no-gpu'); return; }
			return a.requestDevice();
		}).then(function(dev) {
			if (!dev) return;
			return dev.popErrorScope().then(function() {
				document.body.setAttribute('data-result', 'should-have-rejected');
			}, function(e) {
				document.body.setAttribute('data-result', 'rejected:' + e.name);
			});
		});
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	got, _ := dom.Find(root, "body").Attribute("data-result")
	if got == "skip-no-gpu" {
		t.Skip("no usable GPU on this machine/platform")
	}
	if got != "rejected:OperationError" {
		t.Fatalf("data-result = %q, want %q", got, "rejected:OperationError")
	}
}

// TestCreateComputePipelineAsyncEndToEnd covers
// createComputePipelineAsync — the SAME translate/compile work as the
// sync form, via an already-settled Promise (this binding has no real
// background compile to await). Both a successful creation and a
// rejection (an unsupported layout, mirroring
// TestComputePipelineRejectsExplicitLayout's own case) are checked
// through the async entry point specifically, not assumed from the sync
// one.
func TestCreateComputePipelineAsyncEndToEnd(t *testing.T) {
	root, logs, res := runWebGPU(t, page(`
		navigator.gpu.requestAdapter().then(function(a) {
			if (a === null) { document.body.setAttribute('data-result', 'skip-no-gpu'); return; }
			return a.requestDevice();
		}).then(function(dev) {
			if (!dev) return;
			var module = dev.createShaderModule({ code: `+"`"+`
				@group(0) @binding(0) var<storage, read_write> data: array<u32>;
				@compute @workgroup_size(1)
				fn main(@builtin(global_invocation_id) id: vec3<u32>) { data[id.x] = 7u; }
			`+"`"+` });
			return dev.createComputePipelineAsync({ layout: 'auto', compute: { module: module } }).then(function(pipeline) {
				document.body.setAttribute('data-async-ok', String(typeof pipeline.getBindGroupLayout === 'function'));
				return dev.createComputePipelineAsync({ layout: {}, compute: { module: module } });
			}).then(function() {
				document.body.setAttribute('data-async-reject', 'should-have-rejected');
			}, function(e) {
				document.body.setAttribute('data-async-reject', 'rejected:' + e.name);
			});
		}, function(e) {
			document.body.setAttribute('data-result', 'REJECTED:' + e);
		});
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	body := dom.Find(root, "body")
	if got, _ := body.Attribute("data-result"); got == "skip-no-gpu" {
		t.Skip("no usable GPU on this machine/platform")
	} else if got != "" {
		t.Fatalf("data-result = %q, want empty", got)
	}
	if got, _ := body.Attribute("data-async-ok"); got != "true" {
		t.Fatalf("data-async-ok = %q, want true", got)
	}
	if got, _ := body.Attribute("data-async-reject"); got != "rejected:TypeError" {
		t.Fatalf("data-async-reject = %q, want %q", got, "rejected:TypeError")
	}
}

// TestComputePipelineRejectsExplicitLayout proves createComputePipeline
// still rejects a `layout` that is neither `"auto"` nor a REAL
// GPUPipelineLayout from createPipelineLayout() — a bare `{}` object here
// (never registered) is exactly the shape a script would pass by mistake.
// Explicit layouts built the real way (TestExplicitBindGroupLayoutEndToEnd)
// are supported as of this round; this test covers the rejection path
// that remains, not the "explicit layouts are unsupported" gap that used
// to be here.
func TestComputePipelineRejectsExplicitLayout(t *testing.T) {
	root, logs, res := runWebGPU(t, page(`
		navigator.gpu.requestAdapter().then(function(a) {
			if (a === null) { document.body.setAttribute('data-result', 'skip-no-gpu'); return; }
			return a.requestDevice();
		}).then(function(dev) {
			if (!dev) return;
			var module = dev.createShaderModule({ code: `+"`"+`
				@group(0) @binding(0) var<storage, read_write> data: array<f32>;
				@compute @workgroup_size(1)
				fn main(@builtin(global_invocation_id) id: vec3<u32>) { data[id.x] = 0.0; }
			`+"`"+` });
			try {
				dev.createComputePipeline({ layout: {}, compute: { module: module } });
				document.body.setAttribute('data-result', 'should-have-thrown');
			} catch (e) {
				document.body.setAttribute('data-result', 'threw:' + e.name);
			}
		});
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	got, _ := dom.Find(root, "body").Attribute("data-result")
	if got == "skip-no-gpu" {
		t.Skip("no usable GPU on this machine/platform")
	}
	if got != "threw:TypeError" {
		t.Fatalf("data-result = %q, want %q", got, "threw:TypeError")
	}
}

// TestComputePipelineRejectsTextureResource is the regression test for
// engine#447 review bug #5: a @compute entry point declaring a
// texture_2d<u32> resource (the one shape wgslmsl's own resolveResources
// legitimately accepts for ANY stage, including compute, directly via
// go-macos/metal — see wgslmsl's own TestTranslateTextureLoadEndToEnd)
// must still be rejected by THIS binding's createComputePipeline: its own
// dispatch path (gpuDispatchWorkgroups) has no texture-binding mechanism
// and would otherwise collide the texture's Metal index — allocated from
// wgslmsl's separate tr.textureCount counter — with a real buffer
// resource's own same-numbered tr.bufferCount index, silently delivering
// one resource's bytes under the other's binding instead of failing.
func TestComputePipelineRejectsTextureResource(t *testing.T) {
	root, logs, res := runWebGPU(t, page(`
		navigator.gpu.requestAdapter().then(function(a) {
			if (a === null) { document.body.setAttribute('data-result', 'skip-no-gpu'); return; }
			return a.requestDevice();
		}).then(function(dev) {
			if (!dev) return;
			var module = dev.createShaderModule({ code: `+"`"+`
				@group(0) @binding(0) var tex: texture_2d<u32>;
				@group(0) @binding(1) var<storage, read_write> out: array<u32>;
				@compute @workgroup_size(1)
				fn main(@builtin(global_invocation_id) id: vec3<u32>) {
					out[id.x] = textureLoad(tex, vec2<i32>(0, 0), 0).x;
				}
			`+"`"+` });
			try {
				dev.createComputePipeline({ layout: 'auto', compute: { module: module } });
				document.body.setAttribute('data-result', 'should-have-thrown');
			} catch (e) {
				document.body.setAttribute('data-result', 'threw:' + e.name);
			}
		});
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	got, _ := dom.Find(root, "body").Attribute("data-result")
	if got == "skip-no-gpu" {
		t.Skip("no usable GPU on this machine/platform")
	}
	if got != "threw:TypeError" {
		t.Fatalf("data-result = %q, want %q", got, "threw:TypeError")
	}
}

// TestDispatchWithoutBindGroupRejected proves a dispatch missing a needed
// bind group fails clearly rather than reading/writing an unbound buffer
// index.
// TestDispatchWithoutBindGroupRejected proves the WebGPU error MODEL
// (§22), not a synchronous throw: a dispatch missing a bind group for a
// declared @group is GPU content validation, which per spec never throws
// — it is captured by an open error scope (or fired as
// "uncapturederror" with none open), and the call itself still returns
// normally. Confirmed directly needed by go-wasmcontainers: their own
// go-gfx/webgpu wraps calls in pushErrorScope("validation")/
// popErrorScope() specifically so a guest's own mistake surfaces as a
// captured, positioned error rather than killing the whole worker — a
// real synchronous throw here would do exactly that.
func TestDispatchWithoutBindGroupRejected(t *testing.T) {
	root, logs, res := runWebGPU(t, page(`
		navigator.gpu.requestAdapter().then(function(a) {
			if (a === null) { document.body.setAttribute('data-result', 'skip-no-gpu'); return; }
			return a.requestDevice();
		}).then(function(dev) {
			if (!dev) return;
			var module = dev.createShaderModule({ code: `+"`"+`
				@group(0) @binding(0) var<storage, read_write> data: array<f32>;
				@compute @workgroup_size(1)
				fn main(@builtin(global_invocation_id) id: vec3<u32>) { data[id.x] = 0.0; }
			`+"`"+` });
			var pipeline = dev.createComputePipeline({ layout: 'auto', compute: { module: module } });
			var encoder = dev.createCommandEncoder();
			var pass = encoder.beginComputePass();
			pass.setPipeline(pipeline);
			dev.pushErrorScope('validation');
			try {
				pass.dispatchWorkgroups(1);
				document.body.setAttribute('data-threw', 'false');
			} catch (e) {
				document.body.setAttribute('data-threw', 'true:' + e.name);
			}
			return dev.popErrorScope().then(function(err) {
				document.body.setAttribute('data-captured-name', err ? err.constructor.name : 'null');
				document.body.setAttribute('data-captured-message', err ? err.message : '');
			});
		});
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	body := dom.Find(root, "body")
	got, _ := body.Attribute("data-threw")
	if got == "" {
		if skip, _ := body.Attribute("data-result"); skip == "skip-no-gpu" {
			t.Skip("no usable GPU on this machine/platform")
		}
	}
	if got != "false" {
		t.Fatalf("data-threw = %q, want %q (a GPU validation error must never throw synchronously)", got, "false")
	}
	if got, _ := body.Attribute("data-captured-name"); got != "GPUValidationError" {
		t.Fatalf("data-captured-name = %q, want %q", got, "GPUValidationError")
	}
	if got, _ := body.Attribute("data-captured-message"); got == "" {
		t.Fatal("data-captured-message is empty, want a real message naming the missing bind group")
	}
}

// TestDispatchErrorFiresUncapturedErrorWithNoScopeOpen is
// TestDispatchWithoutBindGroupRejected's own counterpart with NO error
// scope open: the SAME GPU validation failure must still not throw, and
// must instead fire "uncapturederror" on the device — confirmed by
// go-wasmcontainers' own ask directly ("when no scope is open, fire
// uncapturederror on the device").
func TestDispatchErrorFiresUncapturedErrorWithNoScopeOpen(t *testing.T) {
	root, logs, res := runWebGPU(t, page(`
		navigator.gpu.requestAdapter().then(function(a) {
			if (a === null) { document.body.setAttribute('data-result', 'skip-no-gpu'); return; }
			return a.requestDevice();
		}).then(function(dev) {
			if (!dev) return;
			var module = dev.createShaderModule({ code: `+"`"+`
				@group(0) @binding(0) var<storage, read_write> data: array<f32>;
				@compute @workgroup_size(1)
				fn main(@builtin(global_invocation_id) id: vec3<u32>) { data[id.x] = 0.0; }
			`+"`"+` });
			var pipeline = dev.createComputePipeline({ layout: 'auto', compute: { module: module } });
			var encoder = dev.createCommandEncoder();
			var pass = encoder.beginComputePass();
			pass.setPipeline(pipeline);
			var fired = null;
			dev.addEventListener('uncapturederror', function(e) { fired = e.error; });
			try {
				pass.dispatchWorkgroups(1);
				document.body.setAttribute('data-threw', 'false');
			} catch (e) {
				document.body.setAttribute('data-threw', 'true:' + e.name);
			}
			document.body.setAttribute('data-fired-name', fired ? fired.constructor.name : 'null');
		});
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	body := dom.Find(root, "body")
	if skip, _ := body.Attribute("data-result"); skip == "skip-no-gpu" {
		t.Skip("no usable GPU on this machine/platform")
	}
	if got, _ := body.Attribute("data-threw"); got != "false" {
		t.Fatalf("data-threw = %q, want %q", got, "false")
	}
	if got, _ := body.Attribute("data-fired-name"); got != "GPUValidationError" {
		t.Fatalf("data-fired-name = %q, want %q (uncapturederror should fire with no scope open)", got, "GPUValidationError")
	}
}

// TestGPUMapAsyncRejectsBadRange proves mapAsync's own alignment/range
// validation (OperationError), matching the spec rather than silently
// accepting a misaligned or out-of-bounds request.
func TestGPUMapAsyncRejectsBadRange(t *testing.T) {
	root, logs, res := runWebGPU(t, page(`
		navigator.gpu.requestAdapter().then(function(a) {
			if (a === null) { document.body.setAttribute('data-result', 'skip-no-gpu'); return; }
			return a.requestDevice();
		}).then(function(dev) {
			if (!dev) return;
			var buf = dev.createBuffer({ size: 16, usage: GPUBufferUsage.MAP_READ | GPUBufferUsage.COPY_DST });
			return buf.mapAsync(GPUMapMode.READ, 3, 4).then(function() {
				document.body.setAttribute('data-misaligned', 'should-not-resolve');
			}, function(e) {
				document.body.setAttribute('data-misaligned', e.name);
			});
		});
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	body := dom.Find(root, "body")
	if got, _ := body.Attribute("data-result"); got == "skip-no-gpu" {
		t.Skip("no usable GPU on this machine/platform")
	}
	if v, _ := body.Attribute("data-misaligned"); v != "OperationError" {
		t.Fatalf("data-misaligned = %q, want OperationError", v)
	}
}

// TestComputePipelineWithUniformAtomicAndMultipleGroups directly answers a
// question go-wasmcontainers asked explicitly (engine#345 follow-up,
// batch/loopbudget.go: their rewrite uses an explicit @group(3) layout
// alongside the guest shader's own @group(0)) through the REAL public API —
// layout: "auto" correctly builds a SEPARATE GPUBindGroupLayout per group
// number a kernel actually uses (pipeline.getBindGroupLayout(0) and (3) are
// independent, bound with two separate createBindGroup/setBindGroup calls),
// and a `var<uniform>` budget plus a `var<storage, read_write>
// atomic<u32>` flag — their own loop-budget pattern exactly — both work
// through createBuffer/createBindGroup/dispatchWorkgroups like any other
// resource.
func TestComputePipelineWithUniformAtomicAndMultipleGroups(t *testing.T) {
	root, logs, res := runWebGPU(t, page(`
		navigator.gpu.requestAdapter().then(function(a) {
			if (a === null) { document.body.setAttribute('data-result', 'skip-no-gpu'); return; }
			return a.requestDevice();
		}).then(function(dev) {
			if (!dev) return;
			var module = dev.createShaderModule({ code: `+"`"+`
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
			`+"`"+` });
			var pipeline = dev.createComputePipeline({ layout: 'auto', compute: { module: module } });

			var dataBuf = dev.createBuffer({ size: 4, usage: GPUBufferUsage.STORAGE | GPUBufferUsage.MAP_READ | GPUBufferUsage.MAP_WRITE | GPUBufferUsage.COPY_DST });
			var flagBuf = dev.createBuffer({ size: 4, usage: GPUBufferUsage.STORAGE | GPUBufferUsage.MAP_READ | GPUBufferUsage.COPY_DST });
			var budgetBuf = dev.createBuffer({ size: 4, usage: GPUBufferUsage.UNIFORM | GPUBufferUsage.MAP_WRITE | GPUBufferUsage.COPY_DST });

			return dataBuf.mapAsync(GPUMapMode.WRITE).then(function() {
				new Float32Array(dataBuf.getMappedRange())[0] = 0;
				dataBuf.unmap();
				return budgetBuf.mapAsync(GPUMapMode.WRITE);
			}).then(function() {
				new Uint32Array(budgetBuf.getMappedRange())[0] = 5;
				budgetBuf.unmap();

				var group0 = dev.createBindGroup({
					layout: pipeline.getBindGroupLayout(0),
					entries: [{ binding: 0, resource: { buffer: dataBuf } }],
				});
				var group3 = dev.createBindGroup({
					layout: pipeline.getBindGroupLayout(3),
					entries: [
						{ binding: 0, resource: { buffer: flagBuf } },
						{ binding: 1, resource: { buffer: budgetBuf } },
					],
				});

				var encoder = dev.createCommandEncoder();
				var pass = encoder.beginComputePass();
				pass.setPipeline(pipeline);
				pass.setBindGroup(0, group0);
				pass.setBindGroup(3, group3);
				pass.dispatchWorkgroups(1);
				pass.end();
				dev.queue.submit([encoder.finish()]);

				return Promise.all([dataBuf.mapAsync(GPUMapMode.READ), flagBuf.mapAsync(GPUMapMode.READ)]);
			}).then(function() {
				var data = new Float32Array(dataBuf.getMappedRange())[0];
				var flag = new Uint32Array(flagBuf.getMappedRange())[0];
				document.body.setAttribute('data-data', String(data));
				document.body.setAttribute('data-flag', String(flag));
				dataBuf.destroy();
				flagBuf.destroy();
				budgetBuf.destroy();
			});
		}, function(e) {
			document.body.setAttribute('data-result', 'REJECTED:' + e);
		});
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	body := dom.Find(root, "body")
	if got, _ := body.Attribute("data-result"); got == "skip-no-gpu" {
		t.Skip("no usable GPU on this machine/platform")
	} else if got != "" {
		t.Fatalf("data-result = %q, want empty (no rejection)", got)
	}
	if got, _ := body.Attribute("data-data"); got != "1005" {
		t.Fatalf("data-data = %q, want 1005 (5 loop iterations + 1000 once flag is observed set)", got)
	}
	if got, _ := body.Attribute("data-flag"); got != "1" {
		t.Fatalf("data-flag = %q, want 1", got)
	}
}

// TestComputePipelineWithHelperFunctionReadingAResource is the real,
// dispatched end-to-end for go-wasmcontainers' own exact reported shape
// (kernels.go:96's `wm`): a WGSL helper function (wm) reads a SEPARATE
// module-scope storage buffer (prm) directly — not through its own WGSL
// parameter list — and the entry point calls it. wgslmsl's own structural
// test (TestHelperFunctionThreadsResourceTransitively) already checks the
// generated MSL text directly; this one proves the real value comes back
// right through the full navigator.gpu surface, on real Metal hardware.
func TestComputePipelineWithHelperFunctionReadingAResource(t *testing.T) {
	root, logs, res := runWebGPU(t, page(`
		navigator.gpu.requestAdapter().then(function(a) {
			if (a === null) { document.body.setAttribute('data-result', 'skip-no-gpu'); return; }
			return a.requestDevice();
		}).then(function(dev) {
			if (!dev) return;
			var module = dev.createShaderModule({ code: `+"`"+`
				@group(0) @binding(0) var<storage, read> prm: array<u32>;
				@group(0) @binding(1) var<storage, read_write> data: array<f32>;

				fn wm(f: u32, d: u32) -> u32 {
					return prm[f * 3u + d];
				}

				@compute @workgroup_size(1)
				fn main(@builtin(global_invocation_id) id: vec3<u32>) {
					data[id.x] = f32(wm(id.x, 0u));
				}
			`+"`"+` });
			var pipeline = dev.createComputePipeline({ layout: 'auto', compute: { module: module } });

			var prmBuf = dev.createBuffer({ size: 24, usage: GPUBufferUsage.STORAGE | GPUBufferUsage.MAP_WRITE | GPUBufferUsage.COPY_DST });
			var dataBuf = dev.createBuffer({ size: 8, usage: GPUBufferUsage.STORAGE | GPUBufferUsage.MAP_READ | GPUBufferUsage.COPY_DST });

			return prmBuf.mapAsync(GPUMapMode.WRITE).then(function() {
				new Uint32Array(prmBuf.getMappedRange()).set([10, 20, 30, 40, 50, 60]);
				prmBuf.unmap();

				var bindGroup = dev.createBindGroup({
					layout: pipeline.getBindGroupLayout(0),
					entries: [
						{ binding: 0, resource: { buffer: prmBuf } },
						{ binding: 1, resource: { buffer: dataBuf } },
					],
				});

				var encoder = dev.createCommandEncoder();
				var pass = encoder.beginComputePass();
				pass.setPipeline(pipeline);
				pass.setBindGroup(0, bindGroup);
				pass.dispatchWorkgroups(2);
				pass.end();
				dev.queue.submit([encoder.finish()]);

				return dataBuf.mapAsync(GPUMapMode.READ);
			}).then(function() {
				var out = Array.from(new Float32Array(dataBuf.getMappedRange()));
				document.body.setAttribute('data-out', out.join(','));
				prmBuf.destroy();
				dataBuf.destroy();
			});
		}, function(e) {
			document.body.setAttribute('data-result', 'REJECTED:' + e);
		});
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	body := dom.Find(root, "body")
	if got, _ := body.Attribute("data-result"); got == "skip-no-gpu" {
		t.Skip("no usable GPU on this machine/platform")
	} else if got != "" {
		t.Fatalf("data-result = %q, want empty (no rejection)", got)
	}
	want := "10,40" // prm[0*3+0]=10, prm[1*3+0]=40
	if got, _ := body.Attribute("data-out"); got != want {
		t.Fatalf("data-out = %q, want %q logs=%v", got, want, logs)
	}
}

// TestCreateBindGroupSubRangeHonoursOffsetAndItsOwnLength is the decisive
// positive for go-wasmcontainers' own reported gap (createBindGroup used to
// reject any GPUBufferBinding whose offset/size did not cover the WHOLE
// buffer). The shader writes to a fixed, deliberately out-of-range LOCAL
// index (7) with a single invocation — no concurrent writer ever touches
// the same element, so the result is bit-for-bit deterministic regardless
// of wgslmsl's own out-of-bounds CLAMPING (expr.go's `metal::min(uint(i),
// max(_sizes.sN, 1u) - 1u)`): clamping always lands on this binding's own
// LAST valid local index, never on some other binding's or the whole
// buffer's.
//
// The bind group covers bytes [256, 272) of a 512-byte (128-float) buffer
// — offset 256 (float index 64, the smallest offset that honours
// minStorageBufferOffsetAlignment=256), size 16 (4 floats, local indices
// 0-3). Two things must both be right for the write to land on GLOBAL
// float index 67 (local index 3, this binding's own last valid index):
//   - the offset (256) is actually applied when binding the Metal buffer
//     (go-macos/metal's own Encoder.BufferAt, not always-0 Buffer) — get
//     this wrong and the write lands near global index 7 instead;
//   - the bounds-check sizes buffer carries THIS BINDING's own length (4),
//     not the whole buffer's (128) — get this wrong and local index 7 is
//     never clamped at all, landing on global index 71 instead.
func TestCreateBindGroupSubRangeHonoursOffsetAndItsOwnLength(t *testing.T) {
	root, logs, res := runWebGPU(t, page(`
		navigator.gpu.requestAdapter().then(function(a) {
			if (a === null) { document.body.setAttribute('data-result', 'skip-no-gpu'); return; }
			return a.requestDevice();
		}).then(function(dev) {
			if (!dev) return;
			var module = dev.createShaderModule({ code: `+"`"+`
				@group(0) @binding(0) var<storage, read_write> data: array<f32>;
				@compute @workgroup_size(1)
				fn main() {
					data[7] = 99.0;
				}
			`+"`"+` });
			var pipeline = dev.createComputePipeline({ layout: 'auto', compute: { module: module } });

			var buf = dev.createBuffer({
				size: 512,
				usage: GPUBufferUsage.STORAGE | GPUBufferUsage.MAP_READ | GPUBufferUsage.MAP_WRITE | GPUBufferUsage.COPY_DST,
			});

			return buf.mapAsync(GPUMapMode.WRITE).then(function() {
				new Float32Array(buf.getMappedRange()).fill(-1);
				buf.unmap();

				var bindGroup = dev.createBindGroup({
					layout: pipeline.getBindGroupLayout(0),
					entries: [{ binding: 0, resource: { buffer: buf, offset: 256, size: 16 } }],
				});

				var encoder = dev.createCommandEncoder();
				var pass = encoder.beginComputePass();
				pass.setPipeline(pipeline);
				pass.setBindGroup(0, bindGroup);
				pass.dispatchWorkgroups(1);
				pass.end();
				dev.queue.submit([encoder.finish()]);

				return buf.mapAsync(GPUMapMode.READ);
			}).then(function() {
				var out = Array.from(new Float32Array(buf.getMappedRange()));
				document.body.setAttribute('data-out', out.join(','));
				buf.destroy();
			});
		}, function(e) {
			document.body.setAttribute('data-result', 'REJECTED:' + e);
		});
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	body := dom.Find(root, "body")
	if got, _ := body.Attribute("data-result"); got == "skip-no-gpu" {
		t.Skip("no usable GPU on this machine/platform")
	} else if got != "" {
		t.Fatalf("data-result = %q, want empty (no rejection)", got)
	}
	want := make([]string, 128)
	for i := range want {
		want[i] = "-1"
	}
	want[67] = "99" // offset 256 (float 64) + this binding's own last valid local index (3)
	got, _ := body.Attribute("data-out")
	if got != strings.Join(want, ",") {
		t.Fatalf("data-out = %q, want %q logs=%v (float 67 written, float 71 untouched: wrong offset or wrong per-binding length would move the write)", got, strings.Join(want, ","), logs)
	}
}

// TestCreateBindGroupSubRangeBindsDisjointRegionsOfOneBuffer is the
// realistic shape go-wasmcontainers' own batch/ONNX cases need: several
// logical buffers packed into one real allocation, each bound as its own
// sub-range. Two dispatches, in the SAME compute pass, each over a
// DIFFERENT region of one 512-byte buffer (offsets 0 and 256 — both
// multiples of minStorageBufferOffsetAlignment), each adding a DIFFERENT
// uniform constant — identical writes from both regions would hide a bug
// where one binding's offset silently fell back to 0 and overwrote the
// other's.
func TestCreateBindGroupSubRangeBindsDisjointRegionsOfOneBuffer(t *testing.T) {
	root, logs, res := runWebGPU(t, page(`
		navigator.gpu.requestAdapter().then(function(a) {
			if (a === null) { document.body.setAttribute('data-result', 'skip-no-gpu'); return; }
			return a.requestDevice();
		}).then(function(dev) {
			if (!dev) return;
			var module = dev.createShaderModule({ code: `+"`"+`
				@group(0) @binding(0) var<storage, read_write> data: array<f32>;
				@group(0) @binding(1) var<uniform> k: f32;
				@compute @workgroup_size(1)
				fn main(@builtin(global_invocation_id) id: vec3<u32>) {
					data[id.x] = f32(id.x) + k;
				}
			`+"`"+` });
			var pipeline = dev.createComputePipeline({ layout: 'auto', compute: { module: module } });

			var buf = dev.createBuffer({
				size: 512,
				usage: GPUBufferUsage.STORAGE | GPUBufferUsage.MAP_READ | GPUBufferUsage.MAP_WRITE | GPUBufferUsage.COPY_DST,
			});
			var kBufA = dev.createBuffer({ size: 4, usage: GPUBufferUsage.UNIFORM | GPUBufferUsage.MAP_WRITE | GPUBufferUsage.COPY_DST });
			var kBufB = dev.createBuffer({ size: 4, usage: GPUBufferUsage.UNIFORM | GPUBufferUsage.MAP_WRITE | GPUBufferUsage.COPY_DST });

			return Promise.all([
				buf.mapAsync(GPUMapMode.WRITE).then(function() { new Float32Array(buf.getMappedRange()).fill(-1); buf.unmap(); }),
				kBufA.mapAsync(GPUMapMode.WRITE).then(function() { new Float32Array(kBufA.getMappedRange())[0] = 100; kBufA.unmap(); }),
				kBufB.mapAsync(GPUMapMode.WRITE).then(function() { new Float32Array(kBufB.getMappedRange())[0] = 200; kBufB.unmap(); }),
			]).then(function() {
				var groupA = dev.createBindGroup({
					layout: pipeline.getBindGroupLayout(0),
					entries: [
						{ binding: 0, resource: { buffer: buf, offset: 0, size: 16 } },
						{ binding: 1, resource: { buffer: kBufA } },
					],
				});
				var groupB = dev.createBindGroup({
					layout: pipeline.getBindGroupLayout(0),
					entries: [
						{ binding: 0, resource: { buffer: buf, offset: 256, size: 16 } },
						{ binding: 1, resource: { buffer: kBufB } },
					],
				});

				var encoder = dev.createCommandEncoder();
				var pass = encoder.beginComputePass();
				pass.setPipeline(pipeline);
				pass.setBindGroup(0, groupA);
				pass.dispatchWorkgroups(4);
				pass.setBindGroup(0, groupB);
				pass.dispatchWorkgroups(4);
				pass.end();
				dev.queue.submit([encoder.finish()]);

				return buf.mapAsync(GPUMapMode.READ);
			}).then(function() {
				var out = Array.from(new Float32Array(buf.getMappedRange()));
				document.body.setAttribute('data-out', out.join(','));
				buf.destroy();
				kBufA.destroy();
				kBufB.destroy();
			});
		}, function(e) {
			document.body.setAttribute('data-result', 'REJECTED:' + e);
		});
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	body := dom.Find(root, "body")
	if got, _ := body.Attribute("data-result"); got == "skip-no-gpu" {
		t.Skip("no usable GPU on this machine/platform")
	} else if got != "" {
		t.Fatalf("data-result = %q, want empty (no rejection)", got)
	}
	want := make([]string, 128)
	for i := range want {
		want[i] = "-1"
	}
	want[0], want[1], want[2], want[3] = "100", "101", "102", "103"
	want[64], want[65], want[66], want[67] = "200", "201", "202", "203"
	got, _ := body.Attribute("data-out")
	if got != strings.Join(want, ",") {
		t.Fatalf("data-out = %q, want %q logs=%v (each region written only by its own bind group's own offset and constant, everything else untouched)", got, strings.Join(want, ","), logs)
	}
}

// TestGPUCreateTextureEndToEnd covers GPUDevice.createTexture's own happy
// path: every descriptor field this binding's bounded scope reports back
// through the resulting GPUTexture's own property getters, then destroy().
func TestGPUCreateTextureEndToEnd(t *testing.T) {
	root, logs, res := runWebGPU(t, page(`
		navigator.gpu.requestAdapter().then(function(a) {
			if (a === null) { document.body.setAttribute('data-result', 'skip-no-gpu'); return; }
			return a.requestDevice();
		}).then(function(dev) {
			if (!dev) return;
			var tex = dev.createTexture({
				size: { width: 4, height: 4 },
				format: 'r8uint',
				usage: GPUTextureUsage.RENDER_ATTACHMENT | GPUTextureUsage.COPY_SRC,
			});
			document.body.setAttribute('data-shape', [
				tex.width, tex.height, tex.depthOrArrayLayers, tex.format, tex.dimension, tex.mipLevelCount, tex.sampleCount,
			].join(','));
			tex.destroy();
		});
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	body := dom.Find(root, "body")
	if got, _ := body.Attribute("data-result"); got == "skip-no-gpu" {
		t.Skip("no usable GPU on this machine/platform")
	}
	want := "4,4,1,r8uint,2d,1,1"
	if got, _ := body.Attribute("data-shape"); got != want {
		t.Fatalf("data-shape = %q, want %q", got, want)
	}
}

// TestGPUCreateTextureUnsupportedFormatRejected proves an unsupported
// GPUTextureFormat is a named, synchronous rejection (unsupportedJS),
// never a silently wrong go-macos/metal PixelFormat.
func TestGPUCreateTextureUnsupportedFormatRejected(t *testing.T) {
	root, logs, res := runWebGPU(t, page(`
		navigator.gpu.requestAdapter().then(function(a) {
			if (a === null) { document.body.setAttribute('data-result', 'skip-no-gpu'); return; }
			return a.requestDevice();
		}).then(function(dev) {
			if (!dev) return;
			try {
				dev.createTexture({ size: { width: 4, height: 4 }, format: 'rgba8unorm', usage: GPUTextureUsage.COPY_SRC });
				document.body.setAttribute('data-result', 'should-have-thrown');
			} catch (e) {
				document.body.setAttribute('data-result', 'threw:' + e.name);
			}
		});
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	got, _ := dom.Find(root, "body").Attribute("data-result")
	if got == "skip-no-gpu" {
		t.Skip("no usable GPU on this machine/platform")
	}
	if got != "threw:TypeError" {
		t.Fatalf("data-result = %q, want %q", got, "threw:TypeError")
	}
}

// TestGPUTextureCreateViewEndToEnd covers GPUTexture.createView()'s own
// happy path: a view object is handed back and is itself usable where a
// GPUTextureView is expected (beginRenderPass's own colorAttachments[0]
// .view, checked by the render-pass tests below).
func TestGPUTextureCreateViewEndToEnd(t *testing.T) {
	root, logs, res := runWebGPU(t, page(`
		navigator.gpu.requestAdapter().then(function(a) {
			if (a === null) { document.body.setAttribute('data-result', 'skip-no-gpu'); return; }
			return a.requestDevice();
		}).then(function(dev) {
			if (!dev) return;
			var tex = dev.createTexture({ size: { width: 4, height: 4 }, format: 'r8uint', usage: GPUTextureUsage.RENDER_ATTACHMENT });
			var view = tex.createView();
			document.body.setAttribute('data-view-type', typeof view);
			tex.destroy();
		});
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	body := dom.Find(root, "body")
	if got, _ := body.Attribute("data-result"); got == "skip-no-gpu" {
		t.Skip("no usable GPU on this machine/platform")
	}
	if got, _ := body.Attribute("data-view-type"); got != "object" {
		t.Fatalf("data-view-type = %q, want %q", got, "object")
	}
}

// TestGPUTextureCreateViewUnsupportedDimensionRejected proves
// createView's own bounded scope (no mip chain, no array layers, no
// reinterpretation — see its own doc comment) names a rejection rather
// than silently handing back a view of the whole texture anyway.
func TestGPUTextureCreateViewUnsupportedDimensionRejected(t *testing.T) {
	root, logs, res := runWebGPU(t, page(`
		navigator.gpu.requestAdapter().then(function(a) {
			if (a === null) { document.body.setAttribute('data-result', 'skip-no-gpu'); return; }
			return a.requestDevice();
		}).then(function(dev) {
			if (!dev) return;
			var tex = dev.createTexture({ size: { width: 4, height: 4 }, format: 'r8uint', usage: GPUTextureUsage.RENDER_ATTACHMENT });
			try {
				tex.createView({ dimension: '3d' });
				document.body.setAttribute('data-result', 'should-have-thrown');
			} catch (e) {
				document.body.setAttribute('data-result', 'threw:' + e.name);
			}
			tex.destroy();
		});
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	got, _ := dom.Find(root, "body").Attribute("data-result")
	if got == "skip-no-gpu" {
		t.Skip("no usable GPU on this machine/platform")
	}
	if got != "threw:TypeError" {
		t.Fatalf("data-result = %q, want %q", got, "threw:TypeError")
	}
}

// TestGPURenderPipelineRejectsResourceBinding proves this binding's own
// deliberate render-pipeline scope narrowing (go-macos/metal's own
// RenderEncoder has no fragment-stage buffer binding and no bind-group
// mechanism at all — see gpuCreateRenderPipeline's own doc comment): a
// shader declaring a @group/@binding resource fails pipeline creation.
// Per spec (and exactly like TestCreateComputePipelineBadWGSLCapturedByErrorScope's
// own bad-WGSL case for compute), a pipeline-creation failure is GPU
// content validation — captured by an open error scope, never a
// synchronous throw — so createRenderPipeline rejecting this shader via
// wgslmsl.TranslateRender's own translation error takes the exact same
// path a genuinely bad shader would, confirmed empirically (this shader
// IS valid WGSL; wgslmsl's own render translator just does not support a
// module-scope resource in a render entry point yet).
func TestGPURenderPipelineRejectsResourceBinding(t *testing.T) {
	root, logs, res := runWebGPU(t, page(`
		navigator.gpu.requestAdapter().then(function(a) {
			if (a === null) { document.body.setAttribute('data-result', 'skip-no-gpu'); return; }
			return a.requestDevice();
		}).then(function(dev) {
			if (!dev) return;
			var module = dev.createShaderModule({ code: `+"`"+`
				@group(0) @binding(0) var<storage, read> tint: array<f32>;
				struct VertexOut { @builtin(position) position: vec4<f32> }
				@vertex
				fn vmain(@location(0) pos: vec2<f32>) -> VertexOut {
					var out: VertexOut;
					out.position = vec4<f32>(pos, 0.0, 1.0);
					return out;
				}
				@fragment
				fn fmain(in: VertexOut) -> @location(0) u32 { return u32(tint[0]); }
			`+"`"+` });
			dev.pushErrorScope('validation');
			var threw = false;
			try {
				dev.createRenderPipeline({
					layout: 'auto',
					vertex: { module: module, entryPoint: 'vmain', buffers: [{ arrayStride: 8, attributes: [{ shaderLocation: 0, offset: 0, format: 'float32x2' }] }] },
					fragment: { module: module, entryPoint: 'fmain', targets: [{ format: 'r8uint' }] },
				});
			} catch (e) { threw = true; }
			document.body.setAttribute('data-threw', String(threw));
			return dev.popErrorScope().then(function(err) {
				document.body.setAttribute('data-captured-name', err ? err.constructor.name : 'null');
			});
		});
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	body := dom.Find(root, "body")
	if skip, _ := body.Attribute("data-result"); skip == "skip-no-gpu" {
		t.Skip("no usable GPU on this machine/platform")
	}
	if got, _ := body.Attribute("data-threw"); got != "false" {
		t.Fatalf("data-threw = %q, want %q (a resource-binding render shader must never throw synchronously when a scope is open)", got, "false")
	}
	if got, _ := body.Attribute("data-captured-name"); got != "GPUValidationError" {
		t.Fatalf("data-captured-name = %q, want %q", got, "GPUValidationError")
	}
}

// TestGPURenderPipelineRejectsFixedSizeUniformBinding exercises this
// binding's own EXPLICIT post-translation rejection in
// gpuCreateRenderPipeline (checking res.VertexBindings/FragmentBindings),
// as distinct from TestGPURenderPipelineRejectsResourceBinding's own
// case (a runtime-sized storage array, which wgslmsl.TranslateRender
// itself already refuses to translate at all — see TranslateRender's own
// doc comment). A fixed-size `var<uniform>` struct DOES translate
// successfully (same doc comment: "may read ONE resource kind beyond
// uniform/storage"), so reaching createRenderPipeline with a real,
// resolved binding is what actually exercises this check — go-macos/
// metal's own RenderEncoder still has no bind-group mechanism to honour
// it, hence the named, synchronous rejection (unsupportedJS), not a
// captured validation error.
func TestGPURenderPipelineRejectsFixedSizeUniformBinding(t *testing.T) {
	root, logs, res := runWebGPU(t, page(`
		navigator.gpu.requestAdapter().then(function(a) {
			if (a === null) { document.body.setAttribute('data-result', 'skip-no-gpu'); return; }
			return a.requestDevice();
		}).then(function(dev) {
			if (!dev) return;
			var module = dev.createShaderModule({ code: `+"`"+`
				@group(0) @binding(0) var<uniform> tint: f32;
				struct VertexOut { @builtin(position) position: vec4<f32> }
				@vertex
				fn vmain(@location(0) pos: vec2<f32>) -> VertexOut {
					var out: VertexOut;
					out.position = vec4<f32>(pos, 0.0, 1.0);
					return out;
				}
				@fragment
				fn fmain(in: VertexOut) -> @location(0) u32 { return u32(tint); }
			`+"`"+` });
			try {
				dev.createRenderPipeline({
					layout: 'auto',
					vertex: { module: module, entryPoint: 'vmain', buffers: [{ arrayStride: 8, attributes: [{ shaderLocation: 0, offset: 0, format: 'float32x2' }] }] },
					fragment: { module: module, entryPoint: 'fmain', targets: [{ format: 'r8uint' }] },
				});
				document.body.setAttribute('data-result', 'should-have-thrown');
			} catch (e) {
				document.body.setAttribute('data-result', 'threw:' + e.name);
			}
		});
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	got, _ := dom.Find(root, "body").Attribute("data-result")
	if got == "skip-no-gpu" {
		t.Skip("no usable GPU on this machine/platform")
	}
	if got != "threw:TypeError" {
		t.Fatalf("data-result = %q, want %q", got, "threw:TypeError")
	}
}

// TestGPURenderPassEncoderDrawIndexedEndToEnd is the render-pipeline
// counterpart of TestComputePipelineEndToEnd: a full
// beginRenderPass/setPipeline/setVertexBuffer/setIndexBuffer/drawIndexed/
// end/submit round trip, read back bit-for-bit through
// copyTextureToBuffer + mapAsync — mirroring wgslmsl's own
// TestTranslateRenderVertexFragmentEndToEnd (same shader, same quad, same
// firstInstance-as-pixel-value trick) but driven entirely through this
// package's own JS surface instead of go-macos/metal directly.
func TestGPURenderPassEncoderDrawIndexedEndToEnd(t *testing.T) {
	root, logs, res := runWebGPU(t, page(`
		navigator.gpu.requestAdapter().then(function(a) {
			if (a === null) { document.body.setAttribute('data-result', 'skip-no-gpu'); return; }
			return a.requestDevice();
		}).then(function(dev) {
			if (!dev) return;
	`+renderPipelineSetupJS()+`
			var vb = dev.createBuffer({ size: 32, usage: GPUBufferUsage.VERTEX | GPUBufferUsage.COPY_DST });
			dev.queue.writeBuffer(vb, 0, new Float32Array([-1, -1, 1, -1, -1, 1, 1, 1]));
			var ib = dev.createBuffer({ size: 24, usage: GPUBufferUsage.INDEX | GPUBufferUsage.COPY_DST });
			dev.queue.writeBuffer(ib, 0, new Uint32Array([0, 1, 2, 2, 1, 3]));

			var encoder = dev.createCommandEncoder();
			var pass = encoder.beginRenderPass({
				colorAttachments: [{ view: tex.createView(), loadOp: 'clear', storeOp: 'store', clearValue: { r: 5, g: 5, b: 5, a: 5 } }],
			});
			pass.setPipeline(pipeline);
			pass.setVertexBuffer(0, vb);
			pass.setIndexBuffer(ib, 'uint32');
			pass.drawIndexed(6, 1, 0, 0, 7);
			pass.end();

			var readback = dev.createBuffer({ size: 16, usage: GPUBufferUsage.MAP_READ | GPUBufferUsage.COPY_DST });
			encoder.copyTextureToBuffer({ texture: tex }, { buffer: readback }, { width: 4, height: 4 });
			dev.queue.submit([encoder.finish()]);

			return readback.mapAsync(GPUMapMode.READ).then(function() {
				var out = Array.from(new Uint8Array(readback.getMappedRange()));
				document.body.setAttribute('data-out', out.join(','));
				readback.destroy();
				vb.destroy();
				ib.destroy();
				tex.destroy();
			});
		}, function(e) {
			document.body.setAttribute('data-result', 'REJECTED:' + e);
		});
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	body := dom.Find(root, "body")
	if got, _ := body.Attribute("data-result"); got == "skip-no-gpu" {
		t.Skip("no usable GPU on this machine/platform")
	} else if got != "" {
		t.Fatalf("data-result = %q, want empty (no rejection)", got)
	}
	want := make([]string, 16)
	for i := range want {
		want[i] = "7"
	}
	got, _ := body.Attribute("data-out")
	if got != strings.Join(want, ",") {
		t.Fatalf("data-out = %q, want %q (the quad covers the whole 4x4 target; every pixel must read back firstInstance=7)", got, strings.Join(want, ","))
	}
}

// TestGPURenderPassEncoderDrawEndToEnd is drawIndexed's own non-indexed
// counterpart: the same quad, as two triangles given directly through
// draw() with no index buffer at all.
func TestGPURenderPassEncoderDrawEndToEnd(t *testing.T) {
	root, logs, res := runWebGPU(t, page(`
		navigator.gpu.requestAdapter().then(function(a) {
			if (a === null) { document.body.setAttribute('data-result', 'skip-no-gpu'); return; }
			return a.requestDevice();
		}).then(function(dev) {
			if (!dev) return;
	`+renderPipelineSetupJS()+`
			var vb = dev.createBuffer({ size: 48, usage: GPUBufferUsage.VERTEX | GPUBufferUsage.COPY_DST });
			dev.queue.writeBuffer(vb, 0, new Float32Array([-1, -1, 1, -1, -1, 1, -1, 1, 1, -1, 1, 1]));

			var encoder = dev.createCommandEncoder();
			var pass = encoder.beginRenderPass({
				colorAttachments: [{ view: tex.createView(), loadOp: 'clear', storeOp: 'store', clearValue: { r: 5, g: 5, b: 5, a: 5 } }],
			});
			pass.setPipeline(pipeline);
			pass.setVertexBuffer(0, vb);
			pass.draw(6, 1, 0, 3);
			pass.end();

			var readback = dev.createBuffer({ size: 16, usage: GPUBufferUsage.MAP_READ | GPUBufferUsage.COPY_DST });
			encoder.copyTextureToBuffer({ texture: tex }, { buffer: readback }, { width: 4, height: 4 });
			dev.queue.submit([encoder.finish()]);

			return readback.mapAsync(GPUMapMode.READ).then(function() {
				var out = Array.from(new Uint8Array(readback.getMappedRange()));
				document.body.setAttribute('data-out', out.join(','));
				readback.destroy();
				vb.destroy();
				tex.destroy();
			});
		}, function(e) {
			document.body.setAttribute('data-result', 'REJECTED:' + e);
		});
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	body := dom.Find(root, "body")
	if got, _ := body.Attribute("data-result"); got == "skip-no-gpu" {
		t.Skip("no usable GPU on this machine/platform")
	} else if got != "" {
		t.Fatalf("data-result = %q, want empty (no rejection)", got)
	}
	want := make([]string, 16)
	for i := range want {
		want[i] = "3"
	}
	got, _ := body.Attribute("data-out")
	if got != strings.Join(want, ",") {
		t.Fatalf("data-out = %q, want %q (the quad covers the whole 4x4 target; every pixel must read back firstInstance=3)", got, strings.Join(want, ","))
	}
}

// TestDrawWithoutPipelineCapturedByErrorScope proves draw() with no
// setPipeline call is GPU content validation per spec, never a
// synchronous throw — captured by an open error scope exactly like
// TestQueueWriteBufferOutOfRangeCapturedByErrorScope's own writeBuffer
// case.
func TestDrawWithoutPipelineCapturedByErrorScope(t *testing.T) {
	root, logs, res := runWebGPU(t, page(`
		navigator.gpu.requestAdapter().then(function(a) {
			if (a === null) { document.body.setAttribute('data-result', 'skip-no-gpu'); return; }
			return a.requestDevice();
		}).then(function(dev) {
			if (!dev) return;
			var tex = dev.createTexture({ size: { width: 4, height: 4 }, format: 'r8uint', usage: GPUTextureUsage.RENDER_ATTACHMENT });
			var encoder = dev.createCommandEncoder();
			var pass = encoder.beginRenderPass({
				colorAttachments: [{ view: tex.createView(), loadOp: 'clear', storeOp: 'store', clearValue: { r: 0, g: 0, b: 0, a: 0 } }],
			});
			dev.pushErrorScope('validation');
			var threw = false;
			try {
				pass.draw(3, 1, 0, 0);
			} catch (e) { threw = true; }
			pass.end();
			document.body.setAttribute('data-threw', String(threw));
			return dev.popErrorScope().then(function(err) {
				document.body.setAttribute('data-captured-name', err ? err.constructor.name : 'null');
				tex.destroy();
			});
		});
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	body := dom.Find(root, "body")
	if skip, _ := body.Attribute("data-result"); skip == "skip-no-gpu" {
		t.Skip("no usable GPU on this machine/platform")
	}
	if got, _ := body.Attribute("data-threw"); got != "false" {
		t.Fatalf("data-threw = %q, want %q (draw without a pipeline must never throw synchronously when a scope is open)", got, "false")
	}
	if got, _ := body.Attribute("data-captured-name"); got != "GPUValidationError" {
		t.Fatalf("data-captured-name = %q, want %q", got, "GPUValidationError")
	}
}

// TestGPUDrawRejectsMissingVertexBuffer is the regression test for
// engine#447 review bug #6's "no vertex buffer bound per slot" check:
// renderPipelineSetupJS's own pipeline declares one vertex buffer slot
// (index 0, stride 8), but this test never calls setVertexBuffer at all
// before draw() — GPU content validation (captured by an open error
// scope), exactly like TestDrawWithoutPipelineCapturedByErrorScope's own
// "no pipeline set" case just above.
func TestGPUDrawRejectsMissingVertexBuffer(t *testing.T) {
	root, logs, res := runWebGPU(t, page(`
		navigator.gpu.requestAdapter().then(function(a) {
			if (a === null) { document.body.setAttribute('data-result', 'skip-no-gpu'); return; }
			return a.requestDevice();
		}).then(function(dev) {
			if (!dev) return;
	`+renderPipelineSetupJS()+`
			var encoder = dev.createCommandEncoder();
			var pass = encoder.beginRenderPass({
				colorAttachments: [{ view: tex.createView(), loadOp: 'clear', storeOp: 'store', clearValue: { r: 0, g: 0, b: 0, a: 0 } }],
			});
			pass.setPipeline(pipeline);
			dev.pushErrorScope('validation');
			var threw = false;
			try {
				pass.draw(6, 1, 0, 0);
			} catch (e) { threw = true; }
			pass.end();
			document.body.setAttribute('data-threw', String(threw));
			return dev.popErrorScope().then(function(err) {
				document.body.setAttribute('data-captured-name', err ? err.constructor.name : 'null');
				tex.destroy();
			});
		});
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	body := dom.Find(root, "body")
	if skip, _ := body.Attribute("data-result"); skip == "skip-no-gpu" {
		t.Skip("no usable GPU on this machine/platform")
	}
	if got, _ := body.Attribute("data-threw"); got != "false" {
		t.Fatalf("data-threw = %q, want %q (draw with a slot unbound must never throw synchronously when a scope is open)", got, "false")
	}
	if got, _ := body.Attribute("data-captured-name"); got != "GPUValidationError" {
		t.Fatalf("data-captured-name = %q, want %q", got, "GPUValidationError")
	}
}

// TestGPUDrawRejectsVertexBufferTooSmall is engine#447 review bug #6's
// "count-vs-buffer-size bound" check: a vertex buffer IS bound at slot 0,
// but it holds only 1 vertex (8 bytes, the pipeline's own stride) while
// draw(6, ...) needs 6 — without this check, go-macos/metal's own
// RenderEncoder reads 40 bytes past the buffer's real end.
func TestGPUDrawRejectsVertexBufferTooSmall(t *testing.T) {
	root, logs, res := runWebGPU(t, page(`
		navigator.gpu.requestAdapter().then(function(a) {
			if (a === null) { document.body.setAttribute('data-result', 'skip-no-gpu'); return; }
			return a.requestDevice();
		}).then(function(dev) {
			if (!dev) return;
	`+renderPipelineSetupJS()+`
			var vb = dev.createBuffer({ size: 8, usage: GPUBufferUsage.VERTEX | GPUBufferUsage.COPY_DST });
			var encoder = dev.createCommandEncoder();
			var pass = encoder.beginRenderPass({
				colorAttachments: [{ view: tex.createView(), loadOp: 'clear', storeOp: 'store', clearValue: { r: 0, g: 0, b: 0, a: 0 } }],
			});
			pass.setPipeline(pipeline);
			pass.setVertexBuffer(0, vb);
			dev.pushErrorScope('validation');
			var threw = false;
			try {
				pass.draw(6, 1, 0, 0);
			} catch (e) { threw = true; }
			pass.end();
			document.body.setAttribute('data-threw', String(threw));
			return dev.popErrorScope().then(function(err) {
				document.body.setAttribute('data-captured-name', err ? err.constructor.name : 'null');
				vb.destroy();
				tex.destroy();
			});
		});
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	body := dom.Find(root, "body")
	if skip, _ := body.Attribute("data-result"); skip == "skip-no-gpu" {
		t.Skip("no usable GPU on this machine/platform")
	}
	if got, _ := body.Attribute("data-threw"); got != "false" {
		t.Fatalf("data-threw = %q, want %q (a too-small vertex buffer must never throw synchronously when a scope is open)", got, "false")
	}
	if got, _ := body.Attribute("data-captured-name"); got != "GPUValidationError" {
		t.Fatalf("data-captured-name = %q, want %q", got, "GPUValidationError")
	}
}

// TestGPUDrawRejectsNegativeVertexCount is engine#447 review bug #6's
// "no negative-value check": draw(-1, ...) must be rejected, not passed
// straight through to go-macos/metal as a huge unsigned count.
func TestGPUDrawRejectsNegativeVertexCount(t *testing.T) {
	root, logs, res := runWebGPU(t, page(`
		navigator.gpu.requestAdapter().then(function(a) {
			if (a === null) { document.body.setAttribute('data-result', 'skip-no-gpu'); return; }
			return a.requestDevice();
		}).then(function(dev) {
			if (!dev) return;
	`+renderPipelineSetupJS()+`
			var vb = dev.createBuffer({ size: 48, usage: GPUBufferUsage.VERTEX | GPUBufferUsage.COPY_DST });
			var encoder = dev.createCommandEncoder();
			var pass = encoder.beginRenderPass({
				colorAttachments: [{ view: tex.createView(), loadOp: 'clear', storeOp: 'store', clearValue: { r: 0, g: 0, b: 0, a: 0 } }],
			});
			pass.setPipeline(pipeline);
			pass.setVertexBuffer(0, vb);
			dev.pushErrorScope('validation');
			var threw = false;
			try {
				pass.draw(-1, 1, 0, 0);
			} catch (e) { threw = true; }
			pass.end();
			document.body.setAttribute('data-threw', String(threw));
			return dev.popErrorScope().then(function(err) {
				document.body.setAttribute('data-captured-name', err ? err.constructor.name : 'null');
				vb.destroy();
				tex.destroy();
			});
		});
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	body := dom.Find(root, "body")
	if skip, _ := body.Attribute("data-result"); skip == "skip-no-gpu" {
		t.Skip("no usable GPU on this machine/platform")
	}
	if got, _ := body.Attribute("data-threw"); got != "false" {
		t.Fatalf("data-threw = %q, want %q (a negative count must never throw synchronously when a scope is open)", got, "false")
	}
	if got, _ := body.Attribute("data-captured-name"); got != "GPUValidationError" {
		t.Fatalf("data-captured-name = %q, want %q", got, "GPUValidationError")
	}
}

// TestGPUDrawIndexedRejectsOutOfRangeIndex is engine#447 review bug #6's
// "index range/alignment check": the index buffer's own last index (10)
// points well past the 4-vertex (32-byte) vertex buffer bound at slot 0 —
// without reading the index buffer's own contents at drawIndexed time (as
// gpuValidateIndexedVertexBuffers now does), this would silently read
// vertex attribute data from 48 bytes past the buffer's real end.
func TestGPUDrawIndexedRejectsOutOfRangeIndex(t *testing.T) {
	root, logs, res := runWebGPU(t, page(`
		navigator.gpu.requestAdapter().then(function(a) {
			if (a === null) { document.body.setAttribute('data-result', 'skip-no-gpu'); return; }
			return a.requestDevice();
		}).then(function(dev) {
			if (!dev) return;
	`+renderPipelineSetupJS()+`
			var vb = dev.createBuffer({ size: 32, usage: GPUBufferUsage.VERTEX | GPUBufferUsage.COPY_DST });
			dev.queue.writeBuffer(vb, 0, new Float32Array([-1, -1, 1, -1, -1, 1, 1, 1]));
			var ib = dev.createBuffer({ size: 24, usage: GPUBufferUsage.INDEX | GPUBufferUsage.COPY_DST });
			dev.queue.writeBuffer(ib, 0, new Uint32Array([0, 1, 2, 2, 1, 10]));
			var encoder = dev.createCommandEncoder();
			var pass = encoder.beginRenderPass({
				colorAttachments: [{ view: tex.createView(), loadOp: 'clear', storeOp: 'store', clearValue: { r: 0, g: 0, b: 0, a: 0 } }],
			});
			pass.setPipeline(pipeline);
			pass.setVertexBuffer(0, vb);
			pass.setIndexBuffer(ib, 'uint32');
			dev.pushErrorScope('validation');
			var threw = false;
			try {
				pass.drawIndexed(6, 1, 0, 0, 0);
			} catch (e) { threw = true; }
			pass.end();
			document.body.setAttribute('data-threw', String(threw));
			return dev.popErrorScope().then(function(err) {
				document.body.setAttribute('data-captured-name', err ? err.constructor.name : 'null');
				vb.destroy();
				ib.destroy();
				tex.destroy();
			});
		});
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	body := dom.Find(root, "body")
	if skip, _ := body.Attribute("data-result"); skip == "skip-no-gpu" {
		t.Skip("no usable GPU on this machine/platform")
	}
	if got, _ := body.Attribute("data-threw"); got != "false" {
		t.Fatalf("data-threw = %q, want %q (an out-of-range index must never throw synchronously when a scope is open)", got, "false")
	}
	if got, _ := body.Attribute("data-captured-name"); got != "GPUValidationError" {
		t.Fatalf("data-captured-name = %q, want %q", got, "GPUValidationError")
	}
}

// TestGPURenderPassEndCalledTwiceRejected is engine#447 review bug #7's
// "end() called twice re-runs the pass" defect: without the enc.passOpen
// guard in wrapGPURenderPassEncoder's own "end" handler, a second end()
// call would re-append the SAME gpuRecordedCommand (and whatever draws it
// holds) a second time, so gpuQueueSubmit would execute every draw in the
// pass twice. The second end() must instead throw a synchronous TypeError
// (this is a usage error, not a GPU validation error, so it is never
// routed through an error scope the way bug #6's draw() checks are).
func TestGPURenderPassEndCalledTwiceRejected(t *testing.T) {
	root, logs, res := runWebGPU(t, page(`
		navigator.gpu.requestAdapter().then(function(a) {
			if (a === null) { document.body.setAttribute('data-result', 'skip-no-gpu'); return; }
			return a.requestDevice();
		}).then(function(dev) {
			if (!dev) return;
	`+renderPipelineSetupJS()+`
			var encoder = dev.createCommandEncoder();
			var pass = encoder.beginRenderPass({
				colorAttachments: [{ view: tex.createView(), loadOp: 'clear', storeOp: 'store', clearValue: { r: 0, g: 0, b: 0, a: 0 } }],
			});
			pass.setPipeline(pipeline);
			pass.end();
			try {
				pass.end();
				document.body.setAttribute('data-result', 'should-have-thrown');
			} catch (e) {
				document.body.setAttribute('data-result', 'threw:' + e.name);
			}
			tex.destroy();
		});
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	got, _ := dom.Find(root, "body").Attribute("data-result")
	if got == "skip-no-gpu" {
		t.Skip("no usable GPU on this machine/platform")
	}
	if got != "threw:TypeError" {
		t.Fatalf("data-result = %q, want %q (end() called a second time on an already-ended render pass must throw synchronously, not silently re-run it)", got, "threw:TypeError")
	}
}

// TestGPUDrawAfterRenderPassEndRejected is engine#447 review bug #7's
// "draw after end() still executes" defect: draw() records into
// state.draws directly, so without the enc.passOpen guard in "draw"'s own
// handler, a call made after end() has already flushed state into
// enc.commands would still append to that (by-now-submitted) state,
// growing a render pass gpuQueueSubmit already believes is fully
// recorded.
func TestGPUDrawAfterRenderPassEndRejected(t *testing.T) {
	root, logs, res := runWebGPU(t, page(`
		navigator.gpu.requestAdapter().then(function(a) {
			if (a === null) { document.body.setAttribute('data-result', 'skip-no-gpu'); return; }
			return a.requestDevice();
		}).then(function(dev) {
			if (!dev) return;
	`+renderPipelineSetupJS()+`
			var vb = dev.createBuffer({ size: 48, usage: GPUBufferUsage.VERTEX | GPUBufferUsage.COPY_DST });
			var encoder = dev.createCommandEncoder();
			var pass = encoder.beginRenderPass({
				colorAttachments: [{ view: tex.createView(), loadOp: 'clear', storeOp: 'store', clearValue: { r: 0, g: 0, b: 0, a: 0 } }],
			});
			pass.setPipeline(pipeline);
			pass.setVertexBuffer(0, vb);
			pass.end();
			try {
				pass.draw(6, 1, 0, 0);
				document.body.setAttribute('data-result', 'should-have-thrown');
			} catch (e) {
				document.body.setAttribute('data-result', 'threw:' + e.name);
			}
			vb.destroy();
			tex.destroy();
		});
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	got, _ := dom.Find(root, "body").Attribute("data-result")
	if got == "skip-no-gpu" {
		t.Skip("no usable GPU on this machine/platform")
	}
	if got != "threw:TypeError" {
		t.Fatalf("data-result = %q, want %q (draw() after end() must throw synchronously, not silently record into an already-submitted pass)", got, "threw:TypeError")
	}
}

// TestGPUCommandEncoderFinishWithOpenRenderPassRejected is engine#447
// review bug #7's "a pass begun but never end()ed is silently dropped"
// defect: finish() must reject when a render (or compute) pass is still
// open, since every draw recorded against it is sitting unflushed in the
// render pass's own state.draws and would otherwise be lost the moment
// finish() hands back a command buffer with no reference to it at all.
func TestGPUCommandEncoderFinishWithOpenRenderPassRejected(t *testing.T) {
	root, logs, res := runWebGPU(t, page(`
		navigator.gpu.requestAdapter().then(function(a) {
			if (a === null) { document.body.setAttribute('data-result', 'skip-no-gpu'); return; }
			return a.requestDevice();
		}).then(function(dev) {
			if (!dev) return;
	`+renderPipelineSetupJS()+`
			var encoder = dev.createCommandEncoder();
			var pass = encoder.beginRenderPass({
				colorAttachments: [{ view: tex.createView(), loadOp: 'clear', storeOp: 'store', clearValue: { r: 0, g: 0, b: 0, a: 0 } }],
			});
			pass.setPipeline(pipeline);
			try {
				encoder.finish();
				document.body.setAttribute('data-result', 'should-have-thrown');
			} catch (e) {
				document.body.setAttribute('data-result', 'threw:' + e.name);
			}
			tex.destroy();
		});
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	got, _ := dom.Find(root, "body").Attribute("data-result")
	if got == "skip-no-gpu" {
		t.Skip("no usable GPU on this machine/platform")
	}
	if got != "threw:TypeError" {
		t.Fatalf("data-result = %q, want %q (finish() with a render pass still open — no end() call — must throw synchronously, not silently drop the pass)", got, "threw:TypeError")
	}
}

// TestGPUComputePassDispatchAfterEndRejected is
// TestGPUDrawAfterRenderPassEndRejected's own compute-pass counterpart:
// dispatchWorkgroups records directly into enc.commands, so a call after
// end() would still run against an encoder whose pass has already been
// handed to gpuQueueSubmit's own command list.
func TestGPUComputePassDispatchAfterEndRejected(t *testing.T) {
	root, logs, res := runWebGPU(t, page(`
		navigator.gpu.requestAdapter().then(function(a) {
			if (a === null) { document.body.setAttribute('data-result', 'skip-no-gpu'); return; }
			return a.requestDevice();
		}).then(function(dev) {
			if (!dev) return;
			var module = dev.createShaderModule({ code: `+"`"+`
				@group(0) @binding(0) var<storage, read_write> data: array<f32>;
				@compute @workgroup_size(1)
				fn main(@builtin(global_invocation_id) id: vec3<u32>) { data[id.x] = 0.0; }
			`+"`"+` });
			var pipeline = dev.createComputePipeline({ layout: 'auto', compute: { module: module } });
			var encoder = dev.createCommandEncoder();
			var pass = encoder.beginComputePass();
			pass.setPipeline(pipeline);
			pass.end();
			try {
				pass.dispatchWorkgroups(1);
				document.body.setAttribute('data-result', 'should-have-thrown');
			} catch (e) {
				document.body.setAttribute('data-result', 'threw:' + e.name);
			}
		});
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	got, _ := dom.Find(root, "body").Attribute("data-result")
	if got == "skip-no-gpu" {
		t.Skip("no usable GPU on this machine/platform")
	}
	if got != "threw:TypeError" {
		t.Fatalf("data-result = %q, want %q (dispatchWorkgroups() after end() must throw synchronously, not silently record into an already-submitted compute pass)", got, "threw:TypeError")
	}
}

// TestGPUComputePassEndCalledTwiceRejected is
// TestGPURenderPassEndCalledTwiceRejected's own compute-pass counterpart:
// without the enc.passOpen guard, a second end() would re-append the same
// dispatch commands a second time (since dispatchWorkgroups already wrote
// them straight into enc.commands, "ending" a second time has no draws
// left to re-flush here, but the guard must still fire symmetrically with
// the render-pass encoder sharing the same gpuCommandEncoderState fields).
func TestGPUComputePassEndCalledTwiceRejected(t *testing.T) {
	root, logs, res := runWebGPU(t, page(`
		navigator.gpu.requestAdapter().then(function(a) {
			if (a === null) { document.body.setAttribute('data-result', 'skip-no-gpu'); return; }
			return a.requestDevice();
		}).then(function(dev) {
			if (!dev) return;
			var module = dev.createShaderModule({ code: `+"`"+`
				@group(0) @binding(0) var<storage, read_write> data: array<f32>;
				@compute @workgroup_size(1)
				fn main(@builtin(global_invocation_id) id: vec3<u32>) { data[id.x] = 0.0; }
			`+"`"+` });
			var pipeline = dev.createComputePipeline({ layout: 'auto', compute: { module: module } });
			var encoder = dev.createCommandEncoder();
			var pass = encoder.beginComputePass();
			pass.setPipeline(pipeline);
			pass.end();
			try {
				pass.end();
				document.body.setAttribute('data-result', 'should-have-thrown');
			} catch (e) {
				document.body.setAttribute('data-result', 'threw:' + e.name);
			}
		});
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	got, _ := dom.Find(root, "body").Attribute("data-result")
	if got == "skip-no-gpu" {
		t.Skip("no usable GPU on this machine/platform")
	}
	if got != "threw:TypeError" {
		t.Fatalf("data-result = %q, want %q (end() called a second time on an already-ended compute pass must throw synchronously, not silently re-run it)", got, "threw:TypeError")
	}
}

// TestSetIndexBufferUnsupportedFormatRejected proves setIndexBuffer's own
// bounded scope (uint32 indices only — see its own doc comment, and
// gpuDrawIndexed's firstIndex folding, which assumes 4-byte indices)
// names a rejection for "uint16" rather than silently misreading the
// buffer.
func TestSetIndexBufferUnsupportedFormatRejected(t *testing.T) {
	root, logs, res := runWebGPU(t, page(`
		navigator.gpu.requestAdapter().then(function(a) {
			if (a === null) { document.body.setAttribute('data-result', 'skip-no-gpu'); return; }
			return a.requestDevice();
		}).then(function(dev) {
			if (!dev) return;
			var tex = dev.createTexture({ size: { width: 4, height: 4 }, format: 'r8uint', usage: GPUTextureUsage.RENDER_ATTACHMENT });
			var ib = dev.createBuffer({ size: 24, usage: GPUBufferUsage.INDEX | GPUBufferUsage.COPY_DST });
			var encoder = dev.createCommandEncoder();
			var pass = encoder.beginRenderPass({
				colorAttachments: [{ view: tex.createView(), loadOp: 'clear', storeOp: 'store', clearValue: { r: 0, g: 0, b: 0, a: 0 } }],
			});
			try {
				pass.setIndexBuffer(ib, 'uint16');
				document.body.setAttribute('data-result', 'should-have-thrown');
			} catch (e) {
				document.body.setAttribute('data-result', 'threw:' + e.name);
			}
			pass.end();
			ib.destroy();
			tex.destroy();
		});
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	got, _ := dom.Find(root, "body").Attribute("data-result")
	if got == "skip-no-gpu" {
		t.Skip("no usable GPU on this machine/platform")
	}
	if got != "threw:TypeError" {
		t.Fatalf("data-result = %q, want %q", got, "threw:TypeError")
	}
}

// TestGPUQueueWriteTextureEndToEnd covers GPUQueue.writeTexture's own
// happy path: a full-extent, tightly packed write, read back bit-for-bit
// through copyTextureToBuffer + mapAsync.
func TestGPUQueueWriteTextureEndToEnd(t *testing.T) {
	root, logs, res := runWebGPU(t, page(`
		navigator.gpu.requestAdapter().then(function(a) {
			if (a === null) { document.body.setAttribute('data-result', 'skip-no-gpu'); return; }
			return a.requestDevice();
		}).then(function(dev) {
			if (!dev) return;
			var tex = dev.createTexture({
				size: { width: 4, height: 4 },
				format: 'r8uint',
				usage: GPUTextureUsage.COPY_DST | GPUTextureUsage.COPY_SRC,
			});
			var pixels = new Uint8Array([1,2,3,4, 5,6,7,8, 9,10,11,12, 13,14,15,16]);
			dev.queue.writeTexture({ texture: tex }, pixels, {}, { width: 4, height: 4 });

			var encoder = dev.createCommandEncoder();
			var readback = dev.createBuffer({ size: 16, usage: GPUBufferUsage.MAP_READ | GPUBufferUsage.COPY_DST });
			encoder.copyTextureToBuffer({ texture: tex }, { buffer: readback }, { width: 4, height: 4 });
			dev.queue.submit([encoder.finish()]);

			return readback.mapAsync(GPUMapMode.READ).then(function() {
				var out = Array.from(new Uint8Array(readback.getMappedRange()));
				document.body.setAttribute('data-out', out.join(','));
				readback.destroy();
				tex.destroy();
			});
		}, function(e) {
			document.body.setAttribute('data-result', 'REJECTED:' + e);
		});
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	body := dom.Find(root, "body")
	if got, _ := body.Attribute("data-result"); got == "skip-no-gpu" {
		t.Skip("no usable GPU on this machine/platform")
	} else if got != "" {
		t.Fatalf("data-result = %q, want empty (no rejection)", got)
	}
	want := "1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16"
	if got, _ := body.Attribute("data-out"); got != want {
		t.Fatalf("data-out = %q, want %q (bit-for-bit round trip)", got, want)
	}
}

// TestGPUQueueWriteTextureBoundsRejectedCapturedByErrorScope proves a
// data buffer too small for the given copy size is GPU content
// validation, captured by an open error scope, never a synchronous
// throw — mirroring writeBuffer's own out-of-range convention exactly
// (gpuQueueWriteTexture's own doc comment).
func TestGPUQueueWriteTextureBoundsRejectedCapturedByErrorScope(t *testing.T) {
	root, logs, res := runWebGPU(t, page(`
		navigator.gpu.requestAdapter().then(function(a) {
			if (a === null) { document.body.setAttribute('data-result', 'skip-no-gpu'); return; }
			return a.requestDevice();
		}).then(function(dev) {
			if (!dev) return;
			var tex = dev.createTexture({ size: { width: 4, height: 4 }, format: 'r8uint', usage: GPUTextureUsage.COPY_DST });
			dev.pushErrorScope('validation');
			var threw = false;
			try {
				dev.queue.writeTexture({ texture: tex }, new Uint8Array(4), {}, { width: 4, height: 4 }); // 4 bytes for a 16-byte copy
			} catch (e) { threw = true; }
			document.body.setAttribute('data-threw', String(threw));
			return dev.popErrorScope().then(function(err) {
				document.body.setAttribute('data-captured-name', err ? err.constructor.name : 'null');
				tex.destroy();
			});
		});
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	body := dom.Find(root, "body")
	if skip, _ := body.Attribute("data-result"); skip == "skip-no-gpu" {
		t.Skip("no usable GPU on this machine/platform")
	}
	if got, _ := body.Attribute("data-threw"); got != "false" {
		t.Fatalf("data-threw = %q, want %q (an out-of-range writeTexture must never throw synchronously when a scope is open)", got, "false")
	}
	if got, _ := body.Attribute("data-captured-name"); got != "GPUValidationError" {
		t.Fatalf("data-captured-name = %q, want %q", got, "GPUValidationError")
	}
}

// TestCopyTextureToBufferOutOfBoundsRejected proves copyTextureToBuffer's
// own destination-range check is a WebIDL-level argument problem (a
// thrown TypeError), mirroring copyBufferToBuffer's own convention for
// the same kind of bounds mismatch exactly (gpuCopyTextureToBuffer's own
// doc comment) rather than GPU content validation.
func TestCopyTextureToBufferOutOfBoundsRejected(t *testing.T) {
	root, logs, res := runWebGPU(t, page(`
		navigator.gpu.requestAdapter().then(function(a) {
			if (a === null) { document.body.setAttribute('data-result', 'skip-no-gpu'); return; }
			return a.requestDevice();
		}).then(function(dev) {
			if (!dev) return;
			var tex = dev.createTexture({ size: { width: 4, height: 4 }, format: 'r8uint', usage: GPUTextureUsage.COPY_SRC });
			var readback = dev.createBuffer({ size: 4, usage: GPUBufferUsage.MAP_READ | GPUBufferUsage.COPY_DST }); // too small for a 16-byte copy
			var encoder = dev.createCommandEncoder();
			try {
				encoder.copyTextureToBuffer({ texture: tex }, { buffer: readback }, { width: 4, height: 4 });
				document.body.setAttribute('data-result', 'should-have-thrown');
			} catch (e) {
				document.body.setAttribute('data-result', 'threw:' + e.name);
			}
			tex.destroy();
			readback.destroy();
		});
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	got, _ := dom.Find(root, "body").Attribute("data-result")
	if got == "skip-no-gpu" {
		t.Skip("no usable GPU on this machine/platform")
	}
	if got != "threw:TypeError" {
		t.Fatalf("data-result = %q, want %q", got, "threw:TypeError")
	}
}

// TestGPUCreateTextureMissingSizeRejected proves a descriptor with no
// "size" key at all — gpuReadExtent's own non-object input (v is
// undefined, not a *goja.Object) — resolves to the zero extent rather
// than panicking on a nil object, and that zero extent is then rejected
// by createTexture's own size.width/height>0 check, exactly like an
// explicit zero size would be.
func TestGPUCreateTextureMissingSizeRejected(t *testing.T) {
	root, logs, res := runWebGPU(t, page(`
		navigator.gpu.requestAdapter().then(function(a) {
			if (a === null) { document.body.setAttribute('data-result', 'skip-no-gpu'); return; }
			return a.requestDevice();
		}).then(function(dev) {
			if (!dev) return;
			try {
				dev.createTexture({ format: 'r8uint', usage: GPUTextureUsage.COPY_SRC });
				document.body.setAttribute('data-result', 'should-have-thrown');
			} catch (e) {
				document.body.setAttribute('data-result', 'threw:' + e.name);
			}
		});
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	got, _ := dom.Find(root, "body").Attribute("data-result")
	if got == "skip-no-gpu" {
		t.Skip("no usable GPU on this machine/platform")
	}
	if got != "threw:TypeError" {
		t.Fatalf("data-result = %q, want %q", got, "threw:TypeError")
	}
}

// TestGPUQueueWriteTextureNumericOriginTreatedAsZero proves
// gpuIsZeroOrigin's own non-object branch: an origin that is not an
// object at all (here the plain number 0, never a dict or sequence) is
// not a *goja.Object, so the type assertion fails and gpuIsZeroOrigin
// reports it as the zero origin by default, letting the write through —
// the write must still land for real, not be silently skipped.
func TestGPUQueueWriteTextureNumericOriginTreatedAsZero(t *testing.T) {
	root, logs, res := runWebGPU(t, page(`
		navigator.gpu.requestAdapter().then(function(a) {
			if (a === null) { document.body.setAttribute('data-result', 'skip-no-gpu'); return; }
			return a.requestDevice();
		}).then(function(dev) {
			if (!dev) return;
			var tex = dev.createTexture({
				size: { width: 4, height: 4 },
				format: 'r8uint',
				usage: GPUTextureUsage.COPY_DST | GPUTextureUsage.COPY_SRC,
			});
			var pixels = new Uint8Array([1,2,3,4, 5,6,7,8, 9,10,11,12, 13,14,15,16]);
			dev.queue.writeTexture({ texture: tex, origin: 0 }, pixels, {}, { width: 4, height: 4 });

			var encoder = dev.createCommandEncoder();
			var readback = dev.createBuffer({ size: 16, usage: GPUBufferUsage.MAP_READ | GPUBufferUsage.COPY_DST });
			encoder.copyTextureToBuffer({ texture: tex }, { buffer: readback }, { width: 4, height: 4 });
			dev.queue.submit([encoder.finish()]);

			return readback.mapAsync(GPUMapMode.READ).then(function() {
				var out = Array.from(new Uint8Array(readback.getMappedRange()));
				document.body.setAttribute('data-out', out.join(','));
				readback.destroy();
				tex.destroy();
			});
		}, function(e) {
			document.body.setAttribute('data-result', 'REJECTED:' + e);
		});
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	body := dom.Find(root, "body")
	if got, _ := body.Attribute("data-result"); got == "skip-no-gpu" {
		t.Skip("no usable GPU on this machine/platform")
	} else if got != "" {
		t.Fatalf("data-result = %q, want empty (a numeric origin of 0 must be accepted as the zero origin)", got)
	}
	want := "1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16"
	if got, _ := body.Attribute("data-out"); got != want {
		t.Fatalf("data-out = %q, want %q", got, want)
	}
}

// TestGPUQueueWriteTextureNonZeroPartialOriginRejected proves
// gpuIsZeroOrigin's own per-field default (a missing x/y/z/0/1/2 key
// defaults to 0) does not also zero out a field that IS present: an
// origin object giving only "x": 5 (y and z both absent, so both must
// default to 0 on their own) must still read back as non-zero overall
// and be rejected — a defaulting bug that zeroed every field
// unconditionally would wrongly let this through as the zero origin.
func TestGPUQueueWriteTextureNonZeroPartialOriginRejected(t *testing.T) {
	root, logs, res := runWebGPU(t, page(`
		navigator.gpu.requestAdapter().then(function(a) {
			if (a === null) { document.body.setAttribute('data-result', 'skip-no-gpu'); return; }
			return a.requestDevice();
		}).then(function(dev) {
			if (!dev) return;
			var tex = dev.createTexture({
				size: { width: 4, height: 4 },
				format: 'r8uint',
				usage: GPUTextureUsage.COPY_DST,
			});
			try {
				dev.queue.writeTexture({ texture: tex, origin: { x: 5 } }, new Uint8Array(16), {}, { width: 4, height: 4 });
				document.body.setAttribute('data-result', 'should-have-thrown');
			} catch (e) {
				document.body.setAttribute('data-result', 'threw:' + e.name);
			}
			tex.destroy();
		});
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	got, _ := dom.Find(root, "body").Attribute("data-result")
	if got == "skip-no-gpu" {
		t.Skip("no usable GPU on this machine/platform")
	}
	if got != "threw:TypeError" {
		t.Fatalf("data-result = %q, want %q", got, "threw:TypeError")
	}
}

// TestGPUQueueWriteTextureOriginMissingFieldsDefaultToZero proves
// gpuIsZeroOrigin's own missing-key default (obj.Get(k) genuinely
// returns Go nil, not a JS "undefined" Value, once a key is absent from
// both the object and its whole prototype chain) for every key besides
// the one given explicitly: an origin of only `{x: 0}` must still walk
// y, z, "0", "1" and "2" — each one genuinely missing — through that
// same default, never short-circuiting before reaching them the way
// `{x: 5}` above does. A guard that stopped defaulting those missing
// keys would call ToInteger() on a nil Value and crash; this must
// instead succeed exactly like the explicit numeric-0 origin case.
func TestGPUQueueWriteTextureOriginMissingFieldsDefaultToZero(t *testing.T) {
	root, logs, res := runWebGPU(t, page(`
		navigator.gpu.requestAdapter().then(function(a) {
			if (a === null) { document.body.setAttribute('data-result', 'skip-no-gpu'); return; }
			return a.requestDevice();
		}).then(function(dev) {
			if (!dev) return;
			var tex = dev.createTexture({
				size: { width: 4, height: 4 },
				format: 'r8uint',
				usage: GPUTextureUsage.COPY_DST | GPUTextureUsage.COPY_SRC,
			});
			var pixels = new Uint8Array([1,2,3,4, 5,6,7,8, 9,10,11,12, 13,14,15,16]);
			dev.queue.writeTexture({ texture: tex, origin: { x: 0 } }, pixels, {}, { width: 4, height: 4 });

			var encoder = dev.createCommandEncoder();
			var readback = dev.createBuffer({ size: 16, usage: GPUBufferUsage.MAP_READ | GPUBufferUsage.COPY_DST });
			encoder.copyTextureToBuffer({ texture: tex }, { buffer: readback }, { width: 4, height: 4 });
			dev.queue.submit([encoder.finish()]);

			return readback.mapAsync(GPUMapMode.READ).then(function() {
				var out = Array.from(new Uint8Array(readback.getMappedRange()));
				document.body.setAttribute('data-out', out.join(','));
				readback.destroy();
				tex.destroy();
			});
		}, function(e) {
			document.body.setAttribute('data-result', 'REJECTED:' + e);
		});
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	body := dom.Find(root, "body")
	if got, _ := body.Attribute("data-result"); got == "skip-no-gpu" {
		t.Skip("no usable GPU on this machine/platform")
	} else if got != "" {
		t.Fatalf("data-result = %q, want empty (an origin of {x: 0}, with y/z/sequence keys entirely absent, must default every missing key to 0 and be accepted as the zero origin)", got)
	}
	want := "1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16"
	if got, _ := body.Attribute("data-out"); got != want {
		t.Fatalf("data-out = %q, want %q", got, want)
	}
}

// TestGPUBeginRenderPassClearValueDictFormVisibleWithoutDraw proves
// gpuReadColor's own dict-keyed branch ("r" present selects {r,g,b,a}
// over the [0,1,2,3] sequence form) by never covering the target with
// any draw at all — the clear colour is the ONLY thing that reaches the
// pixels, unlike every other render-pass test above where draws paint
// over it. Choosing the wrong branch here would read undefined
// "0"/"1"/"2"/"3" properties instead and land a different value (or
// none) in the target.
func TestGPUBeginRenderPassClearValueDictFormVisibleWithoutDraw(t *testing.T) {
	root, logs, res := runWebGPU(t, page(`
		navigator.gpu.requestAdapter().then(function(a) {
			if (a === null) { document.body.setAttribute('data-result', 'skip-no-gpu'); return; }
			return a.requestDevice();
		}).then(function(dev) {
			if (!dev) return;
			var tex = dev.createTexture({
				size: { width: 4, height: 4 },
				format: 'r8uint',
				usage: GPUTextureUsage.RENDER_ATTACHMENT | GPUTextureUsage.COPY_SRC,
			});
			var encoder = dev.createCommandEncoder();
			var pass = encoder.beginRenderPass({
				colorAttachments: [{ view: tex.createView(), loadOp: 'clear', storeOp: 'store', clearValue: { r: 9, g: 9, b: 9, a: 9 } }],
			});
			pass.end();

			var readback = dev.createBuffer({ size: 16, usage: GPUBufferUsage.MAP_READ | GPUBufferUsage.COPY_DST });
			encoder.copyTextureToBuffer({ texture: tex }, { buffer: readback }, { width: 4, height: 4 });
			dev.queue.submit([encoder.finish()]);

			return readback.mapAsync(GPUMapMode.READ).then(function() {
				var out = Array.from(new Uint8Array(readback.getMappedRange()));
				document.body.setAttribute('data-out', out.join(','));
				readback.destroy();
				tex.destroy();
			});
		}, function(e) {
			document.body.setAttribute('data-result', 'REJECTED:' + e);
		});
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	body := dom.Find(root, "body")
	if got, _ := body.Attribute("data-result"); got == "skip-no-gpu" {
		t.Skip("no usable GPU on this machine/platform")
	} else if got != "" {
		t.Fatalf("data-result = %q, want empty (no rejection)", got)
	}
	want := make([]string, 16)
	for i := range want {
		want[i] = "9"
	}
	got, _ := body.Attribute("data-out")
	if got != strings.Join(want, ",") {
		t.Fatalf("data-out = %q, want %q (the dict-form clearValue must be read as {r,g,b,a}, not the sequence form)", got, strings.Join(want, ","))
	}
}

// TestGPUBeginRenderPassClearValueNonObjectDefaultsToZero proves
// gpuReadColor's own non-object branch: a clearValue that is not an
// object at all (here the plain number 5, never a dict or sequence) is
// not a *goja.Object, so the type assertion fails and gpuReadColor
// returns the zero colour by default, rather than panicking on a nil
// object or silently using 5. Same no-draw trick as the dict-form test
// above makes the actual clear colour observable.
func TestGPUBeginRenderPassClearValueNonObjectDefaultsToZero(t *testing.T) {
	root, logs, res := runWebGPU(t, page(`
		navigator.gpu.requestAdapter().then(function(a) {
			if (a === null) { document.body.setAttribute('data-result', 'skip-no-gpu'); return; }
			return a.requestDevice();
		}).then(function(dev) {
			if (!dev) return;
			var tex = dev.createTexture({
				size: { width: 4, height: 4 },
				format: 'r8uint',
				usage: GPUTextureUsage.RENDER_ATTACHMENT | GPUTextureUsage.COPY_SRC,
			});
			var encoder = dev.createCommandEncoder();
			var pass = encoder.beginRenderPass({
				colorAttachments: [{ view: tex.createView(), loadOp: 'clear', storeOp: 'store', clearValue: 5 }],
			});
			pass.end();

			var readback = dev.createBuffer({ size: 16, usage: GPUBufferUsage.MAP_READ | GPUBufferUsage.COPY_DST });
			encoder.copyTextureToBuffer({ texture: tex }, { buffer: readback }, { width: 4, height: 4 });
			dev.queue.submit([encoder.finish()]);

			return readback.mapAsync(GPUMapMode.READ).then(function() {
				var out = Array.from(new Uint8Array(readback.getMappedRange()));
				document.body.setAttribute('data-out', out.join(','));
				readback.destroy();
				tex.destroy();
			});
		}, function(e) {
			document.body.setAttribute('data-result', 'REJECTED:' + e);
		});
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	body := dom.Find(root, "body")
	if got, _ := body.Attribute("data-result"); got == "skip-no-gpu" {
		t.Skip("no usable GPU on this machine/platform")
	} else if got != "" {
		t.Fatalf("data-result = %q, want empty (no rejection)", got)
	}
	want := make([]string, 16)
	for i := range want {
		want[i] = "0"
	}
	got, _ := body.Attribute("data-out")
	if got != strings.Join(want, ",") {
		t.Fatalf("data-out = %q, want %q (a non-object clearValue must fall back to the zero colour, not 5)", got, strings.Join(want, ","))
	}
}

// TestGPUBeginRenderPassMissingRenderAttachmentUsageCapturedByErrorScope
// proves beginRenderPass's own panicValidation (a texture not created
// with RENDER_ATTACHMENT usage) is real GPU content validation, captured
// by an open error scope exactly like TestDrawWithoutPipelineCapturedByErrorScope's
// own draw() case — never a raw, unrecovered panic. Before this fix,
// beginRenderPass's registration never wrapped the call in gpuGuard (the
// one mechanism that recovers a panicValidation panic at all), so this
// exact script crashed the whole process instead of rejecting cleanly.
func TestGPUBeginRenderPassMissingRenderAttachmentUsageCapturedByErrorScope(t *testing.T) {
	root, logs, res := runWebGPU(t, page(`
		navigator.gpu.requestAdapter().then(function(a) {
			if (a === null) { document.body.setAttribute('data-result', 'skip-no-gpu'); return; }
			return a.requestDevice();
		}).then(function(dev) {
			if (!dev) return;
			var tex = dev.createTexture({ size: { width: 4, height: 4 }, format: 'r8uint', usage: GPUTextureUsage.TEXTURE_BINDING });
			var encoder = dev.createCommandEncoder();
			dev.pushErrorScope('validation');
			var threw = false;
			try {
				encoder.beginRenderPass({
					colorAttachments: [{ view: tex.createView(), loadOp: 'clear', storeOp: 'store', clearValue: { r: 0, g: 0, b: 0, a: 0 } }],
				});
			} catch (e) { threw = true; }
			document.body.setAttribute('data-threw', String(threw));
			return dev.popErrorScope().then(function(err) {
				document.body.setAttribute('data-captured-name', err ? err.constructor.name : 'null');
				tex.destroy();
			});
		});
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	body := dom.Find(root, "body")
	if skip, _ := body.Attribute("data-result"); skip == "skip-no-gpu" {
		t.Skip("no usable GPU on this machine/platform")
	}
	if got, _ := body.Attribute("data-threw"); got != "false" {
		t.Fatalf("data-threw = %q, want %q (a texture missing RENDER_ATTACHMENT usage is GPU content validation, never a synchronous throw)", got, "false")
	}
	if got, _ := body.Attribute("data-captured-name"); got != "GPUValidationError" {
		t.Fatalf("data-captured-name = %q, want %q", got, "GPUValidationError")
	}
}

// TestGPUCopyTextureToBufferRegistrationGuarded proves
// copyTextureToBuffer's own registration is wrapped the same way as
// beginRenderPass's — a defensive, consistency fix (engine#447 review)
// so any future panicValidation added inside gpuCopyTextureToBuffer is
// caught exactly like every other recording call, instead of silently
// depending on nobody ever adding one. The out-of-bounds case already
// throws a TypeError (see TestCopyTextureToBufferOutOfBoundsRejected) and
// must keep doing so — gpuGuard only ever recovers a panicValidation
// panic, never an ordinary TypeError, so wrapping it must not change that
// behaviour.
func TestGPUCopyTextureToBufferRegistrationGuarded(t *testing.T) {
	root, logs, res := runWebGPU(t, page(`
		navigator.gpu.requestAdapter().then(function(a) {
			if (a === null) { document.body.setAttribute('data-result', 'skip-no-gpu'); return; }
			return a.requestDevice();
		}).then(function(dev) {
			if (!dev) return;
			var tex = dev.createTexture({ size: { width: 4, height: 4 }, format: 'r8uint', usage: GPUTextureUsage.COPY_SRC });
			var readback = dev.createBuffer({ size: 2, usage: GPUBufferUsage.MAP_READ | GPUBufferUsage.COPY_DST });
			var encoder = dev.createCommandEncoder();
			var threw = false;
			try {
				encoder.copyTextureToBuffer({ texture: tex }, { buffer: readback }, { width: 4, height: 4 });
			} catch (e) { threw = (e instanceof TypeError); }
			document.body.setAttribute('data-threw-typeerror', String(threw));
			tex.destroy();
			readback.destroy();
		});
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	body := dom.Find(root, "body")
	if skip, _ := body.Attribute("data-result"); skip == "skip-no-gpu" {
		t.Skip("no usable GPU on this machine/platform")
	}
	if got, _ := body.Attribute("data-threw-typeerror"); got != "true" {
		t.Fatalf("data-threw-typeerror = %q, want %q (an out-of-bounds copy must still throw a TypeError synchronously after wrapping the registration in gpuGuard)", got, "true")
	}
}

// TestGPUReadExtentMissingHeightDefaultsToOne proves gpuReadExtent's dict
// form (engine#447 review bug #2): height is spec-optional
// (GPUExtent3DDict.height defaults to 1) and, before this fix,
// obj.Get("height") returned a genuine Go nil for the missing property —
// calling ToInteger() on it panicked instead of defaulting. This is a
// plain Go-level unit test (no GPU/JS harness needed): before the fix it
// crashed this whole test binary with an unrecovered nil-pointer panic,
// not merely fail an assertion.
func TestGPUReadExtentMissingHeightDefaultsToOne(t *testing.T) {
	vm := goja.New()
	obj := vm.NewObject()
	obj.Set("width", 4)
	got := gpuReadExtent(obj)
	want := gpuExtent{w: 4, h: 1, d: 1}
	if got != want {
		t.Fatalf("gpuReadExtent({width:4}) = %+v, want %+v", got, want)
	}
}

// TestGPUReadExtentSequenceFormMissingTrailingDefaultsToOne is
// gpuReadExtent's sequence-form counterpart: obj.Get("1")/obj.Get("2")
// absent (a short array) must default to 1, not nil-deref.
func TestGPUReadExtentSequenceFormMissingTrailingDefaultsToOne(t *testing.T) {
	vm := goja.New()
	obj := vm.NewObject()
	obj.Set("0", 4)
	got := gpuReadExtent(obj)
	want := gpuExtent{w: 4, h: 1, d: 1}
	if got != want {
		t.Fatalf("gpuReadExtent([4]) = %+v, want %+v", got, want)
	}
}

// TestGPUReadColorMissingGBADefaultToZero proves gpuReadColor's dict
// form: g/b/a are spec-optional in this codebase's own "non-crashing
// default over strict WebIDL enforcement" convention (gpuReadColor's own
// doc comment) — obj.Get("g") absent must default to 0, not nil-deref.
func TestGPUReadColorMissingGBADefaultToZero(t *testing.T) {
	// A deleted guard at webgpu.go:1693 turns this missing-property read
	// into a nil-pointer panic that would crash the whole test binary
	// before the assertion below ever ran, masking the result (mutsweep
	// would read it as "caught by panic", luck rather than a test). This
	// local recover converts that crash into an ordinary failed
	// assertion, same as any other wrong value.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("gpuReadColor({r:5}) panicked: %v", r)
		}
	}()
	vm := goja.New()
	obj := vm.NewObject()
	obj.Set("r", 5.0)
	got := gpuReadColor(obj)
	want := [4]float64{5, 0, 0, 0}
	if got != want {
		t.Fatalf("gpuReadColor({r:5}) = %v, want %v", got, want)
	}
}

// TestGPUReadColorSequenceFormMissingTrailingDefaultsToZero is
// gpuReadColor's sequence-form counterpart.
func TestGPUReadColorSequenceFormMissingTrailingDefaultsToZero(t *testing.T) {
	// Same reasoning as TestGPUReadColorMissingGBADefaultToZero's own
	// recover: this exercises the same gpuGetFloat guard (webgpu.go:1693),
	// just through the sequence-form branch's "1"/"2"/"3" keys.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("gpuReadColor([5]) panicked: %v", r)
		}
	}()
	vm := goja.New()
	obj := vm.NewObject()
	obj.Set("0", 5.0)
	got := gpuReadColor(obj)
	want := [4]float64{5, 0, 0, 0}
	if got != want {
		t.Fatalf("gpuReadColor([5]) = %v, want %v", got, want)
	}
}

// TestGPUReadColorExplicitUndefinedGDefaultsToZero is
// TestGPUReadColorMissingGBADefaultToZero's explicit-undefined
// counterpart (engine#447 review, mutsweep gap on gpuGetFloat's
// `v == nil || goja.IsUndefined(v)` guard at webgpu.go:1693): a property
// set to the literal value `undefined` is NOT the Go nil that a merely
// absent property produces — obj.Get("g") instead returns a non-nil
// goja.Value for which goja.IsUndefined(v) is true. Without the
// IsUndefined half of the guard, gpuGetFloat would call ToFloat() on
// that value instead of defaulting to 0. vm.NewObject()+Set never
// produces this shape (Set always assigns a concrete value), so this
// needs a real script evaluation to get a property whose value is
// `undefined` rather than one that was never set.
func TestGPUReadColorExplicitUndefinedGDefaultsToZero(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("gpuReadColor({r:5, g:undefined, a:2.5}) panicked: %v", r)
		}
	}()
	vm := goja.New()
	v, err := vm.RunString(`({r: 5, g: undefined, a: 2.5})`)
	if err != nil {
		t.Fatalf("RunString: %v", err)
	}
	got := gpuReadColor(v)
	want := [4]float64{5, 0, 0, 2.5}
	if got != want {
		t.Fatalf("gpuReadColor({r:5, g:undefined, a:2.5}) = %v, want %v", got, want)
	}
}

// TestGPUCreateTextureMissingUsageRejected proves createTexture's own
// usage read (required, no spec default) now throws a clean TypeError
// instead of nil-deref-panicking the whole process when usage is
// omitted.
func TestGPUCreateTextureMissingUsageRejected(t *testing.T) {
	root, logs, res := runWebGPU(t, page(`
		navigator.gpu.requestAdapter().then(function(a) {
			if (a === null) { document.body.setAttribute('data-result', 'skip-no-gpu'); return; }
			return a.requestDevice();
		}).then(function(dev) {
			if (!dev) return;
			try {
				dev.createTexture({ size: { width: 4, height: 4 }, format: 'r8uint' });
				document.body.setAttribute('data-result', 'should-have-thrown');
			} catch (e) {
				document.body.setAttribute('data-result', 'threw:' + e.name);
			}
		});
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	got, _ := dom.Find(root, "body").Attribute("data-result")
	if got == "skip-no-gpu" {
		t.Skip("no usable GPU on this machine/platform")
	}
	if got != "threw:TypeError" {
		t.Fatalf("data-result = %q, want %q", got, "threw:TypeError")
	}
}

// TestGPUCreateRenderPipelineMissingVertexFieldsRejected proves
// createRenderPipeline's own vertex.buffers[] reads (arrayStride,
// attributes[].format, attributes[].shaderLocation, attributes[].offset
// — all required, no spec default) now throw a clean TypeError instead
// of nil-deref-panicking when any one of them is omitted.
func TestGPUCreateRenderPipelineMissingVertexFieldsRejected(t *testing.T) {
	cases := []string{
		// missing arrayStride
		`{ buffers: [{ attributes: [{ format: 'float32x2', shaderLocation: 0, offset: 0 }] }] }`,
		// missing attribute format
		`{ buffers: [{ arrayStride: 8, attributes: [{ shaderLocation: 0, offset: 0 }] }] }`,
		// missing shaderLocation
		`{ buffers: [{ arrayStride: 8, attributes: [{ format: 'float32x2', offset: 0 }] }] }`,
		// missing offset
		`{ buffers: [{ arrayStride: 8, attributes: [{ format: 'float32x2', shaderLocation: 0 }] }] }`,
	}
	for _, vertexJS := range cases {
		root, logs, res := runWebGPU(t, page(`
			navigator.gpu.requestAdapter().then(function(a) {
				if (a === null) { document.body.setAttribute('data-result', 'skip-no-gpu'); return; }
				return a.requestDevice();
			}).then(function(dev) {
				if (!dev) return;
				var mod = dev.createShaderModule({ code: `+"`"+renderWGSL+"`"+` });
				try {
					dev.createRenderPipeline({
						vertex: Object.assign({ module: mod }, `+vertexJS+`),
						fragment: { module: mod, targets: [{ format: 'r8uint' }] },
					});
					document.body.setAttribute('data-result', 'should-have-thrown');
				} catch (e) {
					document.body.setAttribute('data-result', 'threw:' + e.name);
				}
			});
		`))
		if res.ScriptsFailed != 0 {
			t.Fatalf("script failed: %+v logs=%v", res, logs)
		}
		got, _ := dom.Find(root, "body").Attribute("data-result")
		if got == "skip-no-gpu" {
			t.Skip("no usable GPU on this machine/platform")
		}
		if got != "threw:TypeError" {
			t.Fatalf("case %q: data-result = %q, want %q", vertexJS, got, "threw:TypeError")
		}
	}
}

// TestGPUCreateRenderPipelineUnsupportedRenderOptionsRejected is
// engine#447 review bug #8: a whole set of GPURenderPipelineDescriptor
// fields (primitive.cullMode/frontFace/unclippedDepth, multisample.count/
// alphaToCoverageEnabled, vertex.constants, fragment.constants,
// fragment.targets[0].blend/writeMask, vertex.buffers[].stepMode) were
// read NOWHERE in gpuCreateRenderPipeline before this fix — go-macos/
// metal's own RenderPipelineDescriptor simply has no field for most of
// them, so a caller asking for back-face culling, 4x MSAA, an instance-
// stepped vertex buffer, alpha blending or a partial colour write mask
// got a pipeline that silently behaved as if none of that had been
// asked for, with no error anywhere — exactly the kind of "unsupported
// option accepted and ignored" shape bug #6/#7 already fixed for draw
// validation and pass lifecycle. Each case here sets exactly one such
// option to a non-default value and must now be a named, synchronous
// TypeError instead.
func TestGPUCreateRenderPipelineUnsupportedRenderOptionsRejected(t *testing.T) {
	base := `{
			layout: 'auto',
			vertex: Object.assign({ module: mod, entryPoint: 'vmain', buffers: [{ arrayStride: 8, attributes: [{ shaderLocation: 0, offset: 0, format: 'float32x2' }] }] }, VEXTRA),
			fragment: Object.assign({ module: mod, entryPoint: 'fmain', targets: [Object.assign({ format: 'r8uint' }, TEXTRA)] }, FEXTRA),
		}`
	cases := []struct {
		name   string
		prim   string // primitive object literal, or "" to omit
		ms     string // multisample object literal, or "" to omit
		vextra string // extra vertex.* fields, or "{}"
		fextra string // extra fragment.* fields, or "{}"
		textra string // extra fragment.targets[0].* fields, or "{}"
	}{
		{name: "cullMode", prim: `{ cullMode: 'back' }`},
		{name: "frontFace", prim: `{ frontFace: 'cw' }`},
		{name: "unclippedDepth", prim: `{ unclippedDepth: true }`},
		{name: "multisample.count", ms: `{ count: 4 }`},
		{name: "multisample.alphaToCoverageEnabled", ms: `{ alphaToCoverageEnabled: true }`},
		{name: "vertex.constants", vextra: `{ constants: { x: 1 } }`},
		{name: "fragment.constants", fextra: `{ constants: { x: 1 } }`},
		{name: "fragment.targets[0].blend", textra: `{ blend: { color: { srcFactor: 'one', dstFactor: 'zero', operation: 'add' }, alpha: { srcFactor: 'one', dstFactor: 'zero', operation: 'add' } } }`},
		{name: "fragment.targets[0].writeMask", textra: `{ writeMask: 0x1 }`},
		{name: "vertex.buffers[].stepMode", vextra: `{ buffers: [{ arrayStride: 8, stepMode: 'instance', attributes: [{ shaderLocation: 0, offset: 0, format: 'float32x2' }] }] }`},
	}
	for _, c := range cases {
		prim, ms, vextra, fextra, textra := c.prim, c.ms, c.vextra, c.fextra, c.textra
		if vextra == "" {
			vextra = "{}"
		}
		if fextra == "" {
			fextra = "{}"
		}
		if textra == "" {
			textra = "{}"
		}
		descJS := strings.NewReplacer(
			"VEXTRA", vextra,
			"FEXTRA", fextra,
			"TEXTRA", textra,
		).Replace(base)
		if prim != "" {
			descJS = strings.Replace(descJS, "layout: 'auto',", "layout: 'auto', primitive: "+prim+",", 1)
		}
		if ms != "" {
			descJS = strings.Replace(descJS, "layout: 'auto',", "layout: 'auto', multisample: "+ms+",", 1)
		}
		root, logs, res := runWebGPU(t, page(`
			navigator.gpu.requestAdapter().then(function(a) {
				if (a === null) { document.body.setAttribute('data-result', 'skip-no-gpu'); return; }
				return a.requestDevice();
			}).then(function(dev) {
				if (!dev) return;
				var mod = dev.createShaderModule({ code: `+"`"+renderWGSL+"`"+` });
				try {
					dev.createRenderPipeline(`+descJS+`);
					document.body.setAttribute('data-result', 'should-have-thrown');
				} catch (e) {
					document.body.setAttribute('data-result', 'threw:' + e.name);
				}
			});
		`))
		if res.ScriptsFailed != 0 {
			t.Fatalf("case %q: script failed: %+v logs=%v", c.name, res, logs)
		}
		got, _ := dom.Find(root, "body").Attribute("data-result")
		if got == "skip-no-gpu" {
			t.Skip("no usable GPU on this machine/platform")
		}
		if got != "threw:TypeError" {
			t.Fatalf("case %q: data-result = %q, want %q (this render option must be a named rejection, not silently ignored)", c.name, got, "threw:TypeError")
		}
	}
}
