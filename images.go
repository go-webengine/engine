// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package engine

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"io"
	"net/http"
	"strings"

	goimages "github.com/go-images/images"

	"github.com/go-webengine/engine/css"
	"github.com/go-webengine/engine/dom"
)

// loadImages fetches and decodes every <img> in the document (best-effort),
// returning intrinsic sizes for layout and decoded bitmaps for paint. Images
// wider than the viewport are scaled down proportionally. Failures are skipped.
func (e *Engine) loadImages(ctx context.Context, doc *Document, sm css.StyleMap, viewportW int) (map[*dom.Node][2]float64, map[*dom.Node]image.Image) {
	sizes := map[*dom.Node][2]float64{}
	bitmaps := map[*dom.Node]image.Image{}

	var imgs []*dom.Node
	var walk func(n *dom.Node)
	walk = func(n *dom.Node) {
		if n.Type == dom.Element && n.Tag == "img" {
			if st := sm[n]; st == nil || st.Display != css.DisplayNone {
				imgs = append(imgs, n)
			}
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(doc.Root)

	count := 0
	for _, n := range imgs {
		if count >= e.MaxImages {
			break
		}
		src, ok := n.Attribute("src")
		if !ok {
			continue
		}
		data, ok := e.fetchImageBytes(ctx, doc.URL, src)
		if !ok {
			continue
		}
		src0, err := goimages.Decode(bytes.NewReader(data))
		if err != nil {
			continue
		}
		w, h := src0.Bounds().Dx(), src0.Bounds().Dy()
		if w <= 0 || h <= 0 {
			continue
		}
		// Apply a single-axis CSS width/height as a browser does: the specified
		// axis is used and the other is scaled by the intrinsic aspect ratio (so
		// e.g. a wide logo constrained to height:1.5rem is ~72px wide, not its
		// full intrinsic width). Both axes set uses both; neither keeps intrinsic.
		if cw, ch, ok := cssImageSize(sm[n], w, h); ok {
			if cw != w || ch != h {
				if scaled, err := goimages.Resize(src0, cw, ch, goimages.Bilinear); err == nil {
					src0 = scaled
					w, h = cw, ch
				}
			}
		}
		// Scale down to fit the viewport width, preserving aspect ratio.
		if w > viewportW && viewportW > 0 {
			nh := int(float64(h) * float64(viewportW) / float64(w))
			if nh < 1 {
				nh = 1
			}
			if scaled, err := goimages.Resize(src0, viewportW, nh, goimages.Bilinear); err == nil {
				src0 = scaled
				w, h = viewportW, nh
			}
		}
		sizes[n] = [2]float64{float64(w), float64(h)}
		bitmaps[n] = src0
		count++
	}
	return sizes, bitmaps
}

// cssImageSize resolves the used pixel dimensions of an image given its style
// and intrinsic size (iw×ih). A definite (non-auto, non-percentage) CSS width
// and/or height override the intrinsic size; when only one axis is definite the
// other is derived from the intrinsic aspect ratio. It reports false when the
// style is nil or specifies neither axis definitely (keep the intrinsic size).
func cssImageSize(st *css.Style, iw, ih int) (w, h int, ok bool) {
	if st == nil || iw <= 0 || ih <= 0 {
		return 0, 0, false
	}
	definite := func(l css.Length) (float64, bool) {
		if l.Auto || l.IsPercent || l.Px <= 0 {
			return 0, false
		}
		return l.Px, true
	}
	cw, hasW := definite(st.Width)
	ch, hasH := definite(st.Height)
	switch {
	case hasW && hasH:
		return iround(cw), iround(ch), true
	case hasW:
		return iround(cw), iround(cw * float64(ih) / float64(iw)), true
	case hasH:
		return iround(ch * float64(iw) / float64(ih)), iround(ch), true
	default:
		return 0, 0, false
	}
}

// iround rounds a non-negative float to the nearest int (>= 1).
func iround(f float64) int {
	n := int(f + 0.5)
	if n < 1 {
		n = 1
	}
	return n
}

// fetchImageBytes returns the raw bytes for an image src, handling data: URIs
// and absolute/relative http(s) URLs.
func (e *Engine) fetchImageBytes(ctx context.Context, base, src string) ([]byte, bool) {
	src = strings.TrimSpace(src)
	if strings.HasPrefix(src, "data:") {
		return decodeDataURI(src)
	}
	abs, ok := resolveURL(base, src)
	if !ok || !(strings.HasPrefix(abs, "http://") || strings.HasPrefix(abs, "https://")) {
		return nil, false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, abs, nil)
	if err != nil {
		return nil, false
	}
	req.Header.Set("User-Agent", e.UserAgent)
	resp, err := e.Client.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, false
	}
	return data, true
}

// decodeDataURI decodes a base64 data: URI's payload.
func decodeDataURI(s string) ([]byte, bool) {
	comma := strings.IndexByte(s, ',')
	if comma < 0 {
		return nil, false
	}
	meta, payload := s[5:comma], s[comma+1:]
	if strings.Contains(meta, "base64") {
		b, err := base64.StdEncoding.DecodeString(payload)
		if err != nil {
			return nil, false
		}
		return b, true
	}
	return []byte(payload), true
}
