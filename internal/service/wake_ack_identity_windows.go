// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"fmt"
	"os/user"

	"github.com/hyper-swe/mtix/internal/model"
)

func wakeAckUserID() (string, error) {
	current, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("acknowledgement Windows identity: %w", err)
	}
	if current.Uid == "" {
		return "", fmt.Errorf("acknowledgement Windows SID is missing: %w", model.ErrInvalidInput)
	}
	return current.Uid, nil
}
