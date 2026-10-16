// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package sqlite_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/require"
)

func contractAnnotationCounts(t *testing.T, raw *sql.DB) [2]int {
	t.Helper()
	var result [2]int
	require.NoError(t, raw.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM sync_events`).Scan(&result[0]))
	require.NoError(t, raw.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM task_history`).Scan(&result[1]))
	return result
}
func TestAppendAnnotationOnce_FaultRollsBackAnnotationEventHistoryAndCallback(t *testing.T) {
	for _, target := range []string{"annotation-update", "event-insert"} {
		t.Run(target, func(t *testing.T) {
			s, raw := mutationTestStore(t)
			mustCreateNode(t, s, "MTIX-1", "")
			original, err := s.GetNode(t.Context(), "MTIX-1")
			require.NoError(t, err)
			before := contractAnnotationCounts(t, raw)
			callbacks := 0
			s.SetOnCommit(func() { callbacks++ })
			trigger := `CREATE TRIGGER contract_abort BEFORE UPDATE OF annotations ON nodes BEGIN SELECT RAISE(ABORT,'synthetic annotation fault'); END`
			if target == "event-insert" {
				trigger = `CREATE TRIGGER contract_abort BEFORE INSERT ON sync_events BEGIN SELECT RAISE(ABORT,'synthetic event fault'); END`
			}
			_, err = raw.ExecContext(t.Context(), trigger)
			require.NoError(t, err)
			require.Error(t, s.AppendAnnotationOnce(t.Context(), "MTIX-1", permissionOnceAnnotation()))
			after, err := s.GetNode(t.Context(), "MTIX-1")
			require.NoError(t, err)
			require.Equal(t, original.Annotations, after.Annotations)
			require.Equal(t, before, contractAnnotationCounts(t, raw))
			require.Zero(t, callbacks)
			_, err = raw.ExecContext(t.Context(), `DROP TRIGGER contract_abort`)
			require.NoError(t, err)
			require.NoError(t, s.AppendAnnotationOnce(t.Context(), "MTIX-1", permissionOnceAnnotation()))
			require.Equal(t, 1, callbacks)
			require.Len(t, readEventsByOp(t, raw, model.OpComment), 1)
			committed, err := s.GetNode(t.Context(), "MTIX-1")
			require.NoError(t, err)
			require.Equal(t, []model.Annotation{permissionOnceAnnotation()}, committed.Annotations)
		})
	}
}
func TestAppendAnnotationOnce_InvalidIdentityAndReplayMetadataDoNotMutate(t *testing.T) {
	s, raw := mutationTestStore(t)
	mustCreateNode(t, s, "MTIX-1", "")
	empty := permissionOnceAnnotation()
	empty.ID = ""
	before := contractAnnotationCounts(t, raw)
	require.ErrorIs(t, s.AppendAnnotationOnce(t.Context(), "MTIX-1", empty), model.ErrInvalidInput)
	require.Equal(t, before, contractAnnotationCounts(t, raw))
	require.NoError(t, s.AppendAnnotationOnce(t.Context(), "MTIX-1", permissionOnceAnnotation()))
	before = contractAnnotationCounts(t, raw)
	for _, field := range []string{"author", "addressee", "time"} {
		changed := permissionOnceAnnotation()
		switch field {
		case "author":
			changed.Author = "other"
		case "addressee":
			changed.Addressee = "other"
		case "time":
			changed.CreatedAt = changed.CreatedAt.Add(time.Hour)
		}
		require.ErrorIs(t, s.AppendAnnotationOnce(t.Context(), "MTIX-1", changed), model.ErrConflict)
		require.Equal(t, before, contractAnnotationCounts(t, raw))
	}
	got, err := s.GetNode(t.Context(), "MTIX-1")
	require.NoError(t, err)
	require.Equal(t, []model.Annotation{permissionOnceAnnotation()}, got.Annotations)
}
func TestAppendAnnotationOnce_CorruptStoredArrayAndCanceledContextFailClosed(t *testing.T) {
	s, raw := mutationTestStore(t)
	mustCreateNode(t, s, "MTIX-1", "")
	_, err := raw.ExecContext(t.Context(), `UPDATE nodes SET annotations = ? WHERE id = ?`, `{broken_synthetic_private_fixture`, "MTIX-1")
	require.NoError(t, err)
	before := contractAnnotationCounts(t, raw)
	require.Error(t, s.AppendAnnotationOnce(t.Context(), "MTIX-1", permissionOnceAnnotation()))
	var stored string
	require.NoError(t, raw.QueryRowContext(t.Context(), `SELECT annotations FROM nodes WHERE id = ?`, "MTIX-1").Scan(&stored))
	require.Equal(t, `{broken_synthetic_private_fixture`, stored)
	require.Equal(t, before, contractAnnotationCounts(t, raw))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, s.AppendAnnotationOnce(ctx, "MTIX-1", permissionOnceAnnotation()), context.Canceled)
	require.Equal(t, before, contractAnnotationCounts(t, raw))
	require.ErrorIs(t, s.AppendAnnotationOnce(t.Context(), "MTIX-404", permissionOnceAnnotation()), model.ErrNotFound)
}
