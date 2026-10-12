// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package oplocal

// The operator-local mirror of the sticky hub mark (MTIX-112.17.3, ADR-007 A9).

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHubMarkMirror_WriteThenRead_IsPerCheckout(t *testing.T) {
	s := identityState(t)
	path := t.TempDir()
	got, _, err := s.HubMarkMirror(path)
	require.NoError(t, err)
	assert.Equal(t, HubMarkMirror{}, got)

	require.NoError(t, s.MirrorHubMark(path, "config"))
	got, reports, err := s.HubMarkMirror(path)
	require.NoError(t, err)
	require.Empty(t, reports)
	assert.Equal(t, HubMarkMirror{Checkout: path, Source: "config"}, got)

	other, _, err := s.HubMarkMirror(filepath.Join(path, "other"))
	require.NoError(t, err)
	assert.Equal(t, HubMarkMirror{}, other)
}

func TestHubMarkMirror_InvalidInput_IsRejected(t *testing.T) {
	s := identityState(t)
	require.Error(t, s.MirrorHubMark("relative/path", "config"))
	require.Error(t, s.MirrorHubMark(t.TempDir(), ""))
	_, _, err := s.HubMarkMirror("relative/path")
	require.Error(t, err)
}

func TestHubMarkMirror_ClearMirror_RemovesRecord(t *testing.T) {
	s := identityState(t)
	path := t.TempDir()
	require.NoError(t, s.MirrorHubMark(path, "secrets"))
	require.NoError(t, s.ClearHubMarkMirror(path))
	got, _, err := s.HubMarkMirror(path)
	require.NoError(t, err)
	assert.Equal(t, HubMarkMirror{}, got)
}
