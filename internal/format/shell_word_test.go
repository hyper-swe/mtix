// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package format

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestShellWord_Cases_QuoteOrRefuse(t *testing.T) {
	tests := []struct {
		name, in, want string
		ok             bool
	}{
		{"plain id", "TEST-1.2", "TEST-1.2", true},
		{"space", "a b", "'a b'", true},
		{"single quote", "a'b", `'a'\''b'`, true},
		{"dollar and backtick", "$(x)`y`", "'$(x)`y`'", true},
		{"leading equals", "=cmd", "'=cmd'", true},
		{"leading dash", "-x", "'-x'", true},
		{"unicode letter", "é-1", "'é-1'", true},
		{"empty", "", "", false},
		{"NUL", "a\x00b", "", false},
		{"invalid UTF-8", "a\xffb", "", false},
		{"newline", "a\nb", "", false},
		{"tab", "a\tb", "", false},
		{"ESC", "a\x1b[31m", "", false},
		{"bidi override", "a\u202eb", "", false},
		{"zero width", "a\u200bb", "", false},
		{"non-ASCII space", "a\u00a0b", "", false},
		{"C1 control", "a\u0085b", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ShellWord(tc.in)
			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestShellOperand_LeadingDash_GetsDoubleDash(t *testing.T) {
	got, ok := ShellOperand("-x y")
	require.True(t, ok)
	assert.Equal(t, "-- '-x y'", got)
	got, ok = ShellOperand("TEST-1")
	require.True(t, ok)
	assert.Equal(t, "TEST-1", got)
	_, ok = ShellOperand("-a\x00")
	assert.False(t, ok)
}

func TestEventBlock_TruncatedHints_ShowCommandOnlyForTypeableValidTask(t *testing.T) {
	long := strings.Repeat("long ", 40)
	caps := Caps{MaxEvents: 5, MaxBodyBytes: 32}
	valid, err := RenderEnvelopeOn("worker", "", []Event{{Seq: 1, Task: "TEST-2", Sender: "sender", Body: long}}, caps)
	require.NoError(t, err)
	assert.Contains(t, valid, "full text: mtix show TEST-2;")
	for _, task := range []string{"TEST-1\x00", "TEST-1; rm -rf /", "evt\xff", "-x", "TEST-1\x1b[31m"} {
		text, renderErr := RenderEnvelopeOn("worker", "", []Event{{Seq: 1, Task: task, Sender: "sender", Body: long}}, caps)
		require.NoError(t, renderErr, task)
		assert.NotContains(t, text, "mtix show", task)
		assert.Contains(t, text, "truncated; full event remains unacked", task)
	}
}

func TestShowCommand_DashOperand_PutsBoardBeforeDoubleDash(t *testing.T) {
	board := " --board /tmp/b"
	assert.Equal(t, "mtix show TEST-2"+board, showCommand("TEST-2", board))
	assert.Equal(t, "mtix show"+board+" -- '-x'", showCommand("-- '-x'", board))
	assert.Equal(t, "mtix show -- '-x'", showCommand("-- '-x'", ""))
}
