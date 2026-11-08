// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package js

import (
	"crypto/rand"
	"fmt"
	"net/url"
	"strings"

	"github.com/dop251/goja"
)

// Requested directly by go-wasmcontainers (engine#342 fallout), confirmed
// live via their own probe: go-gfx/webgpu v0.15.0's record_js.go loads its
// replay script inside a Worker as `new Blob([source], {type:
// "text/javascript"})` -> `URL.createObjectURL(blob)` ->
// `importScripts(thatBlobURL)` -> `URL.revokeObjectURL(url)`. This file
// implements exactly that round trip: a Blob holding concatenated string
// parts (the one confirmed shape — BufferSource/Blob parts are real per
// the File API but no confirmed caller passes them), and the File API §8
// "blob URL store" (createObjectURL/revokeObjectURL), consulted directly
// by importScripts (worker.go) for a `blob:` URL.

// newBlob implements `new Blob(parts, options)` (File API §5): parts is an
// array whose entries this binding coerces to string and concatenates —
// the one confirmed shape go-wasmcontainers' own dependency needs. `.size`/
// `.type` are read-only per spec; `.text()` returns an already-resolved
// Promise of the content, matching every other synchronous-under-the-hood
// Promise this engine returns (see fetch()'s own doc comment) — provided
// for spec completeness, since nothing in the confirmed use case calls it.
func (b *binder) newBlob(call goja.ConstructorCall) *goja.Object {
	var sb strings.Builder
	if arr, ok := call.Argument(0).(*goja.Object); ok && arr.ClassName() == "Array" {
		length := int(arr.Get("length").ToInteger())
		for i := 0; i < length; i++ {
			sb.WriteString(arr.Get(fmt.Sprint(i)).String())
		}
	}
	typ := ""
	if opts, ok := call.Argument(1).(*goja.Object); ok {
		if t := opts.Get("type"); t != nil && !goja.IsUndefined(t) {
			typ = strings.ToLower(t.String())
		}
	}
	data := []byte(sb.String())
	o := b.vm.NewObject()
	_ = o.DefineDataPropertySymbol(goja.SymToStringTag, b.vm.ToValue("Blob"), goja.FLAG_FALSE, goja.FLAG_FALSE, goja.FLAG_TRUE)
	o.Set("size", b.vm.ToValue(len(data)))
	o.Set("type", b.vm.ToValue(typ))
	o.Set("text", func(goja.FunctionCall) goja.Value { return b.resolved(b.vm.ToValue(string(data))) })
	if b.blobs == nil {
		b.blobs = map[*goja.Object][]byte{}
	}
	b.blobs[o] = data
	return o
}

// installURL wires the URL global (buildURL's own constructor, events.go)
// plus its own static createObjectURL/revokeObjectURL methods (File API
// §8's "blob URL store") — shared between the main document's own
// install() and every Worker's installWorkerGlobals, since both need
// identical behaviour, each scoped to its OWN binder (see blobURLs' own
// doc comment in js.go for why this is never a package-level registry).
func (b *binder) installURL(g *goja.Object) {
	ctorVal := b.vm.ToValue(func(call goja.ConstructorCall) *goja.Object {
		return b.buildURL(call.Argument(0).String(), call.Argument(1))
	})
	if ctorObj, ok := ctorVal.(*goja.Object); ok {
		ctorObj.Set("createObjectURL", func(call goja.FunctionCall) goja.Value {
			obj, _ := call.Argument(0).(*goja.Object)
			data, ok := b.blobs[obj]
			if !ok {
				panic(b.vm.NewTypeError("URL.createObjectURL: argument is not a Blob"))
			}
			u := b.newBlobURL()
			if b.blobURLs == nil {
				b.blobURLs = map[string][]byte{}
			}
			b.blobURLs[u] = data
			return b.vm.ToValue(u)
		})
		ctorObj.Set("revokeObjectURL", func(call goja.FunctionCall) goja.Value {
			delete(b.blobURLs, call.Argument(0).String())
			return goja.Undefined()
		})
	}
	g.Set("URL", ctorVal)
	g.Set("Blob", func(call goja.ConstructorCall) *goja.Object { return b.newBlob(call) })
}

// newBlobURL mints a unique blob: URL (File API §8: "blob:" + the
// creating environment's own origin + "/" + a UUID) — the shape real
// browsers use, though nothing in this engine's own blobURLs lookup
// depends on that shape; it only needs to be unique and round-trip
// through exact string equality.
func (b *binder) newBlobURL() string {
	var raw [16]byte
	_, _ = rand.Read(raw[:])
	org := "null"
	if u, err := url.Parse(b.opt.PageURL); err == nil {
		org = origin(u)
	}
	return fmt.Sprintf("blob:%s/%x-%x-%x-%x-%x", org, raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16])
}
