// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package oplocal

import (
	"errors"
	"os"
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/require"
)

func TestWriteJSON_DirectorySyncFailure_ReportsCommittedContent(t *testing.T) {
	s := fixture(t)
	require.NoError(t, s.WriteJSON("hooks", "original"))
	failure := errors.New("operation failed")
	called := false
	s.syncDirectory = func(dir *os.File) error {
		called = true
		require.Equal(t, s.pathForTest(t), dir.Name())
		return failure
	}
	err := s.WriteJSON("hooks", "replacement")
	require.ErrorIs(t, err, failure)
	require.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
	require.True(t, called)
	var got string
	_, err = s.ReadJSON("hooks", &got)
	require.NoError(t, err)
	require.Equal(t, "replacement", got)
	s.syncDirectory = nil
	require.NoError(t, s.WriteJSON("hooks", "recovered"))
	_, err = s.ReadJSON("hooks", &got)
	require.NoError(t, err)
	require.Equal(t, "recovered", got)
}
