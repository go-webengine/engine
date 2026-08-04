// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRunMissingURL(t *testing.T) {
	if code := run([]string{}, os.Stderr); code != 2 {
		t.Errorf("missing url: code = %d, want 2", code)
	}
}

func TestRunBadFlag(t *testing.T) {
	if code := run([]string{"-nope"}, os.Stderr); code != 2 {
		t.Errorf("bad flag: code = %d, want 2", code)
	}
}

func TestRunUnreachable(t *testing.T) {
	// A malformed URL fails fast in the render path, returning exit code 1.
	code := run([]string{"-url", "http://nonexistent.invalid.", "-out",
		filepath.Join(t.TempDir(), "o.png"), "-timeout", "3s"}, os.Stderr)
	if code != 1 {
		t.Skipf("expected render failure (code 1), got %d (resolver may have answered)", code)
	}
}
