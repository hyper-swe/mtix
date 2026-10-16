// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"testing"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

func TestPermissionPublicationExists_OrdinaryEventAndImportedAnnotation(t *testing.T) {
	s, _ := mutationTestStore(t)
	mustCreateNode(t, s, "MTIX-1", "")
	annotation := permissionOnceAnnotation()
	exists, err := s.PermissionPublicationExists(t.Context(), annotation.ID)
	require.NoError(t, err)
	require.False(t, exists)
	require.NoError(t, s.AppendAnnotationOnce(t.Context(), "MTIX-1", annotation))
	exists, err = s.PermissionPublicationExists(t.Context(), annotation.ID)
	require.NoError(t, err)
	require.True(t, exists)
	data, err := s.Export(t.Context(), "MTIX", "fixture-current")
	require.NoError(t, err)
	require.NoError(t, sqlite.ValidateExport(data))
	peer, _ := mutationTestStore(t)
	_, err = peer.Import(t.Context(), data, sqlite.ImportModeReplace, false)
	require.NoError(t, err)
	exists, err = peer.PermissionPublicationExists(t.Context(), annotation.ID)
	require.NoError(t, err)
	require.True(t, exists, "actual imported annotation reserves the identity without a local comment event")
	var count int
	require.NoError(t, peer.QueryRow(t.Context(), `SELECT COUNT(*) FROM sync_events WHERE event_id = ?`, annotation.ID).Scan(&count))
	require.Zero(t, count)
	exists, err = peer.PermissionPublicationExists(t.Context(), "fedcba9876543210fedcba9876543210")
	require.NoError(t, err)
	require.False(t, exists)
}
