// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"

	"github.com/hyper-swe/mtix/internal/service"
)

// attachErrorRemedy keeps an enrolment cause separate from a subsequent adapter
// alert, and gives doctor the guard's recovery rather than an unrelated fix.
func attachErrorRemedy(err error, fallbackFix string) (string, string) {
	var guard *boardEnrolGuardError
	if !errors.As(err, &guard) {
		return attachAlertVerbFailure, fallbackFix
	}
	fix := "mtix doctor"
	if errors.Is(err, service.ErrEnrolInProgress) {
		fix = "wait for worktree enrollment to finish; then run mtix doctor"
	}
	return attachAlertEnrolBlocked, fix
}
