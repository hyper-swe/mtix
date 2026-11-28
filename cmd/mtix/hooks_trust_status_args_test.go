// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/hooks"
	"github.com/hyper-swe/mtix/internal/oplocal"
)

// TestHooksTrustStatus_ProgramWithoutName_IsRefused pins that only the plain
// --status form may omit the hook name; --program still needs one.
func TestHooksTrustStatus_ProgramWithoutName_IsRefused(t *testing.T) {
	pulledProject(t, "prefix: TEST\n")
	for _, args := range [][]string{
		{"hooks", "trust", "--program", "--status"},
		{"hooks", "trust", "--program"},
		{"hooks", "trust"},
	} {
		saveAndResetApp(t)
		var err error
		out := captureStdout(t, func() { err = runArgs(args) })
		require.ErrorContains(t, err, "accepts 1 arg(s), received 0", "%v", args)
		require.Empty(t, out, "%v", args)
	}
}

// TestHooksTrustStatus_JSONUnlocked_IsBareArray pins the fresh (lock free)
// --json shape: a bare array of trusted hooks, not the stale {data,stale} wrap.
func TestHooksTrustStatus_JSONUnlocked_IsBareArray(t *testing.T) {
	pulledProject(t, "prefix: TEST\n")
	const config = `hooks:
  - name: alpha
    match: {events: [comment.addressed]}
    deliver: [exec]
    exec: {command: [sh, wake.sh]}
`
	require.NoError(t, os.WriteFile(filepath.Join(".mtix", "hooks.yaml"), []byte(config), 0o600))
	require.NoError(t, os.WriteFile("wake.sh", []byte("#!/bin/sh\nexit 0\n"), 0o700))
	env, err := oplocal.FromProcess()
	require.NoError(t, err)
	require.NoError(t, hooks.NewApprovals(".mtix", oplocal.New(env), false).Approve("alpha", true))

	out, err := runHookStatusReader(t, false, "hooks", "trust", "--status", "--json")
	require.NoError(t, err, out)
	var list []hooks.Decision
	require.NoError(t, json.Unmarshal([]byte(out), &list), out)
	require.Len(t, list, 1)
	require.Equal(t, "alpha", list[0].Name)
	require.True(t, list[0].Trusted)
}
