// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package attach

import (
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// MaxSessionLen is the longest --session handle an attach verb accepts; it is the
// registration limit, so every registrable handle is waitable.
const MaxSessionLen = model.MaxSessionHandleLen

// ValidateSession checks a --session handle at the process boundary (MTIX-112.19.7).
// The handle arrives from harness glue and is hostile input. It must be non-empty and
// satisfy model.ValidateSessionHandle, the same rule agent registration applies, so a
// session that could register can also wait. It is only ever compared and stored as
// data and is passed to the wait child as one argv element, never through a shell.
func ValidateSession(s string) error {
	if s == "" {
		return fmt.Errorf("session handle is required: %w", model.ErrInvalidInput)
	}
	return model.ValidateSessionHandle(s)
}
