// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package js

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-webengine/engine/dom"
)

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
