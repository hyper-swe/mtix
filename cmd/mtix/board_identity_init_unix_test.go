//go:build !windows

// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

// Initialization reports filesystem failures without creating an identity.

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func TestInit_NewBoard_UnwritableDirectory_PreservesFailure(t *testing.T) {
	initTestApp(t)
	root := t.TempDir()
	t.Chdir(root)
	require.NoError(t, os.Chmod(root, 0500))
	t.Cleanup(func() { require.NoError(t, os.Chmod(root, 0700)) })
	err := runInit("TEST")
	assert.ErrorIs(t, err, os.ErrPermission)
	_, err = os.Stat(filepath.Join(root, ".mtix", "board-id"))
	assert.ErrorIs(t, err, os.ErrNotExist)
}
