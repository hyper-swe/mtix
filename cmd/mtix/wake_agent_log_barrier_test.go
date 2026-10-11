//go:build !windows

// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

// Diagnostic barriers wait for the reported operation rather than file creation.

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func waitWakeLogReport(t *testing.T, path, report string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(raw), report) {
			return
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			require.NoError(t, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("owned wake fixture did not report %s", report)
}
