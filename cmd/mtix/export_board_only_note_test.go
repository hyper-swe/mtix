// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestNewExportCmd_BoardOnlyPrivateTarget_ExplainsJournalExclusion(t *testing.T) {
	initTestApp(t)
	envelopeCLIEvents(t, "private mail")
	cmd := newExportCmd()
	require.NoError(t, cmd.ParseFlags([]string{"--board-only"}))
	note, err := captureStderrOf(t, func() error {
		_, runErr := cliBehaviorOutput(t, func() error { return cmd.RunE(cmd, nil) })
		return runErr
	})
	require.NoError(t, err)
	require.Contains(t, note, "journal-free export")
	require.NotContains(t, note, "this tracked-mirror export")
	require.Contains(t, note, "without --board-only")
}
