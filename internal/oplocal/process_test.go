// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"github.com/stretchr/testify/require"
	"os"
	"runtime"
	"testing"
)

func TestFromProcess_ConfiguredEnvironment_ResolvesOperatorPaths(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/config")
	t.Setenv("CODEX_HOME", "/harness")
	t.Setenv("TMPDIR", "")
	env, err := FromProcess()
	require.NoError(t, err)
	require.Equal(t, runtime.GOOS, env.GOOS)
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	require.Equal(t, home, env.Home)
	require.Equal(t, "/config", env.Values["XDG_CONFIG_HOME"])
	require.Equal(t, os.TempDir(), env.Values["TMPDIR"])
	require.Equal(t, "/harness", env.Values["CODEX_HOME"])
	t.Setenv("TMPDIR", "/chosen-temp")
	env, err = FromProcess()
	require.NoError(t, err)
	require.Equal(t, "/chosen-temp", env.Values["TMPDIR"])
}
func TestFromProcess_MissingHome_ReturnsError(t *testing.T) {
	key := "HOME"
	if runtime.GOOS == "windows" {
		key = "USERPROFILE"
	}
	t.Setenv(key, "")
	_, err := FromProcess()
	require.ErrorContains(t, err, "resolve home")
}
