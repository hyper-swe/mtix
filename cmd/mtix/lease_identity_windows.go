// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Windows wake ownership uses the current user SID.
package main

import (
	"fmt"
	"os/user"
)

func currentUserID() (string, error) {
	current, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("current Windows user: %w", err)
	}
	if current.Uid == "" {
		return "", fmt.Errorf("current Windows user has no SID")
	}
	return current.Uid, nil
}
