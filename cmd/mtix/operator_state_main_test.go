// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestMain isolates operator-local state so command tests never write the
// developer's real configuration directory.
func TestMain(m *testing.M) {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "home directory:", err)
		os.Exit(1)
	}
	root, err := os.MkdirTemp(home, ".mtix-cmd-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "operator test directory:", err)
		os.Exit(1)
	}
	config := filepath.Join(root, "operator")
	os.Setenv("XDG_CONFIG_HOME", config) //nolint:errcheck,gosec // test-process setup before any test runs
	os.Setenv("APPDATA", config)         //nolint:errcheck,gosec // test-process setup before any test runs
	code := m.Run()
	if err := os.RemoveAll(root); err != nil {
		fmt.Fprintln(os.Stderr, "operator test cleanup:", err)
		code = 1
	}
	os.Exit(code)
}
