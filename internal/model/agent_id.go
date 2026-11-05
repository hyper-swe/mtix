// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"
	"strings"
	"unicode"
)

// ValidateAgentID enforces the MTIX-125 identity contract before mutations.
// Empty means no assignment; required/default actors remain caller policy.
// The 64-byte ceiling matches FR-18.7; accepted IDs are never normalized.
func ValidateAgentID(id string) error {
	if id == "" {
		return nil
	}
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("agent ID must not be whitespace-only: %w", ErrInvalidInput)
	}
	if len(id) > 64 {
		return fmt.Errorf("agent ID exceeds 64 UTF-8 bytes: %w", ErrInvalidInput)
	}
	for _, r := range id {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return fmt.Errorf("agent ID contains control or invisible formatting character U+%04X: %w", r, ErrInvalidInput)
		}
	}
	return nil
}
