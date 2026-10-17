// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/hooks"
)

func TestHooksTrustProgram_ShowsTheResolvedProgramBeforeApproving(t *testing.T) {
	state := reviewProject(t)
	approvals := hooks.NewApprovals(app.mtixDir, state, false)
	var notice string
	captureStdout(t, func() {
		notice = captureStderr(t, func() { require.NoError(t, runHooksTrustProgram(approvals, "runner", false, true)) })
	})
	require.Contains(t, notice, "Program to approve for hook \"runner\"")
	require.Contains(t, notice, "find")
}
