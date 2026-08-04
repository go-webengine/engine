# bench — webengine vs headless Chromium

A repeatable harness that benchmarks the pure-Go **webengine** renderer against
**real headless Chromium** on both **fidelity** (SSIM + pixel-diff over the
common render region) and **speed** (median wall-clock over N runs). Re-run it at
the end of every phase to track the gap closing.

## Why a separate module

`bench/` is its **own Go module** (`github.com/go-webengine/engine/bench`) with a
local `replace github.com/go-webengine/engine => ..`. Its Chrome driver,
[`chromedp`](https://github.com/chromedp/chromedp), is pure-Go (it speaks the
Chrome DevTools Protocol to an external Chrome binary — no cgo in the library),
but it is still a heavy dependency we do **not** want in the engine module's
CGO=0, six-arch CI or coverage gate. Because `bench/` is a nested module, the
engine's `go build ./...` / `go test ./...` from the repo root do **not** see it.

## Requirements

- Go 1.26.4+
- A Chrome / Chromium binary. Point `CHROME_BIN` at it; default is
  `/Applications/Google Chrome.app/Contents/MacOS/Google Chrome`.

## Run

```sh
cd bench
CHROME_BIN="/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" \
  go run ./cmd/compare -urls urls.txt
```

Outputs (paths are relative to the working directory, defaults shown):

- `results.json` — per-URL `{url, webengine_ms, chrome_ms, speed_ratio, ssim, pixdiff_pct, …}`
- `REPORT.md` — the run header + fidelity/speed table + montage links. The
  hand-written analysis below the `BEGIN ANALYSIS` marker is **preserved** across
  re-runs; only the auto-generated table is overwritten.
- `out/<slug>.png` — a side-by-side montage per URL: **webengine | chrome | diff-heatmap**
  (white = identical, deepening red = larger per-pixel delta).

## Flags

| flag | default | meaning |
|------|---------|---------|
| `-urls` | `urls.txt` | URL list (one per line, `#` comments allowed) |
| `-out` | `out` | montage output directory |
| `-report` | `REPORT.md` | Markdown report path |
| `-json` | `results.json` | JSON results path |
| `-n` | `5` | timed runs per engine per URL |
| `-warmup` | `1` | untimed warmup runs |
| `-width` / `-height` | `1024` / `768` | viewport (device-scale 1) |
| `-settle` | `900` | ms to let Chrome settle before the screenshot |
| `-timeout` | `45` | per-render timeout (s) |
| `-pixthresh` | `16` | per-channel Δ threshold (0-255) for pixdiff |
| `-maxheight` | `2500` | cap the common fidelity/montage region to N top rows (0 = full page) |

## How the metrics are computed

Both engines produce a **full-page** screenshot at 1024px width, device-scale 1.
Because the two engines lay pages out differently they reach different total
heights, so metrics are computed over the **common top-left region** (min width ×
min height). `metrics` (pure Go, std-lib + go-images) then computes:

- **SSIM** — windowed (8px window, step 4) over the Rec.601 luminance plane, mean
  over all windows, standard 8-bit constants `C1=(0.01·255)²`, `C2=(0.03·255)²`.
  `1.0` == identical.
- **pixdiff %** — fraction of pixels whose maximum per-channel absolute
  difference exceeds the threshold (default 16/255).

Timings include the network fetch (both engines fetch fresh each run; Chrome's
HTTP cache is disabled for fairness), so absolute numbers vary with the network —
the **speed ratio** and the **fidelity** numbers are the stable signal.
