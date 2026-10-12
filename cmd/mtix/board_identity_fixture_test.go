// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

// Identity fixtures use exclusively owned operator state outside the checkout.

import (
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/stretchr/testify/require"
	"os"
	"testing"
)

func identityOperatorState(t *testing.T) *oplocal.State {
	t.Helper()
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	root, err := os.MkdirTemp(home, ".mtix-identity-test-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(root)) })
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv("APPDATA", root)
	env, err := oplocal.FromProcess()
	require.NoError(t, err)
	return oplocal.New(env)
}
