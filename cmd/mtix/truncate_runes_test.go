// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTruncate_MultibyteTitle_CutsOnRune proves Truncate counts and cuts on
// runes, so a multibyte title is never split into invalid UTF-8 (MTIX-187.7.4).
func TestTruncate_MultibyteTitle_CutsOnRune(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		max    int
		want   string
		wantOK bool // result must be valid UTF-8
	}{
		{"ascii short unchanged", "short", 10, "short", true},
		{"ascii exact fit unchanged", "abcdefghij", 10, "abcdefghij", true},
		{"ascii cut", "abcdefghijk", 10, "abcdefg...", true},
		{"cjk fits by runes though long in bytes", "\u65e5\u672c\u8a9e\u65e5\u672c", 5, "\u65e5\u672c\u8a9e\u65e5\u672c", true},
		{"cjk cut on rune", "\u65e5\u672c\u8a9e\u65e5\u672c\u8a9e\u65e5\u672c\u8a9e", 6, "\u65e5\u672c\u8a9e...", true},
		{"accent cut on rune", strings.Repeat("\u00e9", 12), 8, strings.Repeat("\u00e9", 5) + "...", true},
		{"emoji cut on rune", strings.Repeat("\U0001F642", 10), 7, strings.Repeat("\U0001F642", 4) + "...", true},
		{"max at or below 3 returns input", "abcdef", 3, "abcdef", true},
		{"empty", "", 10, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Truncate(tt.in, tt.max)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantOK, utf8.ValidString(got))
		})
	}
}

// TestWriteTable_MultibyteCells_AlignByRuneCount proves column widths count
// runes, so a CJK or accented title does not skew the columns.
func TestWriteTable_MultibyteCells_AlignByRuneCount(t *testing.T) {
	var buf bytes.Buffer
	w := &humanWriter{w: &buf}
	w.WriteTable([]string{"ID", "Title"}, [][]string{
		{"A-1", "\u65e5\u672c\u8a9e"},
		{"B-22", "plain"},
	})
	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	require.Len(t, lines, 4)
	// Every line pads the first column to the same rune width.
	for _, ln := range lines {
		assert.GreaterOrEqual(t, utf8.RuneCountInString(ln), 4+2)
	}
	assert.Equal(t, utf8.RuneCountInString(strings.TrimRight(lines[2], " ")),
		4+2+3, "cjk row is 4 wide id column, 2 gap, 3 runes")
	// Widest title is "plain" (5 runes); id column is 4, gap 2.
	assert.Equal(t, 4+2+5, utf8.RuneCountInString(lines[1]),
		"separator width counts runes, not bytes")
}
