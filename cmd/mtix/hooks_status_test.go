// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package main

import (
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestHooksStatus_ValidateInput(t *testing.T) {
	cmd := newHooksCmd()
	status, _, err := cmd.Find([]string{"status"})
	require.NoError(t, err)
	require.Equal(t, "status", status.Name())
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	state := oplocal.New(oplocal.Env{GOOS: "darwin", Home: "/home/test", Root: root})
	saveAndResetApp(t)
	app.jsonOutput = true
	text := captureStdout(t, func() { require.NoError(t, runHooksStatus(state)) })
	require.Contains(t, text, `"status": "absent"`)
	require.NoError(t, state.Ensure())
	text = captureStdout(t, func() { require.NoError(t, runHooksStatus(state)) })
	require.Contains(t, text, `"status": "ready"`)
}

func TestHooksStatus_Output(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	state := oplocal.New(oplocal.Env{GOOS: "linux", Home: "/home/test", Root: root, Values: map[string]string{"XDG_CONFIG_HOME": "/tmp"}})
	saveAndResetApp(t)
	text := captureStdout(t, func() { require.Error(t, runHooksStatus(state)) })
	require.Contains(t, text, "refused")
	require.Contains(t, text, "mtix hooks status --json")
	app.jsonOutput = true
	text = captureStdout(t, func() { require.Error(t, runHooksStatus(state)) })
	require.Contains(t, text, `"status": "refused"`)
	require.Contains(t, text, "directory must be outside /tmp")
}

func TestHooksStatus_Command(t *testing.T) {
	saveAndResetApp(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cmd := newHooksStatusCmd()
	text := captureStdout(t, func() { require.Error(t, cmd.RunE(cmd, nil)) })
	require.Contains(t, text, "Operator-local state: refused")
}

func TestHooksStatus_Reports(t *testing.T) {
	saveAndResetApp(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	state := oplocal.New(oplocal.Env{GOOS: "linux", Home: "/home/test", Root: root})
	require.NoError(t, state.WriteJSON("hooks", map[string]int{}))
	dir, err := state.Path()
	require.NoError(t, err)
	body := []byte(`{"host_id":"00000000000000000000000000000000","data":{}}`)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hooks.json"), body, 0600))
	text := captureStdout(t, func() { require.NoError(t, runHooksStatus(state)) })
	require.Contains(t, text, "record belongs to another host")
}

func TestHooksStatus_HomeInput(t *testing.T) {
	saveAndResetApp(t)
	key := "HOME"
	if runtime.GOOS == "windows" {
		key = "USERPROFILE"
	}
	t.Setenv(key, "")
	cmd := newHooksStatusCmd()
	require.ErrorContains(t, cmd.RunE(cmd, nil), "resolve home")
}
