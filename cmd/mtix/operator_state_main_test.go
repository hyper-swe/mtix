// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// isolatedOperatorEnv marks a process whose operator-local state is already
// isolated, so re-executed helper processes inherit it and create nothing.
const isolatedOperatorEnv = "MTIX_TEST_OPERATOR_ISOLATED"

// TestMain isolates operator-local state so command tests never write the
// developer's real configuration directory. Only the top test process creates
// and removes the directory; helper processes it starts inherit the variables.
func TestMain(m *testing.M) {
	if os.Getenv(isolatedOperatorEnv) != "" {
		os.Exit(m.Run())
	}
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
	for key, value := range map[string]string{"XDG_CONFIG_HOME": config, "APPDATA": config, isolatedOperatorEnv: "1"} {
		if err := os.Setenv(key, value); err != nil {
			fmt.Fprintln(os.Stderr, "operator test environment:", err)
			_ = os.RemoveAll(root)
			os.Exit(1)
		}
	}
	code := m.Run()
	if err := os.RemoveAll(root); err != nil {
		fmt.Fprintln(os.Stderr, "operator test cleanup:", err)
		code = 1
	}
	os.Exit(code)
}
