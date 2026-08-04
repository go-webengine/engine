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

func TestRunFileOffline(t *testing.T) {
	dir := t.TempDir()
	htmlPath := filepath.Join(dir, "in.html")
	if err := os.WriteFile(htmlPath, []byte(
		`<html><head><title>T</title></head><body><div style="display:flex">`+
			`<div style="width:40px">A</div><div style="width:40px">B</div></div></body></html>`), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "o.png")
	if code := run([]string{"-file", htmlPath, "-out", out, "-w", "200", "-h", "100"}, os.Stderr); code != 0 {
		t.Fatalf("run -file: code = %d, want 0", code)
	}
	if fi, err := os.Stat(out); err != nil || fi.Size() == 0 {
		t.Fatalf("output png = %v %v", fi, err)
	}
}

func TestRunFileMissing(t *testing.T) {
	code := run([]string{"-file", filepath.Join(t.TempDir(), "nope.html"), "-out",
		filepath.Join(t.TempDir(), "o.png")}, os.Stderr)
	if code != 1 {
		t.Errorf("missing file: code = %d, want 1", code)
	}
}

func TestRunNeitherURLNorFile(t *testing.T) {
	if code := run([]string{"-w", "100"}, os.Stderr); code != 2 {
		t.Errorf("no url/file: code = %d, want 2", code)
	}
}
