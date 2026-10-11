// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package oplocal

import (
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func TestWalkDirectory_InitialAncestorRejected_ClosesBeforeCreation(t *testing.T) {
	root := safeFixtureRoot(t)
	target := filepath.Join(root, "config")
	initial, err := os.Open("/")
	require.NoError(t, err)
	check := func(dir *os.File) error {
		if dir == initial {
			return invalid("validate input")
		}
		return verifyAncestor(dir)
	}
	result, err := walkDirectoryChecked(initial, target, true, check)
	if result != nil {
		require.NoError(t, result.Close())
	}
	require.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
	requireMissingDirectory(t, target)
	_, err = initial.Stat()
	require.ErrorIs(t, err, os.ErrClosed)
}
