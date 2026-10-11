// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Exchange snapshots retain local journal and cursor identity in the board read.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/require"
)

func exchangeSnapshotStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(filepath.Join(t.TempDir(), "exchange.db"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	return s
}

func TestExportExchangeSnapshot_OwnJournalAndAppliedDuplicates_AreEligible(t *testing.T) {
	s := exchangeSnapshotStore(t)
	require.NoError(t, s.CreateNode(t.Context(), &model.Node{ID: "TEST-1", Project: "TEST", Seq: 1, Title: "own", Status: model.StatusOpen, Priority: 3, Weight: 1}))
	var own string
	require.NoError(t, s.QueryRow(t.Context(), `SELECT event_id FROM sync_events LIMIT 1`).Scan(&own))
	data, b, err := s.ExportExchangeSnapshot(t.Context(), "TEST", "reference", []string{own, own}, 10000)
	require.NoError(t, err)
	require.Equal(t, int64(0), b.Unresolved)
	require.Equal(t, 1, data.NodeCount)
	require.Positive(t, b.LocalSequence)
	var applied int
	require.NoError(t, s.QueryRow(t.Context(), `SELECT COUNT(*) FROM applied_events WHERE event_id=?`, own).Scan(&applied))
	require.Zero(t, applied, "own journal may qualify without applied marker")
}

func TestExportExchangeSnapshot_QuarantinePrecedesKnownJournal(t *testing.T) {
	s := exchangeSnapshotStore(t)
	require.NoError(t, s.CreateNode(t.Context(), &model.Node{ID: "TEST-1", Project: "TEST", Seq: 1, Title: "own", Status: model.StatusOpen, Priority: 3, Weight: 1}))
	var id string
	require.NoError(t, s.QueryRow(t.Context(), `SELECT event_id FROM sync_events LIMIT 1`).Scan(&id))
	require.NoError(t, s.WithTx(t.Context(), func(tx *sql.Tx) error {
		return QuarantineEvent(t.Context(), tx, QuarantinedEvent{EventID: id, Source: "pull", RawEvent: `{}`, Reason: "held", FirstSeen: "2026-10-01T00:00:00Z", LastAttempt: "2026-10-01T00:00:00Z"})
	}))
	_, b, err := s.ExportExchangeSnapshot(t.Context(), "TEST", "reference", []string{id}, 10000)
	require.NoError(t, err)
	require.Equal(t, int64(1), b.Unresolved)
	_, b, err = s.ExportExchangeSnapshot(t.Context(), "TEST", "reference", []string{}, 10000)
	require.NoError(t, err)
	require.Zero(t, b.Unresolved, "unrelated held row is not encountered")
	require.NoError(t, s.WithTx(t.Context(), func(tx *sql.Tx) error { return RemoveQuarantined(t.Context(), tx, id) }))
	_, b, err = s.ExportExchangeSnapshot(t.Context(), "TEST", "reference", []string{id, "missing"}, 10000)
	require.NoError(t, err)
	require.Equal(t, int64(1), b.Unresolved, "missing requested ID remains unresolved")
}

func TestExportExchangeSnapshot_BoundarySharesBoardWALSnapshot(t *testing.T) {
	s := exchangeSnapshotStore(t)
	ready := make(chan struct{})
	written := make(chan struct{})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		select {
		case <-ready:
		case <-ctx.Done():
			finished <- ctx.Err()
			return
		}
		err := s.CreateNode(ctx, &model.Node{ID: "TEST-1", Project: "TEST", Seq: 1, Title: "after snapshot", Status: model.StatusOpen, Priority: 3, Weight: 1})
		close(written)
		finished <- err
	}()
	t.Cleanup(func() { cancel(); require.NoError(t, <-finished) })
	data, err := s.readExportSnapshotWith(ctx, func(tx *sql.Tx) error {
		close(ready)
		<-written
		b, err := readExchangeBoundary(ctx, tx)
		if err == nil {
			require.Zero(t, b.LocalSequence)
		}
		return err
	})
	require.NoError(t, err)
	require.Empty(t, data.Nodes)
	after, b, err := s.ExportExchangeSnapshot(ctx, "TEST", "reference", nil, 10000)
	require.NoError(t, err)
	require.Equal(t, 1, after.NodeCount)
	require.Positive(t, b.LocalSequence)
}

func TestExportExchangeSnapshot_AllObservedBoundaryFields_AreBound(t *testing.T) {
	s := exchangeSnapshotStore(t)
	values := map[string]string{"meta.sync.last_pulled_clock": "7", "meta.sync.last_pulled_event_id": "event-7", "meta.sync.last_sweep_at": "2026-10-01T00:00:00Z", "meta.sync.sweep_after_id": "listed", "meta.sync.sweep_after_created_at": "2026-10-01T00:00:01Z", "meta.sync.sweep_started_at": "2026-10-01T00:00:02Z"}
	require.NoError(t, s.WithTx(t.Context(), func(tx *sql.Tx) error {
		for key, value := range values {
			if _, err := tx.ExecContext(t.Context(), `UPDATE meta SET value=? WHERE key=?`, value, key); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(t.Context(), `INSERT INTO sync_sweep_pending(event_id,lamport_clock) VALUES (?,?)`, "staged", 1)
		return err
	}))
	_, b, err := s.ExportExchangeSnapshot(t.Context(), "TEST", "reference", nil, 10000)
	require.NoError(t, err)
	require.Equal(t, int64(7), b.PullClock)
	require.Equal(t, "event-7", b.PullEventID)
	require.Equal(t, values["meta.sync.last_sweep_at"], b.LastSweepAt)
	require.Equal(t, "listed", b.SweepAfterID)
	require.Equal(t, values["meta.sync.sweep_after_created_at"], b.SweepAfterCreatedAt)
	require.Equal(t, values["meta.sync.sweep_started_at"], b.SweepStartedAt)
	require.Equal(t, int64(1), b.Pending)
}

func TestExportExchangeSnapshot_InvalidBudgetCursorAndContext_ReturnErrors(t *testing.T) {
	s := exchangeSnapshotStore(t)
	for _, tc := range []struct {
		ids []string
		cap int64
	}{{nil, 0}, {nil, -1}, {[]string{""}, 100}, {[]string{strings.Repeat("x", 129)}, 1000}, {[]string{"id"}, 65}} {
		_, _, err := s.ExportExchangeSnapshot(t.Context(), "", "", tc.ids, tc.cap)
		require.Error(t, err)
	}
	_, err := s.WriteDB().ExecContext(t.Context(), `UPDATE meta SET value=? WHERE key=?`, "bad", "meta.sync.last_pulled_clock")
	require.NoError(t, err)
	_, _, err = s.ExportExchangeSnapshot(t.Context(), "", "", nil, 1000)
	require.Error(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _, err = s.ExportExchangeSnapshot(ctx, "", "", nil, 1000)
	require.ErrorIs(t, err, context.Canceled)
	require.NoError(t, s.Close())
	_, _, err = s.ExportExchangeSnapshot(t.Context(), "", "", nil, 1000)
	require.Error(t, err)
}

func TestExportExchangeSnapshot_ManyIDs_UsesBoundedQueryChunks(t *testing.T) {
	s := exchangeSnapshotStore(t)
	ids := make([]string, 501)
	for i := range ids {
		ids[i] = "missing"
	}
	_, b, err := s.ExportExchangeSnapshot(t.Context(), "", "", ids, 100000)
	require.NoError(t, err)
	require.Equal(t, int64(501), b.Unresolved)
	raw, err := json.Marshal(b)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"local_sequence":0`)
}
