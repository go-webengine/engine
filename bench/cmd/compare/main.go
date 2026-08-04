// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

// Command compare benchmarks the pure-Go webengine renderer against real
// headless Chromium on both fidelity (SSIM + pixel-diff over the common region)
// and speed (median wall-clock over N runs after a warmup). For each URL it
// writes a side-by-side montage (webengine | chrome | diff-heatmap) plus a
// machine-readable results.json and a human-readable REPORT.md table.
//
// It lives in a separate module (github.com/go-webengine/engine/bench) so its
// Chrome-driving dependency (chromedp) never enters the engine module's CGO=0,
// 6-arch build or its coverage gate.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"

	engine "github.com/go-webengine/engine"
	"github.com/go-webengine/engine/bench/metrics"
)

// Result is one URL's fidelity + speed comparison, emitted to results.json.
type Result struct {
	URL         string  `json:"url"`
	WebengineMs float64 `json:"webengine_ms"`
	ChromeMs    float64 `json:"chrome_ms"`
	SpeedRatio  float64 `json:"speed_ratio"` // chrome_ms / webengine_ms (>1 == webengine faster)
	SSIM        float64 `json:"ssim"`
	PixDiffPct  float64 `json:"pixdiff_pct"`
	RegionW     int     `json:"region_w"`
	RegionH     int     `json:"region_h"`
	Montage     string  `json:"montage"`
	WebengineOK bool    `json:"webengine_ok"`
	ChromeOK    bool    `json:"chrome_ok"`
	Note        string  `json:"note,omitempty"`
}

func main() { os.Exit(run()) }

func run() int {
	var (
		urlsPath   = flag.String("urls", "urls.txt", "path to newline-delimited URL list (# comments allowed)")
		outDir     = flag.String("out", "out", "directory for montage PNGs")
		reportPath = flag.String("report", "REPORT.md", "path for the Markdown report")
		jsonPath   = flag.String("json", "results.json", "path for the JSON results")
		n          = flag.Int("n", 5, "timed runs per engine per URL")
		warmup     = flag.Int("warmup", 1, "untimed warmup runs per engine per URL")
		width      = flag.Int("width", 1024, "viewport width (device-scale 1)")
		height     = flag.Int("height", 768, "viewport height (device-scale 1)")
		settleMs   = flag.Int("settle", 900, "ms to let Chrome settle (JS/layout) before screenshot")
		timeoutS   = flag.Int("timeout", 45, "per-render timeout in seconds")
		pixThresh  = flag.Int("pixthresh", 16, "per-channel diff threshold (0-255) for pixdiff")
		maxHeight  = flag.Int("maxheight", 2500, "cap the common fidelity/montage region to this many rows (0 = full page)")
	)
	flag.Parse()

	chromeBin := os.Getenv("CHROME_BIN")
	if chromeBin == "" {
		chromeBin = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
	}

	urls, err := readURLs(*urlsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "compare: %v\n", err)
		return 1
	}
	if len(urls) == 0 {
		fmt.Fprintln(os.Stderr, "compare: no URLs to test")
		return 1
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "compare: %v\n", err)
		return 1
	}

	viewport := image.Rect(0, 0, *width, *height)

	// One Chrome process for the whole run; each render uses a fresh tab.
	allocCtx, cancelAlloc := newChromeAllocator(chromeBin, *width, *height)
	defer cancelAlloc()

	var results []Result
	for _, u := range urls {
		fmt.Fprintf(os.Stderr, "== %s\n", u)
		r := Result{URL: u, Montage: filepath.Join(*outDir, slug(u)+".png")}

		weImg, weMs, weErr := benchWebengine(u, viewport, *n, *warmup, time.Duration(*timeoutS)*time.Second)
		if weErr != nil {
			fmt.Fprintf(os.Stderr, "   webengine: FAIL %v\n", weErr)
			r.Note = appendNote(r.Note, "webengine error: "+weErr.Error())
		} else {
			r.WebengineOK = true
			r.WebengineMs = weMs
			fmt.Fprintf(os.Stderr, "   webengine: %.1f ms (%dx%d)\n", weMs, weImg.Bounds().Dx(), weImg.Bounds().Dy())
		}

		chImg, chMs, chErr := benchChrome(allocCtx, u, *width, *height, *n, *warmup,
			time.Duration(*settleMs)*time.Millisecond, time.Duration(*timeoutS)*time.Second)
		if chErr != nil {
			fmt.Fprintf(os.Stderr, "   chrome:    FAIL %v\n", chErr)
			r.Note = appendNote(r.Note, "chrome error: "+chErr.Error())
		} else {
			r.ChromeOK = true
			r.ChromeMs = chMs
			fmt.Fprintf(os.Stderr, "   chrome:    %.1f ms (%dx%d)\n", chMs, chImg.Bounds().Dx(), chImg.Bounds().Dy())
		}

		if r.WebengineOK && r.ChromeOK {
			ca, cb := metrics.CommonRegion(weImg, chImg)
			ca = metrics.CapHeight(ca, *maxHeight)
			cb = metrics.CapHeight(cb, *maxHeight)
			r.RegionW, r.RegionH = ca.Bounds().Dx(), ca.Bounds().Dy()
			r.SSIM = metrics.SSIM(ca, cb, 8, 4)
			r.PixDiffPct = 100 * metrics.PixDiff(ca, cb, uint8(*pixThresh))
			if r.WebengineMs > 0 {
				r.SpeedRatio = r.ChromeMs / r.WebengineMs
			}
			heat := metrics.Heatmap(ca, cb)
			mont := metrics.Montage(ca, cb, heat)
			if err := savePNG(r.Montage, mont); err != nil {
				fmt.Fprintf(os.Stderr, "   montage:   FAIL %v\n", err)
			} else {
				fmt.Fprintf(os.Stderr, "   ssim=%.3f pixdiff=%.1f%% speedx=%.2f montage=%s\n",
					r.SSIM, r.PixDiffPct, r.SpeedRatio, r.Montage)
			}
		}
		results = append(results, r)
	}

	if err := writeJSON(*jsonPath, results); err != nil {
		fmt.Fprintf(os.Stderr, "compare: write json: %v\n", err)
		return 1
	}
	if err := writeReport(*reportPath, results, *width, *height, *n, chromeBin); err != nil {
		fmt.Fprintf(os.Stderr, "compare: write report: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "\nwrote %s and %s\n", *jsonPath, *reportPath)
	return 0
}

// benchWebengine renders url with the engine N+warmup times, returning the last
// rendered image and the median timed wall-clock (ms).
func benchWebengine(url string, viewport image.Rectangle, n, warmup int, timeout time.Duration) (*image.RGBA, float64, error) {
	var last *image.RGBA
	render := func() (float64, error) {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		start := time.Now()
		img, _, err := engine.Render(ctx, url, viewport)
		el := time.Since(start)
		if err != nil {
			return 0, err
		}
		last = img
		return float64(el.Microseconds()) / 1000.0, nil
	}
	for i := 0; i < warmup; i++ {
		if _, err := render(); err != nil {
			return nil, 0, err
		}
	}
	var times []float64
	for i := 0; i < n; i++ {
		ms, err := render()
		if err != nil {
			return nil, 0, err
		}
		times = append(times, ms)
	}
	return last, median(times), nil
}

// benchChrome drives headless Chrome over url N+warmup times, returning the last
// full-page screenshot (decoded) and the median timed navigate+screenshot (ms).
func benchChrome(alloc context.Context, url string, w, h, n, warmup int, settle, timeout time.Duration) (*image.RGBA, float64, error) {
	var lastPNG []byte
	render := func() (float64, error) {
		png, ms, err := chromeShot(alloc, url, w, h, settle, timeout)
		if err != nil {
			return 0, err
		}
		lastPNG = png
		return ms, nil
	}
	for i := 0; i < warmup; i++ {
		if _, err := render(); err != nil {
			return nil, 0, err
		}
	}
	var times []float64
	for i := 0; i < n; i++ {
		ms, err := render()
		if err != nil {
			return nil, 0, err
		}
		times = append(times, ms)
	}
	img, err := decodePNG(lastPNG)
	if err != nil {
		return nil, 0, err
	}
	return img, median(times), nil
}

// newChromeAllocator starts one headless Chrome using the given binary at the
// requested window size and device-scale 1.
func newChromeAllocator(chromeBin string, w, h int) (context.Context, context.CancelFunc) {
	opts := append([]chromedp.ExecAllocatorOption{},
		chromedp.DefaultExecAllocatorOptions[:]...)
	opts = append(opts,
		chromedp.ExecPath(chromeBin),
		chromedp.Flag("headless", true),
		chromedp.Flag("hide-scrollbars", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("force-device-scale-factor", "1"),
		chromedp.WindowSize(w, h),
	)
	return chromedp.NewExecAllocator(context.Background(), opts...)
}

// chromeShot navigates a fresh tab to url and captures a full-page PNG at
// device-scale 1, returning the PNG bytes and the timed wall-clock (ms).
func chromeShot(alloc context.Context, url string, w, h int, settle, timeout time.Duration) ([]byte, float64, error) {
	ctx, cancel := chromedp.NewContext(alloc)
	defer cancel()
	tctx, tcancel := context.WithTimeout(ctx, timeout)
	defer tcancel()

	var buf []byte
	start := time.Now()
	err := chromedp.Run(tctx,
		emulation.SetDeviceMetricsOverride(int64(w), int64(h), 1, false),
		network.SetCacheDisabled(true),
		chromedp.Navigate(url),
		chromedp.Sleep(settle),
		fullPageShot(w, &buf),
	)
	el := time.Since(start)
	if err != nil {
		return nil, 0, err
	}
	return buf, float64(el.Microseconds()) / 1000.0, nil
}

// fullPageShot captures the entire page (beyond the viewport) at scale 1,
// clamped to viewport width w so it matches webengine's output column.
func fullPageShot(w int, buf *[]byte) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		_, _, _, _, _, cssContentSize, err := page.GetLayoutMetrics().Do(ctx)
		if err != nil {
			return err
		}
		clipW := cssContentSize.Width
		if clipW > float64(w) {
			clipW = float64(w)
		}
		data, err := page.CaptureScreenshot().
			WithFormat(page.CaptureScreenshotFormatPng).
			WithCaptureBeyondViewport(true).
			WithClip(&page.Viewport{
				X:      0,
				Y:      0,
				Width:  clipW,
				Height: cssContentSize.Height,
				Scale:  1,
			}).Do(ctx)
		if err != nil {
			return err
		}
		*buf = data
		return nil
	})
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	m := len(s) / 2
	if len(s)%2 == 1 {
		return s[m]
	}
	return (s[m-1] + s[m]) / 2
}

func appendNote(existing, add string) string {
	if existing == "" {
		return add
	}
	return existing + "; " + add
}

func readURLs(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var urls []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		urls = append(urls, line)
	}
	return urls, nil
}

func slug(u string) string {
	u = strings.TrimPrefix(u, "https://")
	u = strings.TrimPrefix(u, "http://")
	repl := strings.NewReplacer("/", "_", ":", "_", "?", "_", "&", "_", "=", "_", "#", "_", "(", "", ")", "", ",", "")
	s := repl.Replace(strings.TrimSuffix(u, "/"))
	if len(s) > 80 {
		s = s[:80]
	}
	return s
}

func writeJSON(path string, results []Result) error {
	data, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}
