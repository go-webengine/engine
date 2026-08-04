// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package layout

import (
	"strings"
	"testing"
)

// word builds an item with a fixed width and a 1-unit space before.
func word(text string, w float64) *InlineItem {
	return &InlineItem{Text: text, Width: w, SpaceBefore: 1}
}

// lineText joins the words of a line for compact assertions.
func lineText(l *LineBox) string {
	var parts []string
	for _, it := range l.Items {
		parts = append(parts, it.Text)
	}
	return strings.Join(parts, " ")
}

func TestWrapItemsEmpty(t *testing.T) {
	if got := WrapItems(nil, 100); got != nil {
		t.Errorf("empty = %v", got)
	}
}

func TestWrapItemsGreedy(t *testing.T) {
	// widths: a=10 b=10 c=10 d=10, space=1. maxW=22.
	// line1: a(10) + " "+b(1+10)=21 <=22; +" "+c(1+10)=32 >22 → break.
	// line2: c(10) + " "+d(11)=21 <=22.
	items := []*InlineItem{word("a", 10), word("b", 10), word("c", 10), word("d", 10)}
	lines := WrapItems(items, 22)
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}
	if lineText(lines[0]) != "a b" {
		t.Errorf("line0 = %q", lineText(lines[0]))
	}
	if lineText(lines[1]) != "c d" {
		t.Errorf("line1 = %q", lineText(lines[1]))
	}
}

func TestWrapItemsExactBoundary(t *testing.T) {
	// Exactly filling maxW must NOT overflow to a new line.
	items := []*InlineItem{word("a", 10), word("b", 11)} // 10 + 1 + 11 = 22
	lines := WrapItems(items, 22)
	if len(lines) != 1 {
		t.Fatalf("expected 1 line at exact fit, got %d", len(lines))
	}
}

func TestWrapItemsOverflowWord(t *testing.T) {
	// A single word wider than maxW sits alone; the next word starts a new line.
	items := []*InlineItem{word("huge", 100), word("x", 5)}
	lines := WrapItems(items, 20)
	if len(lines) != 2 || lineText(lines[0]) != "huge" || lineText(lines[1]) != "x" {
		t.Fatalf("overflow lines = %d %q/%q", len(lines), lineText(lines[0]), lineText(lines[1]))
	}
}

func TestWrapItemsLineBreak(t *testing.T) {
	// A LineBreak ends the current line even when space remains, and an empty
	// line is produced for a double break.
	items := []*InlineItem{
		word("a", 5),
		{LineBreak: true},
		{LineBreak: true},
		word("b", 5),
	}
	lines := WrapItems(items, 1000)
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(lines))
	}
	if lineText(lines[0]) != "a" || len(lines[1].Items) != 0 || lineText(lines[2]) != "b" {
		t.Errorf("break lines = %q / %d / %q", lineText(lines[0]), len(lines[1].Items), lineText(lines[2]))
	}
}
