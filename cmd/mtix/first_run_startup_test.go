// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/oplocal"
)

func TestInitApp_AnyCommand_RecordsHostFirstRun(t *testing.T) {
	saveAndResetApp(t)
	startupApprovalFixture(t, false)
	t.Chdir(t.TempDir())
	require.NoError(t, initApp(&cobra.Command{Use: "show"}, "error"))
	env, err := oplocal.FromProcess()
	require.NoError(t, err)
	var raw json.RawMessage
	reports, err := oplocal.New(env).ReadJSON("migration-first-run", &raw)
	require.NoError(t, err)
	require.Empty(t, reports)
	require.NotNil(t, raw)
}

func TestInitApp_UnusableOperatorState_WarnsOnceAndOnlyWhenNeeded(t *testing.T) {
	saveAndResetApp(t)
	root := startupApprovalFixture(t, false)
	t.Setenv("XDG_CONFIG_HOME", "relative-path")
	t.Setenv("APPDATA", "relative-path")
	t.Run("no approval input", func(t *testing.T) {
		t.Chdir(t.TempDir())
		out := captureStderr(t, func() { require.NoError(t, initApp(&cobra.Command{Use: "show"}, "info")) })
		require.NotContains(t, out, "operator state is unusable")
	})
	t.Run("approval input", func(t *testing.T) {
		t.Chdir(root)
		out := captureStderr(t, func() {
			require.NoError(t, initApp(&cobra.Command{Use: "show"}, "info"))
			t.Cleanup(func() { require.NoError(t, app.store.Close()) })
			initializeHookApprovals(&cobra.Command{Use: "show"}, app.mtixDir)
		})
		require.Equal(t, 1, strings.Count(out, "operator state is unusable"))
	})
}
