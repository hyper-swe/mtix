// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests for MTIX-26.8 structured exit codes: storage failure classes get
// distinct, documented exit codes so scripts and agents do not have to
// parse error wording. Written RED-first per TDD-WORKFLOW.md §1.1.
package main

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/hyper-swe/mtix/internal/model"
)

// TestExitCodeForError maps error classes to their contract exit codes.
func TestExitCodeForError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code int
	}{
		{"nil is success", nil, 0},
		{"generic error", fmt.Errorf("boom"), 1},
		{"disk full", model.ErrDiskFull, exitCodeDiskFull},
		{"wrapped disk full", fmt.Errorf("refusing write: %w", model.ErrDiskFull), exitCodeDiskFull},
		{"corrupted", model.ErrCorrupted, exitCodeCorrupted},
		{"wrapped corrupted", fmt.Errorf("integrity check: %w", model.ErrCorrupted), exitCodeCorrupted},
		{"inbox wait empty", errInboxWaitEmpty, exitCodeInboxEmpty},
		{"wrapped inbox wait empty", fmt.Errorf("park loop: %w", errInboxWaitEmpty), exitCodeInboxEmpty},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.code, exitCodeForError(tc.err))
		})
	}
}

// TestExitCodes_AreDistinctAndDocumentedValues pins the contract: 3 and 4
// are load-bearing for scripts and the fault-injection suite.
func TestExitCodes_AreDistinctAndDocumentedValues(t *testing.T) {
	assert.Equal(t, 3, exitCodeDiskFull)
	assert.Equal(t, 4, exitCodeCorrupted)
	// FR-19.4: 5 = inbox --wait empty timeout. A worker's poll loop branches on
	// this to tell "woke with work" (0) from "nothing yet, loop again" (5), so
	// it must stay distinct from the storage-failure codes and never collide
	// with the FR's originally-proposed 3 (which is disk-full here).
	assert.Equal(t, 5, exitCodeInboxEmpty)
	assert.NotEqual(t, exitCodeDiskFull, exitCodeInboxEmpty, "an empty inbox must never look like disk-full")
}
