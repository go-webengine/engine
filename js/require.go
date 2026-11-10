// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package js

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/dop251/goja"
	"github.com/evanw/esbuild/pkg/api"
	"github.com/go-webengine/esbuildsandbox"
)

// require.go backs the require() call transpileModernJS's own esbuild pass
// (transpile.go) lowers every import() expression to — the one thing esbuild
// itself cannot do for a VARIABLE specifier (`await import(j.module)`,
// engine#412/#413's own real trigger): esbuild can only resolve/inline a
// STRING LITERAL specifier at transpile time; a value only known once the
// script is already running has to be fetched, transpiled and executed for
// real, at the moment require() is actually called.
//
// requireBuildTimeout bounds one require() call's own transpile step (fetch
// bounds are transpile.go's own maxTranspileFetches/maxTranspileSourceBytes
// and transpileFetchTimeout, shared across the whole render — see js.go's
// requireFetched/requireBytes doc comment).
const requireBuildTimeout = 5 * time.Second

// makeRequireFn returns a require(specifier) closure bound to fromURL (the
// requiring script/module's own URL, so a relative specifier resolves
// against IT, not the page) — one fresh closure per script/module, since
// each has its own base. The returned Go func is handed to goja via
// vm.ToValue/vm.Set, never called directly from Go.
func (b *binder) makeRequireFn(fromURL string) func(goja.FunctionCall) goja.Value {
	return func(call goja.FunctionCall) goja.Value {
		spec := call.Argument(0).String()
		exports, err := b.requireModule(fromURL, spec)
		if err != nil {
			panic(b.vm.NewTypeError("require: " + err.Error()))
		}
		return exports
	}
}

// requireModule resolves specifier against fromURL, fetches it, transpiles
// it to CommonJS (recursively lowering its own static imports AND any
// further variable-specifier dynamic import of its own, the same way), runs
// it in a fresh CommonJS module scope (module/exports/require — a REAL
// require, recursively, not a stub), and returns its exports. Cached by
// resolved absolute URL: a chunk required from two call sites (a shared
// vendor chunk, or a circular require) fetches/executes once and returns the
// SAME object every time, matching real module-singleton semantics — the
// in-progress exports object is cached BEFORE executing specifically so a
// circular require (A requires B, B requires A) sees that unfinished object
// instead of recursing forever, the same break Node's own CommonJS loader
// uses.
func (b *binder) requireModule(fromURL, specifier string) (goja.Value, error) {
	abs, ok := resolveURL(fromURL, specifier)
	if !ok {
		return nil, fmt.Errorf("cannot resolve %q", specifier)
	}
	if v, ok := b.requireCache[abs]; ok {
		return v, nil
	}
	if b.requireCache == nil {
		b.requireCache = map[string]goja.Value{}
	}
	if b.requireFetched >= maxTranspileFetches || b.requireBytes >= maxTranspileSourceBytes {
		return nil, fmt.Errorf("budget exhausted: %s", abs)
	}

	fctx, cancel := context.WithTimeout(b.opt.Ctx, transpileFetchTimeout)
	src, ok := b.fetchTranspileChunk(fctx, abs)
	cancel()
	if !ok {
		return nil, fmt.Errorf("fetch failed: %s", abs)
	}
	b.requireFetched++
	b.requireBytes += len(src)

	cjs, ok := b.bundleRequireSource(abs, src)
	if !ok {
		return nil, fmt.Errorf("transpile failed: %s", abs)
	}

	wrapped := "(function(module, exports, require){\n" + cjs + "\n})"
	prog, err := goja.Compile(abs, wrapped, false)
	if err != nil {
		return nil, fmt.Errorf("compile failed: %s: %w", abs, err)
	}
	fnVal, err := b.vm.RunProgram(prog)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", abs, err)
	}
	fn, ok := goja.AssertFunction(fnVal)
	if !ok {
		return nil, fmt.Errorf("internal: wrapped module is not callable: %s", abs)
	}

	moduleObj := b.vm.NewObject()
	exportsObj := b.vm.NewObject()
	if err := moduleObj.Set("exports", exportsObj); err != nil {
		return nil, fmt.Errorf("internal: %s: %w", abs, err)
	}
	b.requireCache[abs] = exportsObj

	reqVal := b.vm.ToValue(b.makeRequireFn(abs))
	if _, err := fn(goja.Undefined(), moduleObj, exportsObj, reqVal); err != nil {
		delete(b.requireCache, abs)
		return nil, fmt.Errorf("execute failed: %s: %w", abs, err)
	}
	final := moduleObj.Get("exports")
	b.requireCache[abs] = final
	return final, nil
}

// bundleRequireSource transpiles one required module's own source to
// CommonJS, the same esbuild shape transpileModernJS uses (Bundle:true so
// ITS OWN static imports resolve too, "dynamic-import" unsupported so any
// further variable-specifier import() of its own ALSO lowers to a require()
// call — requireModule's own recursion then backs that, exactly as it did
// for the entry call), sharing the same per-render requireFetched/
// requireBytes budget transpileModernJS's own plugin does.
func (b *binder) bundleRequireSource(scriptURL, src string) (string, bool) {
	tctx, cancel := context.WithTimeout(b.opt.Ctx, requireBuildTimeout)
	defer cancel()

	overBudget := func() bool {
		return tctx.Err() != nil || b.requireFetched >= maxTranspileFetches || b.requireBytes >= maxTranspileSourceBytes
	}
	plugin := api.Plugin{
		Name: "webengine-require",
		Setup: func(build api.PluginBuild) {
			build.OnResolve(api.OnResolveOptions{Filter: `.*`}, func(args api.OnResolveArgs) (api.OnResolveResult, error) {
				if overBudget() {
					return api.OnResolveResult{}, fmt.Errorf("require budget exhausted")
				}
				base := args.Importer
				if base == "" || !strings.HasPrefix(base, "http") {
					base = scriptURL
				}
				abs, ok := resolveURL(base, args.Path)
				if !ok {
					return api.OnResolveResult{}, fmt.Errorf("cannot resolve %q", args.Path)
				}
				return api.OnResolveResult{Path: abs, Namespace: "http"}, nil
			})
			build.OnLoad(api.OnLoadOptions{Filter: `.*`, Namespace: "http"}, func(args api.OnLoadArgs) (api.OnLoadResult, error) {
				if overBudget() {
					return api.OnLoadResult{}, fmt.Errorf("require budget exhausted")
				}
				chunk, ok := b.fetchTranspileChunk(tctx, args.Path)
				if !ok {
					empty := ""
					return api.OnLoadResult{Contents: &empty, Loader: api.LoaderJS, ResolveDir: esbuildsandbox.ResolveDir()}, nil
				}
				b.requireFetched++
				b.requireBytes += len(chunk)
				return api.OnLoadResult{Contents: &chunk, Loader: api.LoaderJS, ResolveDir: esbuildsandbox.ResolveDir()}, nil
			})
		},
	}

	type buildOut struct {
		res api.BuildResult
	}
	done := make(chan buildOut, 1)
	go func() {
		done <- buildOut{res: api.Build(api.BuildOptions{
			Stdin: &api.StdinOptions{
				Contents:   src,
				ResolveDir: esbuildsandbox.ResolveDir(),
				Sourcefile: scriptURL,
				Loader:     api.LoaderJS,
			},
			Bundle:        true,
			Format:        api.FormatCommonJS,
			Target:        api.ES2017,
			Write:         false,
			LogLevel:      api.LogLevelSilent,
			Plugins:       []api.Plugin{plugin},
			Sourcemap:     api.SourceMapNone,
			LegalComments: api.LegalCommentsNone,
			Supported:     map[string]bool{"import-meta": false, "dynamic-import": false},
		})}
	}()
	select {
	case out := <-done:
		if len(out.res.Errors) > 0 || len(out.res.OutputFiles) == 0 {
			return "", false
		}
		return string(out.res.OutputFiles[0].Contents), true
	case <-tctx.Done():
		return "", false
	}
}
