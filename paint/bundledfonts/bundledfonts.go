// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

// Package bundledfonts embeds the files of paint's bundled families (Inter,
// Lora, Go Mono, DejaVu Sans), from go-opentype/fonts, by the names
// paint.BundledFontNames gives. paint embeds them through it unless built
// with webengine_fontsext; a host built with that tag serves these same
// files (a native tool writes them out with Data), and its tests hand them
// over with paint.SetFontSource(bundledfonts.Source).
package bundledfonts

import (
	"fmt"

	"github.com/go-opentype/fonts/dejavusans"
	"github.com/go-opentype/fonts/gomono"
	"github.com/go-opentype/fonts/inter"
	"github.com/go-opentype/fonts/lora"
)

var files = map[string][]byte{
	"inter/inter.ttf":                      inter.TTF,
	"inter/inter-bold.ttf":                 inter.BoldTTF,
	"inter/inter-italic.ttf":               inter.ItalicTTF,
	"inter/inter-bolditalic.ttf":           inter.BoldItalicTTF,
	"lora/lora.ttf":                        lora.TTF,
	"lora/lora-bold.ttf":                   lora.BoldTTF,
	"lora/lora-italic.ttf":                 lora.ItalicTTF,
	"lora/lora-bolditalic.ttf":             lora.BoldItalicTTF,
	"gomono/gomono.ttf":                    gomono.TTF,
	"dejavusans/dejavusans.ttf":            dejavusans.TTF,
	"dejavusans/dejavusans-bold.ttf":       dejavusans.BoldTTF,
	"dejavusans/dejavusans-italic.ttf":     dejavusans.ItalicTTF,
	"dejavusans/dejavusans-bolditalic.ttf": dejavusans.BoldItalicTTF,
}

// Data is the embedded file of a bundled face by its name, nil for another
// name.
func Data(name string) []byte { return files[name] }

// Source is Data as a paint.SetFontSource source.
func Source(name string) ([]byte, error) {
	if b := files[name]; b != nil {
		return b, nil
	}
	return nil, fmt.Errorf("bundledfonts: no file %s", name)
}
