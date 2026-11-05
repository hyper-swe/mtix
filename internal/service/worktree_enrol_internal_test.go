// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestRemoveTreeFilesFirst_NotMtixData_Refused(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "other")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "keep"), []byte("x"), 0o600))
	err := removeTreeFilesFirst(dir)
	require.ErrorIs(t, err, model.ErrInvalidInput)
	_, statErr := os.Stat(filepath.Join(dir, "keep"))
	assert.NoError(t, statErr, "a refused removal deletes nothing")
}

func TestRemoveTreeFilesFirst_MtixData_RemovesFilesThenDirs(t *testing.T) {
	data := filepath.Join(t.TempDir(), ".mtix", "data")
	require.NoError(t, os.MkdirAll(filepath.Join(data, "sub", "deeper"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(data, "sub", "deeper", "f"), []byte("x"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(data, "mtix.db"), []byte("x"), 0o600))
	require.NoError(t, removeTreeFilesFirst(data))
	_, err := os.Lstat(data)
	assert.ErrorIs(t, err, os.ErrNotExist)
	_, err = os.Lstat(filepath.Dir(data))
	assert.NoError(t, err, "the parent .mtix directory stays")
}
