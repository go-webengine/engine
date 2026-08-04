// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

// Command render fetches a URL and writes a PNG screenshot of the statically
// rendered page.
//
//	render -url https://example.com/ -out shot.png -w 1024 -h 768
package main

import (
	"context"
	"flag"
	"fmt"
	"image"
	"os"
	"time"

	"github.com/go-webengine/engine"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

func run(args []string, stderr *os.File) int {
	fs := flag.NewFlagSet("render", flag.ContinueOnError)
	fs.SetOutput(stderr)
	url := fs.String("url", "", "URL to render (required)")
	out := fs.String("out", "out.png", "output PNG path")
	w := fs.Int("w", 1024, "viewport width")
	h := fs.Int("h", 768, "viewport height")
	timeout := fs.Duration("timeout", 45*time.Second, "overall timeout")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *url == "" {
		fmt.Fprintln(stderr, "render: -url is required")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	e := engine.New()
	img, info, err := e.Render(ctx, *url, image.Rect(0, 0, *w, *h))
	if err != nil {
		fmt.Fprintf(stderr, "render: %v\n", err)
		return 1
	}
	png, err := engine.EncodePNG(img)
	if err != nil {
		fmt.Fprintf(stderr, "render: encode: %v\n", err)
		return 1
	}
	if err := os.WriteFile(*out, png, 0o644); err != nil {
		fmt.Fprintf(stderr, "render: write: %v\n", err)
		return 1
	}
	fmt.Fprintf(stderr, "rendered %q -> %s (%dx%d, title=%q, contentHeight=%d)\n",
		info.URL, *out, img.Rect.Dx(), img.Rect.Dy(), info.Title, info.ContentHeight)
	return 0
}
