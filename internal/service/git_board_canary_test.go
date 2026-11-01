// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Owned test subprocesses distinguish genuine canary exits from arbitrary failure.
package service

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/testutil/hostguard"
)

func TestMergeBaseBoard_Canary_ProcessHelper(t *testing.T) {
	if os.Getenv("MTIX_BOARD_CANARY_HELPER") != "1" {
		return
	}
	path, marker := os.Getenv("MTIX_BOARD_CANARY_PATH"), os.Getenv("MTIX_BOARD_CANARY_MARKER")
	if os.Getenv("MTIX_BOARD_CANARY_POSITIVE") == "1" {
		boardPositiveControl(t, path, marker)
		return
	}
	boardPositiveFailure(t, path, marker)
}

func TestMergeBaseBoard_Canary_WatchdogFollowsTestDeadline(t *testing.T) {
	ctx, cancel := boardTestWatchdog(t)
	defer cancel()
	deadline, hasDeadline := t.Deadline()
	actual, watchdogHasDeadline := ctx.Deadline()
	require.Equal(t, hasDeadline, watchdogHasDeadline)
	if hasDeadline {
		require.Equal(t, deadline.Add(-3*time.Second), actual)
	}
}

func TestMergeBaseBoard_Canary_RequiresExpectedExitAndMarker(t *testing.T) {
	for _, tc := range []struct {
		name, body         string
		positive, accepted bool
	}{
		{"positive", "printf observed > ", true, true},
		{"negative", "printf observed > ", false, true},
		{"wrong-exit", "printf observed > ", false, false},
		{"positive-failure", "printf observed > ", true, false},
		{"missing-marker", "", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "canary")
			body := tc.body
			if body != "" {
				body += "'" + marker + "'; "
			}
			exit := "1"
			switch tc.name {
			case "positive":
				exit = "0"
			case "wrong-exit":
				exit = "2"
			}
			cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestMergeBaseBoard_Canary_ProcessHelper$", "-test.count=1", "-test.timeout=30s")
			cmd.Env = append(hostguard.Environ(), "MTIX_BOARD_CANARY_HELPER=1", "MTIX_BOARD_CANARY_POSITIVE=0", "MTIX_BOARD_CANARY_PATH="+boardScript(t, body+"exit "+exit), "MTIX_BOARD_CANARY_MARKER="+marker)
			if tc.positive {
				cmd.Env = append(cmd.Env, "MTIX_BOARD_CANARY_POSITIVE=1")
			}
			raw, err := cmd.CombinedOutput()
			if tc.accepted {
				require.NoError(t, err, "%s", raw)
				require.NoFileExists(t, marker)
			} else {
				require.Error(t, err, "invalid control must fail its helper: %s", raw)
			}
		})
	}
}
