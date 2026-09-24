// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package format

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestStripControlChars_UnsafeRunes_RemovedTabAndNewlineKept verifies the
// removal variant of the FR-17.5 control-character rule (MTIX-100.1).
func TestStripControlChars_UnsafeRunes_RemovedTabAndNewlineKept(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"plain text unchanged", "PASS: all good", "PASS: all good"},
		{"tab and newline kept", "a\tb\nc", "a\tb\nc"},
		{"escape removed", "x\x1b[31my", "x[31my"},
		{"carriage return removed", "FAIL\r PASS", "FAIL PASS"},
		{"NUL, BEL and DEL removed", "a\x00b\x07c\x7fd", "abcd"},
		{"C1 controls removed", "a\u0085b\u009bc", "abc"},
		{"non-ASCII letters kept", "é中ü", "é中ü"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, StripControlChars(tt.in))
		})
	}
}

// TestSanitizeControlChars_SameRunesAsStrip_ReplacesInsteadOfRemoving pins
// that both functions judge the same runes unsafe; only the treatment
// differs (replacement for briefings, removal for show).
func TestSanitizeControlChars_SameRunesAsStrip_ReplacesInsteadOfRemoving(t *testing.T) {
	in := "a\tb\nc\x1bd\re\x7ff\u009bg"
	assert.Equal(t, "a\tb\nc�d�e�f�g", sanitizeControlChars(in))
	assert.Equal(t, "a\tb\ncdefg", StripControlChars(in))
}
