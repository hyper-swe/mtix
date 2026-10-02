// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestRecoverHints_ReplaceImport_SaysTypedConfirmationAndNoFlag pins that both
// places `mtix recover` offers `mtix import --mode replace` (the command's
// Long help and the next-step hint) say the replace needs the ticket count
// typed at an interactive terminal when the store holds tickets, that no flag
// supplies it, and that the wording never says "human" (MTIX-107.74, MTIX-95.11.3).
func TestRecoverHints_ReplaceImport_SaysTypedConfirmationAndNoFlag(t *testing.T) {
	long := strings.Join(strings.Fields(newRecoverCmd().Long), " ")
	for name, text := range map[string]string{"Long help": long, "next-step hint": recoverNextStep} {
		assert.Contains(t, text, "mtix import --mode replace", name)
		assert.Contains(t, text, "typed at an interactive terminal", name)
		assert.Contains(t, text, "cannot run unattended", name)
		assert.NotContains(t, strings.ToLower(text), "human", name)
		assert.Contains(t, text, "holds tickets", name, "an empty store is not prompted")
	}
}
