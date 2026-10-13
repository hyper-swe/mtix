// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package format

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/hyper-swe/mtix/internal/model"
)

// validateRecipient mirrors the MTIX-125 local-agent contract, with a required
// recipient. It preserves accepted bytes and keeps sender validation separate.
func validateRecipient(agent string) error {
	if agent == "" {
		return fmt.Errorf("recipient agent ID is required: %w", model.ErrInvalidInput)
	}
	if strings.TrimSpace(agent) == "" {
		return fmt.Errorf("recipient agent ID must not be whitespace-only: %w", model.ErrInvalidInput)
	}
	if len(agent) > 64 {
		return fmt.Errorf("recipient agent ID exceeds 64 UTF-8 bytes: %w", model.ErrInvalidInput)
	}
	for _, r := range agent {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return fmt.Errorf("recipient agent ID contains control or invisible formatting character U+%04X: %w", r, model.ErrInvalidInput)
		}
	}
	return nil
}

// quoteRecipient returns one POSIX single-quoted word without normalizing bytes.
func quoteRecipient(agent string) string { return "'" + strings.ReplaceAll(agent, "'", `'\''`) + "'" }
