// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
//go:build !windows

package service

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

// Native permission checks retain the filesystem's refusal cause.
func TestReadBoardIDs_UnreadableFile_ReturnsPermissionCause(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "board-id")
	require.NoError(t, os.WriteFile(path, []byte("identity"), 0000))
	t.Cleanup(func() { require.NoError(t, os.Chmod(path, 0600)) })
	_, err := ReadBoardIDs(dir)
	assert.ErrorIs(t, err, os.ErrPermission)
}
