// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package main

import "testing"

// signalSelfTerminate skips: a Windows process cannot signal itself with
// SIGTERM, so the real-serve shutdown tests run on Unix only.
func signalSelfTerminate(t *testing.T) {
	t.Helper()
	t.Skip("self SIGTERM is not available on Windows")
}
