// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"

	"github.com/hyper-swe/mtix/internal/service"
)

// attachErrorRemedy keeps an enrolment cause separate from a subsequent adapter
// alert, and gives each cause a fix that works when run: doctor prints rows for
// every enrolment cause, including a torn marker and an unprobeable lock, and
// each row carries the exact command (MTIX-112.17.16.21.2).
func attachErrorRemedy(err error, fallbackFix string) (string, string) {
	var guard *boardEnrolGuardError
	if !errors.As(err, &guard) {
		return attachAlertVerbFailure, fallbackFix
	}
	switch {
	case errors.Is(err, service.ErrEnrolInProgress):
		return attachAlertEnrolBlocked, "wait for worktree enrollment to finish; then run mtix doctor"
	case errors.Is(err, service.ErrEnrolMarkerUnreadable):
		return attachAlertEnrolBlocked, "run mtix doctor; its board.enrol-interrupted row prints the exact command, or STOP prose when an enrollment may still be live"
	case errors.Is(err, service.ErrEnrolLockUnprobeable):
		return attachAlertEnrolBlocked, "run mtix doctor; its board.enrol-lock row prints the exact command for the unprobeable enrollment lock"
	default:
		return attachAlertEnrolBlocked, "mtix doctor"
	}
}
