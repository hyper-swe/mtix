// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package oplocal

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/testutil/hostguard"
)

func TestFromProcess_UnderHarness_ResolvesOperatorDirInsideScratchRoot(t *testing.T) {
	env, err := FromProcess()
	require.NoError(t, err)
	dir, err := Dir(env)
	require.NoError(t, err)
	require.NoError(t, hostguard.AssertPath(filepath.Join(dir, "wake.json")))
	require.NoError(t, hostguard.AssertPath(env.Home))
}

func TestFromProcess_UnisolatedXDG_ResolvesPathTheGuardRejects(t *testing.T) {
	outside := filepath.Join(filepath.Dir(os.Getenv(hostguard.RootEnv)), "real-config")
	t.Setenv("XDG_CONFIG_HOME", outside)
	env, err := FromProcess()
	require.NoError(t, err)
	dir, err := Dir(env)
	require.NoError(t, err)
	require.Error(t, hostguard.AssertPath(filepath.Join(dir, "wake.json")), "a write to the unisolated dir must be caught")
	require.Error(t, hostguard.Assert())
}

func TestFromProcess_UnisolatedHome_FallbackConfigPathIsRejected(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", filepath.Join(filepath.Dir(os.Getenv(hostguard.RootEnv)), "real-home"))
	env, err := FromProcess()
	require.NoError(t, err)
	dir, err := Dir(env)
	require.NoError(t, err)
	require.Error(t, hostguard.AssertPath(dir))
	require.Error(t, hostguard.Assert())
}
