// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/require"
)

// Tests of the sticky hub-connected mark (MTIX-112.17.3, ADR-007 §3.5, A9).

func foreignCreate(t *testing.T, id string, lamport int64) *model.SyncEvent {
	t.Helper()
	return makeApplyEvent(t, model.OpCreateNode, id, "alice", lamport,
		&model.CreateNodePayload{Title: "x"})
}

func TestIdempotentApply_ForeignEvent_SetsHubMarkInSameTx(t *testing.T) {
	s, raw := applyTestStore(t)
	ctx := context.Background()
	e := foreignCreate(t, "MTIX-1", 1)

	var inTx string
	require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error {
		if err := s.IdempotentApply(ctx, tx, e); err != nil {
			return err
		}
		// Visible to the applying transaction before it commits.
		return tx.QueryRowContext(ctx,
			`SELECT source FROM board_flags WHERE key = 'hub_connected'`).Scan(&inTx)
	}))
	require.Equal(t, "foreign_event", inTx)

	mark, err := s.HubMark(ctx)
	require.NoError(t, err)
	require.True(t, mark.Set)
	require.Equal(t, "foreign_event", mark.Source)
	require.NotEmpty(t, mark.SetAt)
	var rows int
	require.NoError(t, raw.QueryRow(`SELECT COUNT(*) FROM board_flags WHERE key = 'hub_connected'`).Scan(&rows))
	require.Equal(t, 1, rows)
}

func TestIdempotentApply_SecondForeignEvent_KeepsFirstMark(t *testing.T) {
	s, _ := applyTestStore(t)
	ctx := context.Background()
	require.NoError(t, applyOnce(t, s, foreignCreate(t, "MTIX-1", 1)))
	first, err := s.HubMark(ctx)
	require.NoError(t, err)
	flipped, err := s.MarkHubConnected(ctx, "config")
	require.NoError(t, err)
	require.False(t, flipped, "an existing mark is never rewritten")
	require.NoError(t, applyOnce(t, s, foreignCreate(t, "MTIX-2", 2)))
	second, err := s.HubMark(ctx)
	require.NoError(t, err)
	require.Equal(t, first, second)
}

func TestIdempotentApply_DuplicateAndOwnEvents_DoNotSetHubMark(t *testing.T) {
	s, raw := applyTestStore(t)
	ctx := context.Background()
	e := foreignCreate(t, "MTIX-1", 1)
	require.NoError(t, applyOnce(t, s, e))
	_, err := s.ResetHubMark(ctx)
	require.NoError(t, err)

	// Duplicate: the event id is already in applied_events.
	require.NoError(t, applyOnce(t, s, e))
	mark, err := s.HubMark(ctx)
	require.NoError(t, err)
	require.False(t, mark.Set, "a duplicate is not new evidence")

	// Own event: this replica holds the event in sync_events but has no
	// applied_events row for it.
	_, err = raw.Exec(`DELETE FROM applied_events WHERE event_id = ?`, e.EventID)
	require.NoError(t, err)
	require.NoError(t, applyOnce(t, s, e))
	mark, err = s.HubMark(ctx)
	require.NoError(t, err)
	require.False(t, mark.Set, "an own event is not foreign evidence")
}

func TestIdempotentApply_FailedApply_MarkRolledBack(t *testing.T) {
	s, _ := applyTestStore(t)
	ctx := context.Background()
	boom := errors.New("later step failed")
	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		if applyErr := s.IdempotentApply(ctx, tx, foreignCreate(t, "MTIX-1", 1)); applyErr != nil {
			return applyErr
		}
		return boom
	})
	require.ErrorIs(t, err, boom)
	mark, err := s.HubMark(ctx)
	require.NoError(t, err)
	require.False(t, mark.Set, "the mark rolls back with the apply")
}

func TestMarkHubConnected_Sources(t *testing.T) {
	tests := []struct {
		source  string
		wantErr bool
	}{
		{"foreign_event", false}, {"applied_events", false}, {"config", false},
		{"secrets", false}, {"env_dsn", false}, {"", true}, {"hub", true},
	}
	for _, tt := range tests {
		t.Run("source="+tt.source, func(t *testing.T) {
			s, _ := applyTestStore(t)
			flipped, err := s.MarkHubConnected(context.Background(), tt.source)
			if tt.wantErr {
				require.ErrorIs(t, err, model.ErrInvalidInput)
				return
			}
			require.NoError(t, err)
			require.True(t, flipped)
		})
	}
}

func TestHubMark_FreshStore_NotSet(t *testing.T) {
	s, _ := applyTestStore(t)
	mark, err := s.HubMark(context.Background())
	require.NoError(t, err)
	require.False(t, mark.Set)
}

func TestInferHubMark_AppliedRows_SetsMarkOnce(t *testing.T) {
	s, raw := applyTestStore(t)
	ctx := context.Background()
	flipped, err := s.InferHubMark(ctx)
	require.NoError(t, err)
	require.False(t, flipped, "no applied_events row, no evidence")

	// A 0.5.x applier writes applied_events without the mark.
	_, err = raw.Exec(`INSERT INTO applied_events(event_id, applied_at, applied_by_lamport) VALUES('e1','2026-01-01T00:00:00Z',1)`)
	require.NoError(t, err)
	has, err := s.AppliedEventsAboveFloor(ctx)
	require.NoError(t, err)
	require.True(t, has)
	flipped, err = s.InferHubMark(ctx)
	require.NoError(t, err)
	require.True(t, flipped)
	mark, err := s.HubMark(ctx)
	require.NoError(t, err)
	require.Equal(t, "applied_events", mark.Source)
	flipped, err = s.InferHubMark(ctx)
	require.NoError(t, err)
	require.False(t, flipped)
}

func TestResetHubMark_FloorPreventsReinference(t *testing.T) {
	s, raw := applyTestStore(t)
	ctx := context.Background()
	for _, id := range []string{"e1", "e2"} {
		_, err := raw.Exec(`INSERT INTO applied_events(event_id, applied_at, applied_by_lamport) VALUES(?,'2026-01-01T00:00:00Z',1)`, id)
		require.NoError(t, err)
	}
	_, err := s.InferHubMark(ctx)
	require.NoError(t, err)

	floor, err := s.ResetHubMark(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(2), floor)
	mark, err := s.HubMark(ctx)
	require.NoError(t, err)
	require.False(t, mark.Set)

	// The next pass does not undo the reset.
	has, err := s.AppliedEventsAboveFloor(ctx)
	require.NoError(t, err)
	require.False(t, has)
	flipped, err := s.InferHubMark(ctx)
	require.NoError(t, err)
	require.False(t, flipped)
	mark, err = s.HubMark(ctx)
	require.NoError(t, err)
	require.False(t, mark.Set)
}

func TestResetHubMark_NewAppliedRowsAfterReset_MarkAgain(t *testing.T) {
	s, raw := applyTestStore(t)
	ctx := context.Background()
	_, err := raw.Exec(`INSERT INTO applied_events(event_id, applied_at, applied_by_lamport) VALUES('e1','2026-01-01T00:00:00Z',1)`)
	require.NoError(t, err)
	_, err = s.ResetHubMark(ctx)
	require.NoError(t, err)

	_, err = raw.Exec(`INSERT INTO applied_events(event_id, applied_at, applied_by_lamport) VALUES('e2','2026-01-01T00:00:00Z',2)`)
	require.NoError(t, err)
	flipped, err := s.InferHubMark(ctx)
	require.NoError(t, err)
	require.True(t, flipped)

	// A second reset records the new maximum.
	floor, err := s.ResetHubMark(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(2), floor)
	require.NoError(t, applyOnce(t, s, foreignCreate(t, "MTIX-1", 1)))
	mark, err := s.HubMark(ctx)
	require.NoError(t, err)
	require.True(t, mark.Set, "a foreign event applied after a reset marks again")
}

func TestResetHubMark_EmptyAppliedEvents_FloorZero(t *testing.T) {
	s, _ := applyTestStore(t)
	floor, err := s.ResetHubMark(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(0), floor)
}

// Error paths: a closed store fails every read and write with a wrapped error.
func TestHubMark_ClosedStore_ReturnsWrappedErrors(t *testing.T) {
	s, _ := applyTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.Close())

	_, err := s.HubMark(ctx)
	require.ErrorContains(t, err, "read hub mark")
	_, err = s.MarkHubConnected(ctx, "config")
	require.ErrorContains(t, err, "mark hub connected")
	_, err = s.AppliedEventsAboveFloor(ctx)
	require.ErrorContains(t, err, "applied events above floor")
	_, err = s.InferHubMark(ctx)
	require.ErrorContains(t, err, "applied events above floor")
	_, err = s.ResetHubMark(ctx)
	require.ErrorContains(t, err, "reset hub mark")
}

// Failures inside the transaction body: the flag table is gone, so each
// statement fails and the error names the step.
func TestHubMark_MissingFlagTable_ReturnsStepErrors(t *testing.T) {
	s, raw := applyTestStore(t)
	ctx := context.Background()
	_, err := raw.Exec(`DROP TABLE board_flags`)
	require.NoError(t, err)

	_, err = s.MarkHubConnected(ctx, "config")
	require.ErrorContains(t, err, "write hub mark")
	_, err = s.ResetHubMark(ctx)
	require.ErrorContains(t, err, "clear hub mark")
	err = applyOnce(t, s, foreignCreate(t, "MTIX-1", 1))
	require.ErrorContains(t, err, "hub mark")
}

func TestResetHubMark_MissingAppliedEvents_ReportsFloorRead(t *testing.T) {
	s, raw := applyTestStore(t)
	_, err := raw.Exec(`DROP TABLE applied_events`)
	require.NoError(t, err)
	_, err = s.ResetHubMark(context.Background())
	require.ErrorContains(t, err, "read applied events floor")
}

func TestResetHubMark_FloorWriteFails_RollsBackTheClear(t *testing.T) {
	s, raw := applyTestStore(t)
	ctx := context.Background()
	_, err := s.MarkHubConnected(ctx, "config")
	require.NoError(t, err)
	// Reject the floor row so the second statement of the reset fails.
	_, err = raw.Exec(`CREATE TRIGGER no_floor BEFORE INSERT ON board_flags WHEN NEW.key = 'hub_mark_floor'
		BEGIN SELECT RAISE(ABORT, 'floor rejected'); END`)
	require.NoError(t, err)
	_, err = s.ResetHubMark(ctx)
	require.ErrorContains(t, err, "record hub mark floor")
	mark, err := s.HubMark(ctx)
	require.NoError(t, err)
	require.True(t, mark.Set, "a failed reset leaves the mark in place")
}

// The failure lands in dispatch, after the mark was written: the whole apply
// rolls back, including the mark.
func TestIdempotentApply_DispatchFails_MarkRolledBack(t *testing.T) {
	s, _ := applyTestStore(t)
	ctx := context.Background()
	e := foreignCreate(t, "MTIX-1", 1)
	e.Payload = []byte(`{"title":`) // invalid JSON: the create payload cannot decode
	err := s.WithTx(ctx, func(tx *sql.Tx) error { return s.IdempotentApply(ctx, tx, e) })
	require.Error(t, err, "an undecodable create payload must fail the apply")
	mark, markErr := s.HubMark(ctx)
	require.NoError(t, markErr)
	require.False(t, mark.Set)
}
