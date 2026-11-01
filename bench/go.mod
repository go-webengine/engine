module github.com/go-webengine/engine/bench

go 1.27.3

// Local parent engine module. Keeps chromedp out of the engine's CGO=0,
// 6-arch build and coverage gate.
replace github.com/go-webengine/engine => ..

require (
	github.com/chromedp/cdproto v0.0.0-20260922220944-a19bff23514f
	github.com/chromedp/chromedp v0.16.0
	github.com/go-images/images v0.1.1-0.20261026203936-4ac989d310a1
	github.com/go-webengine/engine v0.0.0-00010101000000-000000000000
	golang.org/x/image v0.47.0
)

require (
	github.com/andybalholm/brotli v1.2.7 // indirect
	github.com/breml/rootcerts v0.3.7 // indirect
	github.com/chromedp/sysutil v1.1.0 // indirect
	github.com/dlclark/regexp2/v2 v2.5.2 // indirect
	github.com/dop251/goja v0.0.0-20260926152631-39ec2650adc9 // indirect
	github.com/dsnet/compress v0.0.2-0.20230904184137-39efe44ab707 // indirect
	github.com/evanw/esbuild v0.28.2 // indirect
	github.com/go-browserhttp/browserhttp v0.2.0 // indirect
	github.com/go-gfx/gfx v0.37.0 // indirect
	github.com/go-gfx/oksvg v0.3.1 // indirect
	github.com/go-images/gif v0.2.0 // indirect
	github.com/go-images/jpeg v0.3.0 // indirect
	github.com/go-images/jpeg2000 v0.14.0 // indirect
	github.com/go-images/png v0.2.0 // indirect
	github.com/go-json-experiment/json v0.0.0-20260820222146-c27c302e5fc3 // indirect
	github.com/go-opentype/fonts v0.14.0 // indirect
	github.com/go-opentype/opentype v0.16.2-0.20261027113239-5e69589e9a5e // indirect
	github.com/go-simd/highway v0.0.14 // indirect
	github.com/go-sourcemap/sourcemap v2.1.4+incompatible // indirect
	github.com/go-webengine/esbuildsandbox v0.1.0 // indirect
	github.com/go-widgets/painter v0.16.0 // indirect
	github.com/gobwas/httphead v0.1.0 // indirect
	github.com/gobwas/pool v0.2.1 // indirect
	github.com/gobwas/ws v1.4.0 // indirect
	github.com/google/pprof v0.0.0-20240727154555-813a5fbdbec8 // indirect
	github.com/klauspost/compress v1.18.7 // indirect
	github.com/refraction-networking/utls v1.8.2 // indirect
	github.com/srwiley/rasterx v0.0.0-20220730225603-2ab79fcdd4ef // indirect
	github.com/tannevaled/gobig2 v0.2.0 // indirect
	github.com/tetratelabs/wazero v1.12.0 // indirect
	golang.org/x/crypto v0.58.0 // indirect
	golang.org/x/net v0.62.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
)
