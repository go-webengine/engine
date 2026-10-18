// Copyright (c) the go-webengine authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package engine

import (
	"container/list"
	"context"
	"crypto/sha256"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/go-opentype/opentype"
	"github.com/go-webengine/engine/css"
	"github.com/go-webengine/engine/paint"
)

// maxFontBytes caps one font file. A real face is tens to a few hundred
// kilobytes; anything past this is not a font we are going to typeset with,
// and the cap is what stops a hostile or broken URL from reading a whole
// disk image into memory.
const maxFontBytes = 8 << 20

// LoadedFontFace is one @font-face rule whose file was fetched and accepted:
// the slot it fills and the SFNT bytes that fill it.
//
// Data is what a consumer needs beyond measurement — a PDF exporter has to
// EMBED the same bytes the layout measured against, or the file it writes
// names a font it does not carry.
type LoadedFontFace struct {
	Family string // lowercased, as css.FontFace gives it
	Weight int
	Italic bool
	Data   []byte // SFNT bytes, ready for opentype.Parse
	URL    string // where they came from, resolved

	// font is Data parsed, when LoadFontFaces parsed it, so that
	// RegisterFontFaces does not parse it again.
	font *opentype.Font
}

// LoadFontFaces fetches the files the document's own @font-face rules name and
// returns the faces that could be used, in the order the rules declared them.
//
// sheets are the stylesheet sources LoadStylesheets returned, plus the
// document's own inline CSS; a document's fonts live in whichever of them
// carries the @font-face rule, most often a <link> to a font service.
//
// Without this, a page could only ever be typeset in the three families the
// paint package bundles, whatever it asked for — so a document written for a
// named typeface was rendered in a substitute AT DIFFERENT METRICS, and its
// lines broke somewhere its author never saw. Registering the faces it names
// (RegisterFontFaces) is what makes the measurement its own.
//
// A rule whose every src is a format this build cannot decode is skipped, and
// the page keeps the bundled family for it: opentype.Parse reads SFNT
// (TrueType and CFF/OpenType), so a WOFF or WOFF2 src needs a decoder in
// go-opentype to become reachable. Fetch failures, oversized bodies and
// unparseable files are skipped the same way — a missing font degrades the
// typeface, it does not fail the render.
//
// Slots load concurrently: each @font-face is one network round trip, and a
// page naming ten faces otherwise waits for all ten in turn (tailwindcss.com,
// round 151: ~4.4s of serial font downloads). Within a slot, every candidate
// rule ALSO fetches concurrently (round 168) rather than one at a time: this
// engine has no unicode-range model (css.FontFace carries no such field), so
// several genuinely distinct @font-face rules that a real browser would pick
// among BY CODEPOINT collapse into one slot here, as redundant-looking
// alternatives of each other — confirmed live on cs.cmu.edu's Font Awesome
// v4-compatibility shim, which declares five separate 'FontAwesome'-family,
// weight-400 rules (solid/brands/regular/compat, split only by unicode-range)
// that all land in ONE slot. Trying them one at a time cost ~52s when the
// origin's /webfonts/ path was erroring (each failed attempt still waits out
// the full ~6.4s server response): a page need not be malicious or even
// unusual for a single slot to carry several slow-to-fail candidates. The
// result keeps declaration order regardless of which candidate's fetch
// finishes first — same winner as the old sequential loop, just not
// waiting for each loser to fail before starting the next.
func (e *Engine) LoadFontFaces(ctx context.Context, doc *Document, sheets []string, m css.Media) []LoadedFontFace {
	type candidate struct {
		idx  int
		face css.FontFace
	}
	var slots []string
	bySlot := map[string][]candidate{}
	idx := 0
	for _, src := range sheets {
		for _, face := range css.ParseFontFaces(src, m) {
			slot := face.Family + "/" + boolKey(face.Italic) + itoa(face.Weight)
			if _, ok := bySlot[slot]; !ok {
				slots = append(slots, slot)
			}
			bySlot[slot] = append(bySlot[slot], candidate{idx, face})
			idx++
		}
	}
	type loaded struct {
		idx int
		lf  LoadedFontFace
	}
	results := make([]loaded, len(slots))
	found := make([]bool, len(slots))
	var wg sync.WaitGroup
	for i, slot := range slots {
		wg.Add(1)
		go func(i int, cands []candidate) {
			defer wg.Done()
			// Every DISTINCT candidate in this slot fetches concurrently (not
			// one at a time, stopping only at a success) — see this
			// function's own doc comment for why a slot can hold several
			// genuinely distinct, slow-to-fail rules rather than true format
			// alternatives of one file. A rule repeated verbatim (the same
			// src list parsed from two stylesheets, or the same stylesheet
			// named twice) still fetches only once: deduped by its own src
			// key below, same as the old sequential loop's incidental effect
			// of never re-fetching a slot's later identical entry once an
			// earlier one had already resolved it. Declaration order still
			// decides the winner among distinct candidates: sorted by idx
			// below, so which fetch happens to FINISH first never matters.
			type attempt struct {
				idx int
				lf  LoadedFontFace
				ok  bool
			}
			firstIdx := map[string]int{} // src key -> that key's own goroutine slot in attempts
			var unique []candidate
			for _, c := range cands {
				key := srcsKey(c.face.Srcs)
				if _, dup := firstIdx[key]; dup {
					continue
				}
				firstIdx[key] = len(unique)
				unique = append(unique, c)
			}
			attempts := make([]attempt, len(unique))
			var cwg sync.WaitGroup
			for j, c := range unique {
				cwg.Add(1)
				go func(j int, c candidate) {
					defer cwg.Done()
					lf, ok := e.loadOneFontFace(ctx, doc, c.face)
					attempts[j] = attempt{c.idx, lf, ok}
				}(j, c)
			}
			cwg.Wait()
			sort.Slice(attempts, func(a, b int) bool { return attempts[a].idx < attempts[b].idx })
			for _, a := range attempts {
				if a.ok {
					results[i] = loaded{a.idx, a.lf}
					found[i] = true
					return
				}
			}
		}(i, bySlot[slot])
	}
	wg.Wait()
	var order []loaded
	for i := range results {
		if found[i] {
			order = append(order, results[i])
		}
	}
	sort.Slice(order, func(a, b int) bool { return order[a].idx < order[b].idx })
	out := make([]LoadedFontFace, 0, len(order))
	for _, r := range order {
		out = append(out, r.lf)
	}
	return out
}

// loadOneFontFace tries a rule's src entries in order and returns the first
// that yields bytes this build can parse.
func (e *Engine) loadOneFontFace(ctx context.Context, doc *Document, face css.FontFace) (LoadedFontFace, bool) {
	for _, s := range face.Srcs {
		if !fontFormatSupported(s) {
			continue
		}
		data, ok := e.fetchFontBytes(ctx, doc.URL, s.URL)
		if !ok {
			continue
		}
		sfnt, ok := decodeFontFile(data)
		if !ok {
			continue
		}
		// Parse it here, not just at registration: this function's contract is
		// the faces that CAN be used, and a consumer beyond measurement — a
		// PDF exporter embedding these very bytes — would otherwise be handed
		// a file that is not a font at all. A data: URI carrying the wrong
		// payload is the case that showed it.
		ft, err := parsedFonts.parse(sfnt)
		if err != nil {
			continue
		}
		abs, _ := resolveURL(doc.URL, s.URL)
		return LoadedFontFace{Family: face.Family, Weight: face.Weight, Italic: face.Italic, Data: sfnt, URL: abs, font: ft}, true
	}
	return LoadedFontFace{}, false
}

// fontFormatSupported reports whether a src entry's format() hint names
// something the font parser can read. An entry with no hint is tried anyway —
// the hint is optional, and the bytes themselves are the authority.
//
// woff2 is the one that matters: it is what a font service serves a browser,
// and this engine sends a browser's User-Agent, so it is what a font service
// serves us. go-opentype reads both containers now (go-opentype/opentype#43).
func fontFormatSupported(s css.FontSrc) bool {
	switch s.Format {
	case "", "truetype", "opentype", "sfnt", "woff", "woff2",
		"truetype-variations", "opentype-variations":
		return true
	}
	return false
}

// fetchFontBytes fetches one font file, resolved against the document. It
// mirrors the image fetch — same rate-limit retry, same User-Agent — but has
// no cache of its own and does not touch the image caches: a font is fetched
// once per render at most (LoadFontFaces takes the first rule per slot), and
// putting fonts in the image cache would make it answer for two kinds of
// resource keyed only by URL.
//
// A data: URI is read inline, which is how a self-contained document — one
// built to be printed with no network — carries its own typefaces.
func (e *Engine) fetchFontBytes(ctx context.Context, base, src string) ([]byte, bool) {
	src = strings.TrimSpace(src)
	if strings.HasPrefix(src, "data:") {
		return decodeDataURI(src)
	}
	abs, ok := resolveURL(base, src)
	if ok && isFileURL(abs) {
		return e.readFileURL(abs, maxFontBytes) // only under Engine.FileRoot
	}
	if !ok || !(strings.HasPrefix(abs, "http://") || strings.HasPrefix(abs, "https://")) {
		return nil, false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, abs, nil)
	if err != nil {
		return nil, false
	}
	req.Header.Set("User-Agent", e.UserAgent)
	resp, ok := e.doWithRateLimitRetry(ctx, req)
	if !ok {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxFontBytes))
	if err != nil {
		return nil, false
	}
	return data, true
}

// decodeFontFile is the gate a fetched font file passes before anyone tries to
// parse it. It used to refuse a WOFF or WOFF2 wrapper, which meant refusing
// every font a font service serves; go-opentype unwraps both containers
// itself now (go-opentype/opentype#43), so there is nothing left to do here
// but reject a file too short to have a signature at all.
//
// It does not validate the file; loadOneFontFace parses it.
func decodeFontFile(data []byte) ([]byte, bool) {
	if len(data) < 4 {
		return nil, false
	}
	return data, true
}

// RegisterFontFaces registers loaded faces with a Fonts registry, so both the
// measurement and whatever draws afterwards use them. A face that fails to
// parse is skipped and named in the returned count's shortfall rather than
// failing the render.
func RegisterFontFaces(fonts *paint.Fonts, faces []LoadedFontFace) int {
	n := 0
	for _, f := range faces {
		ft := f.font
		if ft == nil {
			var err error
			if ft, err = parsedFonts.parse(f.Data); err != nil {
				continue
			}
		}
		if fonts.RegisterFont(f.Family, f.Weight, f.Italic, ft) == nil {
			n++
		}
	}
	return n
}

// ParseFontFace parses a font file (SFNT, WOFF or WOFF2) into a face for
// Engine.FontFaces. A face built this way is parsed once, here; one built
// as a bare struct literal is parsed on first use and then found in the
// cache of parsed fonts by the hash of its bytes, on every render.
func ParseFontFace(family string, weight int, italic bool, data []byte) (LoadedFontFace, error) {
	ft, err := parsedFonts.parse(data)
	if err != nil {
		return LoadedFontFace{}, err
	}
	return LoadedFontFace{Family: family, Weight: weight, Italic: italic, Data: data, font: ft}, nil
}

// registerHostFaces registers the Engine's own FontFaces for the families
// the document did not bring a face for: a page's @font-face wins over an
// installed font of the same name, as in a browser.
func registerHostFaces(fonts *paint.Fonts, faces []LoadedFontFace) {
	var fill []LoadedFontFace
	for _, f := range faces {
		if !fonts.Registered(f.Family) {
			fill = append(fill, f)
		}
	}
	RegisterFontFaces(fonts, fill)
}

// parsedFonts keeps the fonts @font-face rules brought, parsed, across
// renders. Parsing a font is the dearest step of loading it — a slide deck
// rendered page by page, or a site's pages sharing its web fonts, asked for
// the same files on every render and parsed each of them twice (once to
// validate, once to register). A parsed font is read-only, so one copy
// serves every render, concurrently.
//
// It is keyed by the SHA-256 of the file's bytes, not by URL: a key can
// only ever name the bytes it was parsed from, whoever serves them and
// whatever caching headers say. It is bounded, least recently used out
// first, by count and by the bytes of the files, so that a process
// rendering arbitrary pages holds at most fontCacheBytes of files (and
// their parsed tables) however many distinct fonts it meets.
var parsedFonts = newFontCache(fontCacheEntries, fontCacheBytes)

const (
	fontCacheEntries = 64
	fontCacheBytes   = 32 << 20
)

type fontCache struct {
	mu         sync.Mutex
	maxEntries int
	maxBytes   int
	bytes      int
	byKey      map[[sha256.Size]byte]*list.Element
	lru        *list.List // of *fontCacheEntry, most recent first
}

type fontCacheEntry struct {
	key  [sha256.Size]byte
	size int
	font *opentype.Font
}

func newFontCache(maxEntries, maxBytes int) *fontCache {
	return &fontCache{maxEntries: maxEntries, maxBytes: maxBytes, byKey: map[[sha256.Size]byte]*list.Element{}, lru: list.New()}
}

// parse returns sfnt parsed, from the cache when these bytes were parsed
// before. Two renders missing at once both parse; the second to finish
// finds the first's entry and keeps it.
func (c *fontCache) parse(sfnt []byte) (*opentype.Font, error) {
	key := sha256.Sum256(sfnt)
	c.mu.Lock()
	if el, ok := c.byKey[key]; ok {
		c.lru.MoveToFront(el)
		ft := el.Value.(*fontCacheEntry).font
		c.mu.Unlock()
		return ft, nil
	}
	c.mu.Unlock()
	ft, err := opentype.Parse(sfnt)
	if err != nil {
		return nil, err // a file that does not parse is not kept: it costs nothing to refuse again
	}
	if len(sfnt) > c.maxBytes {
		return ft, nil // larger than the whole cache: used, not kept
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.byKey[key]; ok {
		c.lru.MoveToFront(el)
		return el.Value.(*fontCacheEntry).font, nil
	}
	c.byKey[key] = c.lru.PushFront(&fontCacheEntry{key: key, size: len(sfnt), font: ft})
	c.bytes += len(sfnt)
	for c.lru.Len() > c.maxEntries || c.bytes > c.maxBytes {
		old := c.lru.Remove(c.lru.Back()).(*fontCacheEntry)
		delete(c.byKey, old.key)
		c.bytes -= old.size
	}
	return ft, nil
}

// srcsKey identifies a FontFace by its own src list — two rules with the same
// URLs and format hints, in the same order, are the same fetch regardless of
// how many times the same rule was parsed (the same stylesheet named twice,
// or the same rule appearing in two separate sheets), so LoadFontFaces's
// per-slot dedup key is this, not the whole FontFace value.
func srcsKey(srcs []css.FontSrc) string {
	var b strings.Builder
	for i, s := range srcs {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(s.URL)
		b.WriteByte('\x00')
		b.WriteString(s.Format)
	}
	return b.String()
}

func boolKey(b bool) string {
	if b {
		return "i"
	}
	return "n"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	for n > 0 && i > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
