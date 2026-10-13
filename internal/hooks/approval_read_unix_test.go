// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package hooks

import (
	"os"
	"testing"

	"github.com/hyper-swe/mtix/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestApprovals_ProjectFileInput_RequiresReadableContents(t *testing.T) {
	dir, script := setupExecHook(t, "#!/bin/sh\nexit 0\n")
	require.NoError(t, os.Chmod(script, 0100))
	_, err := os.ReadFile(script)
	if err == nil {
		t.Skip("fixture requires an operator whose file read is denied")
	}
	require.ErrorIs(t, err, os.ErrPermission)
	a := NewApprovals(dir, testutil.OperatorState(t), false)
	require.Error(t, a.Approve("wake", true))
	require.NoError(t, os.Chmod(script, 0700))
	require.NoError(t, a.Approve("wake", true))
	decisions, err := a.Check()
	require.NoError(t, err)
	require.True(t, decisions["wake"].Trusted)
}
