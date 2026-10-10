// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package main

import (
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/stretchr/testify/require"
	"path/filepath"
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
