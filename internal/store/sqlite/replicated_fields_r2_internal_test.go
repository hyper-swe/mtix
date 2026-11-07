// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package sqlite

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

// Exercise the common emitter, so a new caller of any workflow op cannot omit
// the snapshot even when it constructs a legacy-shaped payload directly.
func TestReplicatedFieldsR2_AllWorkflowEmitsCaptureSnapshot(t *testing.T) {
	for _, op := range model.AllOpTypes {
		if !isWorkflowOp(op) {
			continue
		}
		t.Run(string(op), func(t *testing.T) {
			s, db := applyTestStore(t)
			require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "author", 1, &model.CreateNodePayload{Title: "task"})))
			want := `"2026-11-01T01:02:03.123456789+05:30"`
			require.NoError(t, s.WithTx(t.Context(), func(tx *sql.Tx) error {
				if _, err := tx.Exec("UPDATE nodes SET closed_at=? WHERE id=?", "2026-11-01T01:02:03.123456789+05:30", "MTIX-1"); err != nil {
					return err
				}
				return emitEvent(t.Context(), tx, emitParams{NodeID: "MTIX-1", OpType: op, Payload: json.RawMessage(`{"to":"open","agent_id":"worker"}`)})
			}))
			var payload string
			require.NoError(t, db.QueryRow("SELECT payload FROM sync_events WHERE op_type=?", op).Scan(&payload))
			var fields map[string]json.RawMessage
			require.NoError(t, json.Unmarshal([]byte(payload), &fields))
			require.JSONEq(t, want, string(fields["closed_at"]))
		})
	}
}
func TestReplicatedFieldsR2_EmptyIdentityApplyRollsBack(t *testing.T) {
	for _, body := range []string{"", "note"} {
		t.Run(body, func(t *testing.T) {
			s, db := applyTestStore(t)
			require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "author", 1, &model.CreateNodePayload{Title: "task"})))
			before, err := s.GetNode(t.Context(), "MTIX-1")
			require.NoError(t, err)
			count := countEvents(t, db)
			e := makeApplyEvent(t, model.OpComment, "MTIX-1", "author", 2, &model.CommentPayload{Body: body, Annotation: &model.CommentAnnotationState{CreatedAt: time.Now()}})
			require.ErrorIs(t, applyOnce(t, s, e), model.ErrInvalidInput)
			after, err := s.GetNode(t.Context(), "MTIX-1")
			require.NoError(t, err)
			require.Equal(t, before, after)
			require.Equal(t, count, countEvents(t, db))
		})
	}
}
func TestReplicatedFieldsR2_UIDWinnerOrdersAndTies(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		for _, order := range [][]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}} {
			t.Run(fmt.Sprint(mixed, order), func(t *testing.T) {
				s, _ := applyTestStore(t)
				create := makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "author", 1, &model.CreateNodePayload{Title: "task"})
				create.UID = create.EventID
				require.NoError(t, applyOnce(t, s, create))
				events := []*model.SyncEvent{makeApplyEvent(t, model.OpComment, "MTIX-1", "one", 2, &model.CommentPayload{}), makeApplyEvent(t, model.OpComment, "MTIX-1", "two", 3, &model.CommentPayload{}), makeApplyEvent(t, model.OpComment, "MTIX-1", "three", 3, &model.CommentPayload{})}
				if events[1].EventID > events[2].EventID {
					events[1], events[2] = events[2], events[1]
				}
				at := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
				for i, e := range events {
					e.UID = create.UID
					if mixed && i == 2 {
						e.UID = ""
					}
					e.Payload = mustSnapshotPayload(t, &model.CommentPayload{Body: "note", AuthorID: "original", Annotation: &model.CommentAnnotationState{ID: "stable", CreatedAt: at, Resolved: i != 2, Addressee: "receiver"}})
				}
				for _, i := range order {
					require.NoError(t, applyOnce(t, s, events[i]))
					require.NoError(t, applyOnce(t, s, events[i]))
				}
				got, err := s.GetNode(t.Context(), "MTIX-1")
				require.NoError(t, err)
				require.Equal(t, []model.Annotation{{ID: "stable", Text: "note", Author: "original", CreatedAt: at, Addressee: "receiver"}}, got.Annotations)
			})
		}
	}
}

func TestReplicatedFieldsR2_EmptyIdentityRawEmitRollsBack(t *testing.T) {
	for _, body := range []string{"", "note"} {
		t.Run(body, func(t *testing.T) {
			s, db := applyTestStore(t)
			require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "author", 1, &model.CreateNodePayload{Title: "task"})))
			before, err := s.GetNode(t.Context(), "MTIX-1")
			require.NoError(t, err)
			count := countEvents(t, db)
			payload := mustSnapshotPayload(t, &model.CommentPayload{Body: body, Annotation: &model.CommentAnnotationState{CreatedAt: time.Now()}})
			err = s.WithTx(t.Context(), func(tx *sql.Tx) error {
				if _, err := tx.Exec("UPDATE nodes SET title=? WHERE id=?", "must roll back", "MTIX-1"); err != nil {
					return err
				}
				return emitEvent(t.Context(), tx, emitParams{NodeID: "MTIX-1", OpType: model.OpComment, Payload: payload})
			})
			require.ErrorIs(t, err, model.ErrInvalidInput)
			after, err := s.GetNode(t.Context(), "MTIX-1")
			require.NoError(t, err)
			require.Equal(t, before, after)
			require.Equal(t, count, countEvents(t, db))
		})
	}
}

func TestReplicatedFieldsR2_MalformedWorkflowEmitRollsBack(t *testing.T) {
	for _, op := range model.AllOpTypes {
		if !isWorkflowOp(op) {
			continue
		}
		for _, raw := range []string{"[", "[]", "42"} {
			t.Run(fmt.Sprint(op, raw), func(t *testing.T) {
				s, db := applyTestStore(t)
				require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "author", 1, &model.CreateNodePayload{Title: "task"})))
				count := countEvents(t, db)
				err := s.WithTx(t.Context(), func(tx *sql.Tx) error {
					if _, err := tx.Exec("UPDATE nodes SET title=? WHERE id=?", "must roll back", "MTIX-1"); err != nil {
						return err
					}
					return emitEvent(t.Context(), tx, emitParams{NodeID: "MTIX-1", OpType: op, Payload: json.RawMessage(raw)})
				})
				require.ErrorIs(t, err, model.ErrInvalidInput)
				require.Equal(t, "task", readNodeColumn(t, db, "MTIX-1", "title"))
				require.Equal(t, count, countEvents(t, db))
			})
		}
	}
}
