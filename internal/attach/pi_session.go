// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package attach

import (
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// MaxSessionLen is the longest --session handle an attach verb accepts.
const MaxSessionLen = 128

// ValidateSession checks a --session handle at the process boundary (MTIX-112.19.7).
// The handle arrives from harness glue and is hostile input: it must be 1 to
// MaxSessionLen characters from [A-Za-z0-9._:-] and start with a letter or digit.
// It is only ever compared and stored as data, never passed to a shell.
func ValidateSession(s string) error {
	if s == "" || len(s) > MaxSessionLen {
		return fmt.Errorf("session handle must be 1 to %d characters: %w", MaxSessionLen, model.ErrInvalidInput)
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		alnum := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
		if alnum || (i > 0 && (c == '.' || c == '_' || c == ':' || c == '-')) {
			continue
		}
		return fmt.Errorf("session handle has a character outside [A-Za-z0-9._:-] or starts with a symbol: %w", model.ErrInvalidInput)
	}
	return nil
}
