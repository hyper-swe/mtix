// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/stretchr/testify/require"
)

func TestHooksStatus_ValidateConfiguration(t *testing.T) {
	saveAndResetApp(t)
	root, err := filepath.EvalSymlinks(operatorTestHome(t))
	require.NoError(t, err)
	resolved := filepath.Join(root, "resolved")
	require.NoError(t, os.Mkdir(resolved, 0700))
	alias := filepath.Join(root, "alias")
	require.NoError(t, os.Symlink(resolved, alias))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(resolved, "missing", "config"))
	t.Setenv("CODEX_HOME", filepath.Join(alias, "missing"))
	env, err := oplocal.FromProcess()
	require.NoError(t, err)
	state := oplocal.New(env)
	for _, jsonOutput := range []bool{false, true} {
		app.jsonOutput = jsonOutput
		text := captureStdout(t, func() { require.ErrorIs(t, runHooksStatus(state), model.ErrOperatorStateUnreadable) })
		require.Contains(t, text, "refused")
		require.Contains(t, text, "mtix hooks status --json")
		require.Contains(t, text, oplocal.PlacementLimit)
	}
	_, err = os.Stat(filepath.Join(resolved, "missing"))
	require.ErrorIs(t, err, os.ErrNotExist)
}
