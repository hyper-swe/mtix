// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MTIX-2.3.2.2: a staged workflow conflict keeps every byte of its strings,
// invalid UTF-8 and the escape mark included.
func TestEncodeWorkflowConflict_InvalidUTF8_RoundTripsByteForByte(t *testing.T) {
	odd := "a\xffb\xc3(m�\x00z"
	old, empty := odd, ""
	c := WorkflowConflict{ID: "REC-1", Title: odd, Hint: odd, Choice: WorkflowOurs, Fields: []WorkflowFieldDiff{{
		Field: "status", Local: odd, File: "",
		LocalHistory: []exportHistoryEntry{{ID: odd, Actor: odd, Old: &old, New: &empty}},
		FileHistory:  []exportHistoryEntry{},
	}}}
	raw, err := encodeWorkflowConflict(c)
	require.NoError(t, err)
	got, err := decodeWorkflowConflict(raw)
	require.NoError(t, err)
	assert.Equal(t, c, got)
	assert.NotNil(t, got.Fields[0].FileHistory, "an empty list stays empty, not null")
}

// Text that is not an escape this code wrote is left as it is.
func TestUnescapeLossless_NotAnEscape_KeptAsIs(t *testing.T) {
	for _, s := range []string{"", "plain", "end", "x", ""} {
		assert.Equal(t, s, unescapeLossless(s))
	}
}
