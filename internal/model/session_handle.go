// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"
	"unicode"
)

// MaxSessionHandleLen is the longest harness session handle, in bytes.
const MaxSessionHandleLen = 256

// ValidateSessionHandle is the one rule for a harness session handle, shared by agent
// registration and the attach wait verb (MTIX-112.19.7), so a registrable handle is always
// waitable. A handle is at most MaxSessionHandleLen bytes and holds no control character.
// The empty handle is valid here (a session with no known handle); callers that need one
// check for it themselves. The handle is only compared and stored as data, never run.
func ValidateSessionHandle(s string) error {
	if len(s) > MaxSessionHandleLen {
		return fmt.Errorf("session handle exceeds %d bytes: %w", MaxSessionHandleLen, ErrInvalidInput)
	}
	for _, c := range s {
		if unicode.IsControl(c) {
			return fmt.Errorf("session handle contains a control character: %w", ErrInvalidInput)
		}
	}
	return nil
}
