// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import (
	"os"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

// signalSelfTerminate sends SIGTERM to this test process, which the serve
// under test catches to shut down.
func signalSelfTerminate(t *testing.T) {
	t.Helper()
	require.NoError(t, syscall.Kill(os.Getpid(), syscall.SIGTERM))
}
