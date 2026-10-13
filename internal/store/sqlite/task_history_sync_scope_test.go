package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// an event that names its task by uid after a local renumber.
func TestTaskHistorySync_EventForARenumberedTask_RecordsTheResolvedTask(t *testing.T) {
	s, raw := applyTestStore(t)
	ctx := context.Background()
	create := makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "alice", 1, &model.CreateNodePayload{Title: "old", Priority: model.Priority(3)})
	require.NoError(t, applyOnce(t, s, create))
	var uid string
	require.NoError(t, raw.QueryRow(`SELECT COALESCE(uid,'') FROM nodes WHERE id='MTIX-1'`).Scan(&uid))
	require.NotEmpty(t, uid)
	require.NoError(t, s.RenumberSubtree(ctx, "MTIX-1", 7))
	ev := makeApplyEvent(t, model.OpUpdateField, "MTIX-1", "alice", 2, &model.UpdateFieldPayload{FieldName: "priority", NewValue: json.RawMessage(`5`)})
	ev.UID = uid
	require.NoError(t, applyOnce(t, s, ev))
	var prio int
	require.NoError(t, raw.QueryRow(`SELECT priority FROM nodes WHERE uid=?`, uid).Scan(&prio))
	require.Equal(t, 5, prio, "the apply reached the renumbered task")
	fields, _, _ := syncHistoryFields(t, raw, ev.EventID)
	require.Equal(t, []string{"priority"}, fields, "the uid-resolved change must be recorded")
}

// replay in a second transaction after the first apply's transaction rolled back.
func TestTaskHistorySync_ReplayAfterRollback_RecordsOnce(t *testing.T) {
	s, raw := applyTestStore(t)
	ctx := context.Background()
	create := makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "alice", 1, &model.CreateNodePayload{Title: "old", Priority: model.Priority(3)})
	require.NoError(t, applyOnce(t, s, create))
	ev := makeApplyEvent(t, model.OpUpdateField, "MTIX-1", "alice", 2, &model.UpdateFieldPayload{FieldName: "priority", NewValue: json.RawMessage(`5`)})
	errBoom := model.ErrConflict
	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		if err := s.IdempotentApply(ctx, tx, ev); err != nil {
			return err
		}
		return errBoom
	})
	require.ErrorIs(t, err, errBoom)
	require.NoError(t, applyOnce(t, s, ev))
	require.NoError(t, applyOnce(t, s, ev))
	var n int
	require.NoError(t, raw.QueryRow(`SELECT COUNT(*) FROM task_history WHERE event_id=?`, ev.EventID).Scan(&n))
	require.Equal(t, 1, n)
}

// DiscardLocal's statements followed by a re-pull of the same events.
func TestTaskHistorySync_ReplayAfterDiscardLocal_RecordsEachEventOnce(t *testing.T) {
	s, raw := applyTestStore(t)
	ctx := context.Background()
	mtixDir := t.TempDir()
	create := makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "alice", 1, &model.CreateNodePayload{Title: "old", Priority: model.Priority(3)})
	ev := makeApplyEvent(t, model.OpUpdateField, "MTIX-1", "alice", 2, &model.UpdateFieldPayload{FieldName: "priority", NewValue: json.RawMessage(`5`)})
	require.NoError(t, applyOnce(t, s, create))
	require.NoError(t, applyOnce(t, s, ev))
	derr := DiscardLocal(ctx, s, mtixDir)
	require.NoError(t, derr)
	require.NoError(t, applyOnce(t, s, create))
	require.NoError(t, applyOnce(t, s, ev))
	var n int
	require.NoError(t, raw.QueryRow(`SELECT COUNT(*) FROM task_history WHERE event_id=?`, ev.EventID).Scan(&n))
	require.Equal(t, 1, n, "a re-pulled event_id must not be recorded twice")
}
