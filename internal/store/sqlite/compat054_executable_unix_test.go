//go:build !windows

// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

// Cache mode checks use an owned copy of the actual released reader.

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompat054ReaderLocation_NonExecutableCache_ReturnsClearError(t *testing.T) {
	readerPath := compat054Binary(t)
	raw, err := os.ReadFile(readerPath)
	require.NoError(t, err)
	cache := t.TempDir()
	platform := runtime.GOOS + "-" + runtime.GOARCH
	path := filepath.Join(cache, "mtix-test", "mtix-v0.5.4-beta", compat054TagCommit, platform, "mtix")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
	require.NoError(t, os.WriteFile(path, raw, 0600))
	require.NoError(t, os.Chmod(path, 0600))
	require.NoError(t, os.WriteFile(path+".sha256", []byte(fmt.Sprintf("%x", sha256.Sum256(raw))), 0600))
	cached, repo, err := compat054ReaderLocation(t.TempDir(), cache, platform)
	assert.Error(t, err)
	assert.Empty(t, cached)
	assert.Empty(t, repo)
	if err != nil {
		assert.Contains(t, err.Error(), "cache missing or invalid")
		assert.Contains(t, err.Error(), "tag repository unavailable")
	}
}
