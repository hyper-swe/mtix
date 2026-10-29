// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Permission-related replay failures must leave the node, hub mark and durable
// event evidence in the same transaction, so retry remains safe.
package sqlite

import (
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestPermissionReplayFault_MirrorAndAppliedInsertFailuresRollbackThenRetry(t *testing.T) {
	for _, tc := range []struct{ name, create, drop string }{
		{"mirror", `CREATE TRIGGER permission_mirror_abort BEFORE INSERT ON sync_events BEGIN SELECT RAISE(ABORT,'synthetic mirror refusal'); END`, `DROP TRIGGER permission_mirror_abort`},
		{"applied", `CREATE TRIGGER permission_applied_abort BEFORE INSERT ON applied_events BEGIN SELECT RAISE(ABORT,'synthetic applied refusal'); END`, `DROP TRIGGER permission_applied_abort`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, raw := applyTestStore(t)
			_, err := raw.ExecContext(t.Context(), tc.create)
			require.NoError(t, err)
			event := foreignCreate(t, "MTIX-1", 1)
			require.Error(t, applyOnce(t, s, event))
			requirePermissionReplayEmpty(t, raw)
			_, err = raw.ExecContext(t.Context(), tc.drop)
			require.NoError(t, err)
			require.NoError(t, applyOnce(t, s, event))
			require.NoError(t, applyOnce(t, s, event))
			node, err := s.GetNode(t.Context(), "MTIX-1")
			require.NoError(t, err)
			require.Equal(t, "MTIX-1", node.ID)
			mark, err := s.HubMark(t.Context())
			require.NoError(t, err)
			require.True(t, mark.Set)
		})
	}
}

func requirePermissionReplayEmpty(t *testing.T, raw *sql.DB) {
	t.Helper()
	var count int
	require.NoError(t, raw.QueryRowContext(t.Context(), `SELECT
	 (SELECT COUNT(*) FROM nodes) + (SELECT COUNT(*) FROM sync_events) +
	 (SELECT COUNT(*) FROM applied_events) + (SELECT COUNT(*) FROM board_flags)`).Scan(&count))
	require.Zero(t, count)
}

func TestPermissionReplayFault_UnavailableDedupeEvidenceRefusesAndRollsBack(t *testing.T) {
	s, raw := applyTestStore(t)
	event := foreignCreate(t, "MTIX-1", 1)
	err := s.WithTx(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(t.Context(), `DROP TABLE applied_events`)
		if err != nil {
			return err
		}
		return s.IdempotentApply(t.Context(), tx, event)
	})
	require.ErrorContains(t, err, "dedupe check")
	requirePermissionReplayEmpty(t, raw)
	require.NoError(t, applyOnce(t, s, event))
}

func TestAppendAnnotationOnce_MissingReplayEventRejectsChangedTimeWithoutMutation(t *testing.T) {
	s, _ := applyTestStore(t)
	stamp := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	annotation := model.Annotation{ID: "0123456789abcdef0123456789abcdef", Author: "mtix-permissions", Text: "synthetic neutral fixture", CreatedAt: stamp}
	node := &model.Node{ID: "MTIX-1", Project: "MTIX", Title: "Synthetic imported annotation", Status: model.StatusOpen, Creator: "fixture", CreatedAt: stamp, UpdatedAt: stamp, Annotations: []model.Annotation{annotation}}
	require.NoError(t, s.CreateNode(t.Context(), node))
	before, err := s.GetNode(t.Context(), node.ID)
	require.NoError(t, err)
	annotation.CreatedAt = stamp.Add(time.Hour)
	require.ErrorIs(t, s.AppendAnnotationOnce(t.Context(), node.ID, annotation), model.ErrConflict)
	after, err := s.GetNode(t.Context(), node.ID)
	require.NoError(t, err)
	require.Equal(t, before, after)
}
