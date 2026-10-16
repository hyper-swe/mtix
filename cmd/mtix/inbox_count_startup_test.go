// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Count startup guards are scoped to the active count command.
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func TestInboxCountStartup_LiveServer_StaysLocal(t *testing.T) {
	initTestApp(t)
	dir := app.mtixDir
	require.NoError(t, writePIDLock(dir, 6849))
	for _, tt := range []struct {
		args []string
		want int
	}{{nil, 6849}, {[]string{"--count"}, 0}, {[]string{"--count=false"}, 6849}} {
		cmd := newInboxCmd()
		require.NoError(t, cmd.Flags().Parse(tt.args))
		require.Equal(t, tt.want, shouldRouteToServer(cmd))
	}
	root := newRootCmd()
	root.SetArgs([]string{"inbox", "--agent", "never", "--count"})
	require.NoError(t, app.store.Close())
	app.store = nil
	resetCloseOnce()
	out, err := cliBehaviorOutput(t, root.Execute)
	require.NoError(t, err)
	require.Equal(t, "0\n", out)
}

func TestInboxCountStartup_ChangedBoard_SkipsOnlyCountImport(t *testing.T) {
	pulledProject(t, "prefix: TEST\n")
	// Preserve annotations so importing this changed export is allowed.
	mtixDir := filepath.Join(".mtix")
	raw, err := os.ReadFile(filepath.Join(mtixDir, "tasks.json"))
	require.NoError(t, err)
	var data sqlite.ExportData
	require.NoError(t, json.Unmarshal(raw, &data))
	require.NoError(t, initApp(newInboxCmd(), ""))
	node, err := app.store.GetNode(t.Context(), "TEST-1")
	require.NoError(t, err)
	data.Nodes[0].Annotations = node.Annotations
	require.NoError(t, sqlite.RecomputeExportChecksum(&data))
	raw, err = json.Marshal(data)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(mtixDir, "tasks.json"), raw, 0600))
	require.NoError(t, app.store.Close())
	app.store = nil
	cmd := newInboxCmd()
	cmd.SetContext(t.Context())
	require.NoError(t, cmd.Flags().Parse([]string{"--count", "--agent", "worker"}))
	require.NoError(t, persistentPreRun(cmd, nil, ""))
	_, err = app.store.GetNode(t.Context(), "TEST-2")
	require.ErrorIs(t, err, model.ErrNotFound)
	after, err := os.ReadFile(filepath.Join(mtixDir, "tasks.json"))
	require.NoError(t, err)
	require.Equal(t, raw, after)
	require.NoError(t, app.store.Close())
	app.store = nil
	ordinary := newInboxCmd()
	ordinary.SetContext(t.Context())
	require.NoError(t, persistentPreRun(ordinary, nil, ""))
	_, err = app.store.GetNode(t.Context(), "TEST-2")
	require.NoError(t, err)
}

func TestInboxCountStartup_MissingAgent_RejectsBeforeInit(t *testing.T) {
	for _, args := range [][]string{{"--count"}, {"--count", "--agent="}} {
		saveAndResetApp(t)
		identityOperatorState(t)
		dir := t.TempDir()
		t.Chdir(dir)
		root := newRootCmd()
		root.SetArgs(append([]string{"-C", dir, "inbox"}, args...))
		require.ErrorContains(t, root.Execute(), "agent ID is required")
		require.Nil(t, app.store)
	}
}

// The ordinary read-error warning is existing base-green behavior. The count
// control protects the newly scoped import bypass without changing that policy.
func TestInboxCountStartup_AutoImportReadError_WarnsOnlyForOrdinaryInbox(t *testing.T) {
	for _, tt := range []struct {
		name  string
		count bool
	}{
		{"ordinary read error", false},
		{"count bypass", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			require.NoError(t, os.Mkdir(filepath.Join(app.mtixDir, "tasks.json"), 0700))
			require.NoError(t, app.store.Close())
			app.store = nil
			cmd := newInboxCmd()
			cmd.SetContext(t.Context())
			args := []string{"--agent", "worker"}
			if tt.count {
				args = append(args, "--count")
			}
			require.NoError(t, cmd.Flags().Parse(args))
			stderr := captureStderr(t, func() {
				require.NoError(t, persistentPreRun(cmd, nil, ""))
			})
			if tt.count {
				require.NotContains(t, stderr, "auto-import failed")
			} else {
				require.Contains(t, stderr, "auto-import failed")
				require.Contains(t, stderr, "tasks.json")
				require.NotContains(t, stderr, "auto-import of .mtix/tasks.json refused")
			}
		})
	}
}
