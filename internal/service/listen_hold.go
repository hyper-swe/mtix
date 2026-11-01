// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import "time"

// ListenHoldArmed reports whether the listen hold is armed at now for one
// snapshot of scheduler inputs, and when the idle clamp ends it. It is the
// same rule the ContactScheduler applies (MTIX-112.20.1), exported so the
// per-board listener (MTIX-112.20.4) decides when to exit from the very
// rule that decides when to dial: manual mode never holds.
func ListenHoldArmed(in ContactInputs, now time.Time) (bool, time.Time) {
	if in.Mode.Kind == HubContactManual {
		return false, time.Time{}
	}
	return listenHold(in, now)
}
