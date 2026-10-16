// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPermissionAddressedAgents_ActualForeignAppliedOnly_IncludingAcked(t *testing.T) {
	s, raw := applyTestStore(t)
	require.NoError(t, applyOnce(t, s, foreignCreate(t, "MTIX-1", 1)))
	event := makeApplyEvent(t, model.OpComment, "MTIX-1", "fixture-sender", 2, &model.CommentPayload{Body: "Synthetic addressed fixture", AuthorID: "fixture-sender", To: "foreign-recipient"})
	require.NoError(t, applyOnce(t, s, event))
	require.NoError(t, s.AppendAnnotation(t.Context(), "MTIX-1", model.Annotation{ID: "local-annotation", Text: "Synthetic local fixture", Author: "fixture-local", Addressee: "local-recipient"}))
	ids, err := s.PermissionAddressedAgents(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{"foreign-recipient"}, ids)
	require.NoError(t, s.ResolveAnnotation(t.Context(), "MTIX-1", event.EventID, true))
	ids, err = s.PermissionAddressedAgents(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{"foreign-recipient"}, ids, "acknowledgment does not erase actual applied-addressed evidence")
	own := makeApplyEvent(t, model.OpComment, "MTIX-1", "fixture-local", 3, &model.CommentPayload{Body: "Synthetic local fixture", AuthorID: "fixture-local", To: "local-recipient"})
	require.NoError(t, raw.QueryRowContext(t.Context(), `SELECT event_id FROM sync_events WHERE op_type = ? AND sync_status = ?`, model.OpComment, model.SyncStatusPending).Scan(&own.EventID))
	require.NoError(t, applyOnce(t, s, own))
	var accepted int
	require.NoError(t, raw.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM applied_events WHERE event_id = ?`, own.EventID).Scan(&accepted))
	assert.Equal(t, 1, accepted, "actual own echo was acknowledged")
	ids, err = s.PermissionAddressedAgents(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{"foreign-recipient"}, ids, "an own echo supplies no additional foreign-addressed fact")
}
