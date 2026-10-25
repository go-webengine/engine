// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

//go:build webengine_fontsext

package engine

import (
	"os"
	"testing"

	"github.com/go-webengine/engine/paint"
	"github.com/go-webengine/engine/paint/bundledfonts"
)

// Built with webengine_fontsext, the engine's renders need a host's source
// for the bundled faces: the tests give them the files a host serves.
func TestMain(m *testing.M) {
	paint.SetFontSource(bundledfonts.Source)
	os.Exit(m.Run())
}
