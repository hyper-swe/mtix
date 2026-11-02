// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package oplocal

import (
	"errors"
	"fmt"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// These tests cover the private native-flush error chain and independent cleanup
// failures. Ordinary write errors retain their existing unreadable classification.
func TestNativeFlushError_Cause_PreservesClassification(t *testing.T) {
	err := fmt.Errorf("record write: %w", nativeFlushError{cause: syscall.EIO})
	require.ErrorIs(t, err, syscall.EIO)
	require.NotErrorIs(t, err, model.ErrOperatorStateUnreadable)
	require.Contains(t, err.Error(), "flush")
	require.True(t, isNativeFlushFailure(err))
	require.False(t, isNativeFlushFailure(nil))
	require.False(t, isNativeFlushFailure(fmt.Errorf("ordinary: %w", syscall.EIO)))
}

func TestWriteStateError_Policy_PreservesIndependentCauses(t *testing.T) {
	cleanup := errors.New("independent cleanup failed")
	native := nativeFlushError{cause: syscall.EIO}
	tests := []struct {
		name       string
		input      error
		unreadable bool
		cleanup    bool
	}{
		{"ordinary", syscall.EIO, true, false},
		{"native", native, false, false},
		{"wrapped native", fmt.Errorf("publication: %w", native), false, false},
		{"native and cleanup", errors.Join(native, failure("cleanup", cleanup)), true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := writeStateError(tt.input)
			require.ErrorIs(t, err, syscall.EIO)
			require.Equal(t, tt.unreadable, errors.Is(err, model.ErrOperatorStateUnreadable))
			require.Equal(t, tt.cleanup, errors.Is(err, cleanup))
		})
	}
}
