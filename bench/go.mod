module github.com/go-webengine/engine/bench

go 1.26.4

// Local parent engine module. Keeps chromedp out of the engine's CGO=0,
// 6-arch build and coverage gate.
replace github.com/go-webengine/engine => ..

require (
	github.com/chromedp/cdproto v0.0.0-20250403032234-65de8f5d025b
	github.com/chromedp/chromedp v0.13.6
	github.com/go-images/images v0.0.0-20260702213524-ea366b42f216
	github.com/go-webengine/engine v0.0.0-00010101000000-000000000000
	golang.org/x/image v0.20.0
)

require (
	github.com/andybalholm/brotli v1.0.6 // indirect
	github.com/chromedp/sysutil v1.1.0 // indirect
	github.com/go-browserhttp/browserhttp v0.1.0 // indirect
	github.com/go-json-experiment/json v0.0.0-20250211171154-1ae217ad3535 // indirect
	github.com/go-opentype/fonts v0.4.2 // indirect
	github.com/go-opentype/opentype v0.5.0 // indirect
	github.com/go-widgets/painter v0.2.0 // indirect
	github.com/gobwas/httphead v0.1.0 // indirect
	github.com/gobwas/pool v0.2.1 // indirect
	github.com/gobwas/ws v1.4.0 // indirect
	github.com/klauspost/compress v1.17.4 // indirect
	github.com/refraction-networking/utls v1.8.2 // indirect
	golang.org/x/crypto v0.54.0 // indirect
	golang.org/x/net v0.57.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.40.0 // indirect
)
