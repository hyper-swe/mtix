//go:build linux || darwin

// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// A marker alone cannot make a blocked or timed-out canary a valid control.
package service

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMergeBaseBoard_Canary_ExpiredWatchdogRejectsExistingMarker(t *testing.T) {
	for _, positive := range []bool{false, true} {
		name, hint := "negative", "negative canary watchdog expired"
		if positive {
			name, hint = "positive", "positive canary watchdog expired"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			marker, gate := filepath.Join(root, "canary"), filepath.Join(root, "release")
			// A preexisting marker cannot hide cancellation, even if launch is delayed.
			boardWrite(t, marker, []byte("observed"))
			require.NoError(t, syscall.Mkfifo(gate, 0600))
			// The direct shell blocks opening the FIFO until its watchdog cancels it.
			path := boardScript(t, "printf observed > '"+marker+"'; read value < '"+gate+"'; exit 1")
			cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestMergeBaseBoard_Canary_ProcessHelper$", "-test.count=1", "-test.timeout=4s")
			cmd.Env = append(os.Environ(), "MTIX_BOARD_CANARY_HELPER=1", "MTIX_BOARD_CANARY_POSITIVE=0", "MTIX_BOARD_CANARY_PATH="+path, "MTIX_BOARD_CANARY_MARKER="+marker)
			if positive {
				cmd.Env = append(cmd.Env, "MTIX_BOARD_CANARY_POSITIVE=1")
			}
			raw, err := cmd.CombinedOutput()
			require.Error(t, err)
			require.Contains(t, string(raw), hint)
			require.NotContains(t, string(raw), "test timed out")
			require.FileExists(t, marker)
		})
	}
}
