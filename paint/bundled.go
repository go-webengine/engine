// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package paint

import (
	"fmt"
	"sync"

	"github.com/go-opentype/opentype"
)

// The files of the bundled families, by go-opentype/fonts' package and file
// name: what SetFontSource is asked for.
const (
	fileInter           = "inter/inter.ttf"
	fileInterBold       = "inter/inter-bold.ttf"
	fileInterItalic     = "inter/inter-italic.ttf"
	fileInterBoldItalic = "inter/inter-bolditalic.ttf"
	fileLora            = "lora/lora.ttf"
	fileLoraBold        = "lora/lora-bold.ttf"
	fileLoraItalic      = "lora/lora-italic.ttf"
	fileLoraBoldItalic  = "lora/lora-bolditalic.ttf"
	fileGoMono          = "gomono/gomono.ttf"
	fileDejaVu          = "dejavusans/dejavusans.ttf"
	fileDejaVuBold      = "dejavusans/dejavusans-bold.ttf"
	fileDejaVuItalic    = "dejavusans/dejavusans-italic.ttf"
	fileDejaVuBoldIt    = "dejavusans/dejavusans-bolditalic.ttf"
)

// BundledFontNames are the files of the bundled families (Inter, Lora, Go
// Mono, and DejaVu Sans for the characters they lack), named as
// go-opentype/fonts names them: "inter/inter-bold.ttf". A host that builds
// with the webengine_fontsext tag serves these files and hands them over
// through SetFontSource.
func BundledFontNames() []string {
	return []string{
		fileInter, fileInterBold, fileInterItalic, fileInterBoldItalic,
		fileLora, fileLoraBold, fileLoraItalic, fileLoraBoldItalic,
		fileGoMono,
		fileDejaVu, fileDejaVuBold, fileDejaVuItalic, fileDejaVuBoldIt,
	}
}

var (
	sourceMu sync.Mutex
	source   func(name string) ([]byte, error)
)

// SetFontSource sets where the bundled faces' files come from: src is asked
// for one of BundledFontNames the first time a render needs that face, and
// at most once. A build with the webengine_fontsext tag embeds none of them
// and must set it before the first render: a host in a browser fetches them
// from its own origin then, so that a document set in Inter Regular never
// downloads the twelve other faces. Without the tag the embedded files
// answer, and SetFontSource replaces them.
func SetFontSource(src func(name string) ([]byte, error)) {
	sourceMu.Lock()
	defer sourceMu.Unlock()
	source = src
}

// fontSource is the source set (nil for none).
func fontSource() func(name string) ([]byte, error) {
	sourceMu.Lock()
	defer sourceMu.Unlock()
	return source
}

// bundledBytes is the file of a bundled face: the source set, else the
// embedded file (none with webengine_fontsext).
func bundledBytes(name string) ([]byte, error) {
	if src := fontSource(); src != nil {
		return src(name)
	}
	if b := embeddedFont(name); b != nil {
		return b, nil
	}
	return nil, fmt.Errorf("paint: no source for the bundled font %s (built with webengine_fontsext: call SetFontSource)", name)
}

// lazyFont is a face parsed the first time it is asked for: a bundled one
// from its file, a registered one already parsed or from its loader.
type lazyFont struct {
	once sync.Once
	load func() (*opentype.Font, error)
	font *opentype.Font
	err  error
}

func parsedLazy(f *opentype.Font) *lazyFont {
	l := &lazyFont{font: f}
	l.once.Do(func() {})
	return l
}

// get is the parsed font, nil for a nil lazyFont or one that failed to load.
func (l *lazyFont) get() *opentype.Font {
	if l == nil {
		return nil
	}
	l.once.Do(func() { l.font, l.err = l.load() })
	return l.font
}

// bundledFace is the lazy bundled face of one file. A bundled file that does
// not parse is a defect of the fonts module (or of a host's source): it
// panics with the file's name, as the eager parse did.
func bundledFace(name string) *lazyFont {
	return &lazyFont{load: func() (*opentype.Font, error) {
		b, err := bundledBytes(name)
		if err != nil {
			panic(err.Error())
		}
		f, err := opentype.Parse(b)
		if err != nil {
			panic("paint: bundled font " + name + " failed to parse: " + err.Error())
		}
		return f, nil
	}}
}
