// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// isolatedOperatorEnv marks a process whose operator-local state is already
// isolated, so re-executed helper processes inherit it and create nothing.
const isolatedOperatorEnv = "MTIX_TEST_OPERATOR_ISOLATED"

// TestMain isolates operator-local state so command tests never write the
// developer's real configuration directory. The directory name is derived from
// this test binary's path, so helper processes that re-execute the binary share
// it even when they are started with a reduced environment. Only the process
// that created the directory removes it, so an abandoned helper leaves nothing.
func TestMain(m *testing.M) {
	if os.Getenv(isolatedOperatorEnv) != "" {
		os.Exit(m.Run())
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "home directory:", err)
		os.Exit(1)
	}
	binary, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "test binary path:", err)
		os.Exit(1)
	}
	sum := sha256.Sum256([]byte(binary))
	root := filepath.Join(home, ".mtix-cmd-test-"+hex.EncodeToString(sum[:8]))
	owner := true
	if err := os.Mkdir(root, 0o700); errors.Is(err, os.ErrExist) {
		owner = false
	} else if err != nil {
		fmt.Fprintln(os.Stderr, "operator test directory:", err)
		os.Exit(1)
	}
	config := filepath.Join(root, "operator")
	for key, value := range map[string]string{"XDG_CONFIG_HOME": config, "APPDATA": config, isolatedOperatorEnv: "1"} {
		if err := os.Setenv(key, value); err != nil {
			fmt.Fprintln(os.Stderr, "operator test environment:", err)
			os.Exit(1)
		}
	}
	code := m.Run()
	if owner {
		if err := os.RemoveAll(root); err != nil {
			fmt.Fprintln(os.Stderr, "operator test cleanup:", err)
			code = 1
		}
	}
	os.Exit(code)
}
