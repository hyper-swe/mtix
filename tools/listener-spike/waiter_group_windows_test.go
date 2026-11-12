// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package main

import (
	"os/exec"
	"testing"

	"github.com/stretchr/testify/require"
)

// startInGroup starts cmd; the waiter guard tests skip on windows before
// reaching here, so no process group handling is needed.
func startInGroup(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	require.NoError(t, cmd.Start())
}

func pidAlive(pid int) bool { return false }
