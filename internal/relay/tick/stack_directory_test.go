// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package tick_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hyper-swe/mtix/internal/relay/segment"
	"github.com/hyper-swe/mtix/internal/relay/tick"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOpen_SymlinkedDirectory_RefusesBeforeCreatingSegments verifies the
// path refusal in FR-21 §5.1 and §11.5 (MTIX-64.9.5).
func TestOpen_SymlinkedDirectory_RefusesBeforeCreatingSegments(t *testing.T) {
	for _, parts := range [][]string{
		{tick.PeersDirName},
		{tick.PeersDirName, peerA},
		{tick.PeersDirName, peerA, tick.SegmentsDirName},
	} {
		relative := filepath.Join(parts...)
		t.Run(relative, func(t *testing.T) {
			r := newRelay(t, false)
			cfg := r.config()
			outside := t.TempDir()
			link := filepath.Join(cfg.RelayDir, relative)
			require.NoError(t, os.MkdirAll(filepath.Dir(link), 0o700))
			require.NoError(t, os.Symlink(outside, link))

			stack, err := tick.Open(cfg)
			assert.ErrorIs(t, err, segment.ErrSymlink)
			assert.Nil(t, stack)
			entries, err := os.ReadDir(outside)
			require.NoError(t, err)
			assert.Empty(t, entries, "opening the stack must leave the symlink destination untouched")
		})
	}
}

// TestOpen_MissingDirectories_AreCreated verifies that ordinary missing
// directories are still created and Open succeeds (MTIX-305).
func TestOpen_MissingDirectories_AreCreated(t *testing.T) {
	r := newRelay(t, false)
	cfg := r.config()
	stack, err := tick.Open(cfg)
	require.NoError(t, err)
	require.NotNil(t, stack)
	fi, err := os.Lstat(filepath.Join(cfg.RelayDir, tick.PeersDirName, peerA, tick.SegmentsDirName))
	require.NoError(t, err)
	assert.True(t, fi.IsDir())
}
