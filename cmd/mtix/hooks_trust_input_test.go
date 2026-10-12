// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// These tests verify hook command input and corrective output.
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/oplocal"
)

func TestHooksTrust_NamedInput_AcceptsOneHook(t *testing.T) {
	cmd := newHooksTrustCmd()
	require.NoError(t, cmd.Args(cmd, []string{"wake"}))
}

func TestRunHooksTrust_NonTerminalInput_ReturnsError(t *testing.T) {
	for _, input := range []string{"pipe", "null"} {
		t.Run(input, func(t *testing.T) {
			saveAndResetApp(t)
			root := t.TempDir()
			app.mtixDir = filepath.Join(root, ".mtix")
			require.NoError(t, os.Mkdir(app.mtixDir, 0700))
			config := "hooks:\n  - name: wake\n    match: {events: [comment.addressed]}\n    deliver: [exec]\n    exec: {command: [/bin/true]}\n"
			require.NoError(t, os.WriteFile(filepath.Join(app.mtixDir, "hooks.yaml"), []byte(config), 0600))
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "operator"))
			t.Setenv("APPDATA", filepath.Join(root, "operator"))
			var stdin *os.File
			if input == "pipe" {
				r, w, err := os.Pipe()
				require.NoError(t, err)
				require.NoError(t, w.Close())
				stdin = r
			} else {
				var err error
				stdin, err = os.Open(os.DevNull)
				require.NoError(t, err)
			}
			original := os.Stdin
			os.Stdin = stdin
			t.Cleanup(func() {
				os.Stdin = original
				require.NoError(t, stdin.Close())
			})
			cmd := newHooksTrustCmd()
			captureStdout(t, func() { assert.Error(t, cmd.RunE(cmd, []string{"wake"})) })
			_, err := os.Stat(filepath.Join(app.mtixDir, "hooks.trust"))
			assert.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}

func TestHooksStatus_ConfiguredHook_ReportsExactFix(t *testing.T) {
	saveAndResetApp(t)
	root := t.TempDir()
	app.mtixDir = filepath.Join(root, ".mtix")
	require.NoError(t, os.Mkdir(app.mtixDir, 0700))
	config := "hooks:\n  - name: wake\n    match: {events: [comment.addressed]}\n    deliver: [exec]\n    exec: {command: [/bin/true]}\n"
	require.NoError(t, os.WriteFile(filepath.Join(app.mtixDir, "hooks.yaml"), []byte(config), 0600))
	home, err := filepath.EvalSymlinks(operatorTestHome(t))
	require.NoError(t, err)
	state := oplocal.New(operatorTestEnv(home))
	app.jsonOutput = true
	output := captureStdout(t, func() { require.NoError(t, runHooksStatus(state)) })
	var value map[string]any
	require.NoError(t, json.Unmarshal([]byte(output), &value))
	assert.Contains(t, value, "hooks")
	assert.Contains(t, output, "mtix hooks trust -- 'wake'")
	path, err := state.Path()
	require.NoError(t, err)
	_, err = os.Stat(path)
	assert.ErrorIs(t, err, os.ErrNotExist)
}
