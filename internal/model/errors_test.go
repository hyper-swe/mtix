// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// TestErrors_AreDistinct verifies all sentinel errors are unique values.
func TestErrors_AreDistinct(t *testing.T) {
	allErrors := []error{
		model.ErrNotFound,
		model.ErrAlreadyExists,
		model.ErrInvalidInput,
		model.ErrInvalidTransition,
		model.ErrCycleDetected,
		model.ErrConflict,
		model.ErrAlreadyClaimed,
		model.ErrNodeBlocked,
		model.ErrStillDeferred,
		model.ErrAgentStillActive,
		model.ErrNoActiveSession,
		model.ErrInvalidConfigKey,
		model.ErrDepthWarning,
	}

	// Verify no two errors are the same.
	seen := make(map[string]bool, len(allErrors))
	for _, err := range allErrors {
		msg := err.Error()
		assert.False(t, seen[msg],
			"duplicate error message: %q", msg)
		seen[msg] = true
	}

	// Verify we have the expected count (12 + 1 advisory).
	assert.Len(t, allErrors, 13, "expected 13 sentinel errors")
}

// TestErrors_CanBeWrappedAndUnwrapped verifies errors work with errors.Is
// after wrapping with fmt.Errorf and %w.
func TestErrors_CanBeWrappedAndUnwrapped(t *testing.T) {
	tests := []struct {
		name     string
		sentinel error
	}{
		{"ErrNotFound", model.ErrNotFound},
		{"ErrAlreadyExists", model.ErrAlreadyExists},
		{"ErrInvalidInput", model.ErrInvalidInput},
		{"ErrInvalidTransition", model.ErrInvalidTransition},
		{"ErrCycleDetected", model.ErrCycleDetected},
		{"ErrConflict", model.ErrConflict},
		{"ErrAlreadyClaimed", model.ErrAlreadyClaimed},
		{"ErrNodeBlocked", model.ErrNodeBlocked},
		{"ErrStillDeferred", model.ErrStillDeferred},
		{"ErrAgentStillActive", model.ErrAgentStillActive},
		{"ErrNoActiveSession", model.ErrNoActiveSession},
		{"ErrInvalidConfigKey", model.ErrInvalidConfigKey},
		{"ErrDepthWarning", model.ErrDepthWarning},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Wrap the error with context.
			wrapped := fmt.Errorf("create node PROJ-1: %w", tt.sentinel)

			// Verify errors.Is works through the wrap.
			require.True(t, errors.Is(wrapped, tt.sentinel),
				"errors.Is should find %v in wrapped error", tt.sentinel)

			// Verify double-wrapping works.
			doubleWrapped := fmt.Errorf("service layer: %w", wrapped)
			require.True(t, errors.Is(doubleWrapped, tt.sentinel),
				"errors.Is should find %v in double-wrapped error", tt.sentinel)
		})
	}
}
