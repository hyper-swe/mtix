// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/stretchr/testify/require"
)

func outboundNode() *model.Node {
	return &model.Node{ID: "TEST-1", Project: "TEST", Seq: 1, Title: "local", Status: model.StatusOpen, Priority: 3, Weight: 1}
}

func outboundSnapshotClock() time.Time {
	return time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
}

func outboundReadContext(ctx context.Context, t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	if deadline, ok := t.Deadline(); ok {
		return context.WithDeadline(ctx, deadline)
	}
	return context.WithCancel(ctx)
}

func outboundWire(t *testing.T, boundary ExchangeBoundary) int64 {
	t.Helper()
	raw, err := json.Marshal(boundary)
	require.NoError(t, err)
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &fields))
	require.Contains(t, fields, "pending_outbound", "snapshot must explicitly distinguish known zero from legacy unknown")
	require.NotEqual(t, "null", string(fields["pending_outbound"]))
	var pending int64
	require.NoError(t, json.Unmarshal(fields["pending_outbound"], &pending))
	return pending
}

func TestExportExchangeSnapshot_OutboundJournal_ReportsKnownCount(t *testing.T) {
	for _, tc := range []struct {
		name         string
		rows         int
		pushed, held bool
	}{
		{"empty", 0, false, false}, {"create", 1, false, false},
		{"create and update", 2, false, false}, {"pushed", 2, true, false}, {"held push", 1, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := exchangeSnapshotStore(t)
			s.SetClock(outboundSnapshotClock)
			if tc.rows > 0 {
				require.NoError(t, s.CreateNode(t.Context(), outboundNode()))
			}
			if tc.rows > 1 {
				title := "edited"
				require.NoError(t, s.UpdateNode(t.Context(), "TEST-1", &store.NodeUpdate{Title: &title}))
			}
			if tc.pushed {
				_, err := s.WriteDB().ExecContext(t.Context(), `UPDATE sync_events SET sync_status=?`, "pushed")
				require.NoError(t, err)
			}
			if tc.held {
				require.NoError(t, holdOutbound(t.Context(), s))
			}
			data, boundary, err := s.ExportExchangeSnapshot(t.Context(), "TEST", "reference", nil, 10000)
			require.NoError(t, err)
			require.Equal(t, min(tc.rows, 1), data.NodeCount)
			require.Equal(t, int64(tc.rows), boundary.LocalSequence)
			expected := int64(tc.rows)
			if tc.pushed {
				expected = 0
			}
			require.Equal(t, expected, outboundWire(t, boundary))
		})
	}
}

func holdOutbound(ctx context.Context, s *Store) error {
	var id string
	if err := s.QueryRow(ctx, `SELECT event_id FROM sync_events LIMIT 1`).Scan(&id); err != nil {
		return err
	}
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		return QuarantineEvent(ctx, tx, QuarantinedEvent{EventID: id, Source: "push", RawEvent: `{}`, Reason: "held", FirstSeen: "2026-10-01T00:00:00Z", LastAttempt: "2026-10-01T00:00:00Z"})
	})
}

func TestExportExchangeSnapshot_ConcurrentOutboundChange_UsesBoardSnapshot(t *testing.T) {
	for _, acknowledge := range []bool{false, true} {
		t.Run(map[bool]string{false: "create", true: "acknowledge"}[acknowledge], func(t *testing.T) {
			s := exchangeSnapshotStore(t)
			s.SetClock(outboundSnapshotClock)
			if acknowledge {
				require.NoError(t, s.CreateNode(t.Context(), outboundNode()))
			}
			ctx, cancel := outboundReadContext(t.Context(), t)
			ready := make(chan struct{})
			written := make(chan struct{})
			finished := make(chan error, 1)
			t.Cleanup(func() { cancel(); require.NoError(t, <-finished) })
			go outboundWriter(ctx, s, acknowledge, ready, written, finished)
			var observed ExchangeBoundary
			data, err := s.readExportSnapshotWith(ctx, func(tx *sql.Tx) error {
				close(ready)
				select {
				case <-written:
				case <-ctx.Done():
					return ctx.Err()
				}
				var boundaryErr error
				observed, boundaryErr = readExchangeBoundary(ctx, tx)
				return boundaryErr
			})
			require.NoError(t, err)
			before := int64(0)
			if acknowledge {
				before = 1
			}
			require.Len(t, data.Nodes, int(before))
			require.Equal(t, before, outboundWire(t, observed))
			_, after, err := s.ExportExchangeSnapshot(ctx, "TEST", "reference", nil, 10000)
			require.NoError(t, err)
			require.Equal(t, 1-before, outboundWire(t, after))
		})
	}
}

func outboundWriter(ctx context.Context, s *Store, acknowledge bool, ready <-chan struct{}, written chan<- struct{}, finished chan<- error) {
	select {
	case <-ready:
	case <-ctx.Done():
		finished <- ctx.Err()
		return
	}
	var err error
	if acknowledge {
		_, err = s.WriteDB().ExecContext(ctx, `UPDATE sync_events SET sync_status=?`, "pushed")
	} else {
		err = s.CreateNode(ctx, outboundNode())
	}
	close(written)
	finished <- err
}

func TestReadExchangeBoundary_CanceledAggregate_LeavesOutboundUnknown(t *testing.T) {
	s := exchangeSnapshotStore(t)
	s.SetClock(outboundSnapshotClock)
	require.NoError(t, s.CreateNode(t.Context(), outboundNode()))
	var observed ExchangeBoundary
	data, err := s.readExportSnapshotWith(t.Context(), func(tx *sql.Tx) error {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		var readErr error
		observed, readErr = readExchangeBoundary(ctx, tx)
		require.ErrorIs(t, readErr, context.Canceled)
		return nil
	})
	require.NoError(t, err)
	require.Len(t, data.Nodes, 1)
	raw, err := json.Marshal(observed)
	require.NoError(t, err)
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &fields))
	require.NotContains(t, fields, "pending_outbound", "failed aggregate must not manufacture known zero")
}
