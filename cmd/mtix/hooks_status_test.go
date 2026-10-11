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
	root, err := filepath.EvalSymlinks(operatorTestHome(t))
	require.NoError(t, err)
	state := oplocal.New(operatorTestEnv(root))
	saveAndResetApp(t)
	app.jsonOutput = true
	text := captureStdout(t, func() { require.NoError(t, runHooksStatus(state)) })
	require.Contains(t, text, `"status": "absent"`)
	require.Contains(t, text, oplocal.PlacementLimit)
	require.NoError(t, state.Ensure())
	text = captureStdout(t, func() { require.NoError(t, runHooksStatus(state)) })
	require.Contains(t, text, `"status": "ready"`)
	require.Contains(t, text, oplocal.PlacementLimit)
}

func TestHooksStatus_Output(t *testing.T) {
	root, err := filepath.EvalSymlinks(operatorTestHome(t))
	require.NoError(t, err)
	env := operatorTestEnv(root)
	env.Values["CODEX_HOME"] = root
	state := oplocal.New(env)
	saveAndResetApp(t)
	text := captureStdout(t, func() { require.Error(t, runHooksStatus(state)) })
	require.Contains(t, text, "refused")
	require.Contains(t, text, oplocal.PlacementLimit)
	require.Contains(t, text, "mtix hooks status --json")
	app.jsonOutput = true
	text = captureStdout(t, func() { require.Error(t, runHooksStatus(state)) })
	require.Contains(t, text, `"status": "refused"`)
	require.Contains(t, text, oplocal.PlacementLimit)
	require.Contains(t, text, "directory must be outside")
}

func TestHooksStatus_Command(t *testing.T) {
	saveAndResetApp(t)
	temp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", temp)
	t.Setenv("APPDATA", temp)
	t.Setenv("CODEX_HOME", temp)
	cmd := newHooksStatusCmd()
	text := captureStdout(t, func() { require.Error(t, cmd.RunE(cmd, nil)) })
	require.Contains(t, text, "Operator-local state: refused")
}

func TestHooksStatus_Reports(t *testing.T) {
	saveAndResetApp(t)
	root, err := filepath.EvalSymlinks(operatorTestHome(t))
	require.NoError(t, err)
	state := oplocal.New(operatorTestEnv(root))
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

func operatorTestHome(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	dir, err := os.MkdirTemp(home, ".mtix-status-test-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(dir)) })
	return dir
}

func operatorTestEnv(root string) oplocal.Env {
	env := oplocal.Env{GOOS: runtime.GOOS, Home: root, Values: map[string]string{}}
	if runtime.GOOS == "windows" {
		env.Values["APPDATA"] = filepath.Join(root, "config")
	}
	return env
}
