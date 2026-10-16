// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package sqlite

import (
	"context"
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/require"
)

func TestPermissionStoreIntegrity_CanceledAndClosedReadersDoNotReportAbsent(t *testing.T) {
	s, _ := applyTestStore(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	exists, err := s.PermissionPublicationExists(ctx, "0123456789abcdef0123456789abcdef")
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, exists)
	ids, err := s.PermissionAddressedAgents(ctx)
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, ids)
	require.NoError(t, s.Close())
	exists, err = s.PermissionPublicationExists(t.Context(), "0123456789abcdef0123456789abcdef")
	require.Error(t, err)
	require.False(t, exists)
	ids, err = s.PermissionAddressedAgents(t.Context())
	require.Error(t, err)
	require.Empty(t, ids)
}
func TestPermissionStoreIntegrity_DamagedAppliedEvidenceFailsClosed(t *testing.T) {
	s, raw := applyTestStore(t)
	require.NoError(t, applyOnce(t, s, foreignCreate(t, "MTIX-1", 1)))
	event := makeApplyEvent(t, model.OpComment, "MTIX-1", "fixture-sender", 2, &model.CommentPayload{Body: "synthetic addressed fixture", AuthorID: "fixture-sender", To: "valid-recipient"})
	require.NoError(t, applyOnce(t, s, event))
	before, err := s.GetNode(t.Context(), "MTIX-1")
	require.NoError(t, err)
	_, err = raw.ExecContext(t.Context(), `UPDATE sync_events SET payload = ? WHERE event_id = ?`, `{broken_private_fixture`, event.EventID)
	require.NoError(t, err)
	ids, err := s.PermissionAddressedAgents(t.Context())
	require.Error(t, err)
	require.NotContains(t, err.Error(), "broken_private_fixture")
	require.Empty(t, ids)
	var stored string
	require.NoError(t, raw.QueryRowContext(t.Context(), `SELECT payload FROM sync_events WHERE event_id = ?`, event.EventID).Scan(&stored))
	require.Equal(t, `{broken_private_fixture`, stored)
	node, err := s.GetNode(t.Context(), "MTIX-1")
	require.NoError(t, err)
	require.Len(t, node.Annotations, 1)
	require.Equal(t, before.Annotations, node.Annotations)
}
