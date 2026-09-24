// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/require"
)

// Unknown or missing transition targets must not wedge a pull (MTIX-95.10,
// review round 2). A pull applies a batch in one transaction and advances
// its cursor only when the batch commits, so an event that fails fails every
// later pull the same way. A status added by a newer client, or a payload
// without a target, therefore never fails the batch: an unknown non-empty
// status is written to the status column alone, a missing target changes no
// node column, and both are recorded as applied with a warning.

// captureDefaultLog routes the default slog logger to a buffer for one test.
func captureDefaultLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// seedSecondaryColumns sets sentinel values in the workflow columns of id
// that a transition could write, so a test can prove they did not change.
func seedSecondaryColumns(t *testing.T, raw *sql.DB, id string) {
	t.Helper()
	// Test seam: sentinels in every column besides status and updated_at.
	_, err := raw.Exec(`UPDATE nodes SET assignee = 'seed-agent', agent_state = 'seed-state',
		previous_status = 'seed-prev', closed_at = '2000-01-01T00:00:00Z', progress = 0.25,
		defer_until = '2000-01-02T00:00:00Z' WHERE id = ?`, id)
	require.NoError(t, err)
}

// workflowColumns returns the workflow columns of id, NULL as "<NULL>".
func workflowColumns(t *testing.T, raw *sql.DB, id string) map[string]string {
	t.Helper()
	cols := []string{"status", "assignee", "agent_state", "previous_status",
		"closed_at", "progress", "defer_until", "updated_at"}
	vals := make([]sql.NullString, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	// Every column a workflow event can write, for before/after comparison.
	require.NoError(t, raw.QueryRow(`SELECT status, assignee, agent_state, previous_status,
		closed_at, progress, defer_until, updated_at FROM nodes WHERE id = ?`, id).Scan(ptrs...))
	out := make(map[string]string, len(cols))
	for i, c := range cols {
		out[c] = "<NULL>"
		if vals[i].Valid {
			out[c] = vals[i].String
		}
	}
	return out
}

// applyBatch applies evs in one transaction, the way a pull applies a page.
func applyBatch(s *Store, evs ...*model.SyncEvent) error {
	ctx := context.Background()
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		for _, e := range evs {
			if err := IdempotentApply(ctx, tx, e); err != nil {
				return err
			}
		}
		return nil
	})
}

// startEvent is a normal winning transition that follows the odd event in
// each batch; it must still apply.
func startEvent(t *testing.T) *model.SyncEvent {
	t.Helper()
	return makeApplyEvent(t, model.OpTransitionStatus, "MTIX-2", "bob", 3,
		&model.TransitionStatusPayload{From: model.StatusOpen, To: model.StatusInProgress})
}

// TestApply_TransitionToUnknownStatus_InBatch_WritesStatusOnlyAndPullContinues
// covers a status this build does not know (for example one added by a newer
// client): the status column takes it, no other workflow column changes, a
// warning names the event and the status, and the rest of the batch applies.
func TestApply_TransitionToUnknownStatus_InBatch_WritesStatusOnlyAndPullContinues(t *testing.T) {
	s, raw := applyTestStore(t)
	createNodeFor(t, s, "MTIX-1")
	createNodeFor(t, s, "MTIX-2")
	seedSecondaryColumns(t, raw, "MTIX-1")
	before := workflowColumns(t, raw, "MTIX-1")
	logs := captureDefaultLog(t)
	odd := makeApplyEvent(t, model.OpTransitionStatus, "MTIX-1", "alice", 2,
		&model.TransitionStatusPayload{From: model.StatusOpen, To: "archived"})

	require.NoError(t, applyBatch(s, odd, startEvent(t)), "the batch must not fail")

	after := workflowColumns(t, raw, "MTIX-1")
	require.Equal(t, "archived", after["status"], "the unknown status is written")
	for _, col := range []string{"assignee", "agent_state", "previous_status", "closed_at", "progress", "defer_until"} {
		require.Equal(t, before[col], after[col], "column %s is not written", col)
	}
	require.Equal(t, string(model.StatusInProgress), readNodeColumn(t, raw, "MTIX-2", "status"),
		"the event after it still applies")
	require.Equal(t, 4, countApplied(t, raw), "two creates and both transitions are recorded")
	require.Contains(t, logs.String(), odd.EventID)
	require.Contains(t, logs.String(), "archived")
}

// TestApply_TransitionWithoutTarget_InBatch_ChangesNothingAndPullContinues
// covers a transition_status whose payload has no usable to-status (missing,
// null, empty) or cannot be decoded: no node column changes, the event is
// recorded as applied with a warning naming it, and the rest of the batch
// applies.
func TestApply_TransitionWithoutTarget_InBatch_ChangesNothingAndPullContinues(t *testing.T) {
	tests := []struct {
		name    string
		payload string
	}{
		{"to missing", `{"from":"open"}`},
		{"to null", `{"from":"open","to":null}`},
		{"to empty", `{"from":"open","to":""}`},
		{"payload null", `null`},
		{"payload not JSON", `<<<not-json`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, raw := applyTestStore(t)
			createNodeFor(t, s, "MTIX-1")
			createNodeFor(t, s, "MTIX-2")
			seedSecondaryColumns(t, raw, "MTIX-1")
			before := workflowColumns(t, raw, "MTIX-1")
			logs := captureDefaultLog(t)
			odd := makeApplyEvent(t, model.OpTransitionStatus, "MTIX-1", "alice", 2, nil)
			odd.Payload = json.RawMessage(tt.payload)

			require.NoError(t, applyBatch(s, odd, startEvent(t)), "the batch must not fail")

			require.Equal(t, before, workflowColumns(t, raw, "MTIX-1"), "no node column changes")
			require.Equal(t, string(model.StatusInProgress), readNodeColumn(t, raw, "MTIX-2", "status"),
				"the event after it still applies")
			var n int
			require.NoError(t, raw.QueryRow(
				`SELECT COUNT(*) FROM applied_events WHERE event_id = ?`, odd.EventID).Scan(&n))
			require.Equal(t, 1, n, "the event is recorded as applied")
			require.Contains(t, logs.String(), odd.EventID)
		})
	}
}
