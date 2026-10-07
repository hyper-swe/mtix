// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/hyper-swe/mtix/internal/model"
)

// validateAgentID enforces the MTIX-125 identity contract before mutations.
// Empty means no assignment; required/default actors remain caller policy.
// The 64-byte ceiling matches FR-18.7; accepted IDs are never normalized.
func validateAgentID(id string) error {
	if id == "" {
		return nil
	}
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("agent ID must not be whitespace-only: %w", model.ErrInvalidInput)
	}
	if len(id) > 64 {
		return fmt.Errorf("agent ID exceeds 64 UTF-8 bytes: %w", model.ErrInvalidInput)
	}
	for _, r := range id {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return fmt.Errorf("agent ID contains control or invisible formatting character U+%04X: %w", r, model.ErrInvalidInput)
		}
	}
	return nil
}
