// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Outbound completion tests preserve receipts while local journal work remains.
package service

import (
	"context"
	"database/sql"
	"os"
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setOutboundSweep(ctx context.Context, s *SyncService) error {
	_, err := s.store.WriteDB().ExecContext(ctx, `UPDATE meta SET value=? WHERE key=?`, "2026-10-01T00:00:00Z", "meta.sync.last_sweep_at")
	return err
}

func TestExchangePull_PendingOutbound_PreservesReceiptAndRecordsReason(t *testing.T) {
	for _, prior := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing prior", true: "prior bytes"}[prior], func(t *testing.T) {
			s, dir := exchangeFixture(t)
			var before []byte
			if prior {
				exchangePublishReference(t, s, dir)
				var err error
				before, err = os.ReadFile(exchangePath(dir))
				require.NoError(t, err)
			}
			require.NoError(t, s.store.CreateNode(t.Context(), &model.Node{ID: "TEST-2", Project: "TEST", Seq: 2, Title: "unpublished local", Status: model.StatusOpen, Priority: 3, Weight: 1}))
			require.NoError(t, setOutboundSweep(t.Context(), s))
			owner, err := s.BeginExchangePull(t.Context(), dir)
			require.NoError(t, err)
			t.Cleanup(owner.Close)
			require.NoError(t, owner.Complete(t.Context(), ExchangePullCompletion{CursorDrained: true, SweepCompleted: true}))
			owner.Close()
			after, readErr := os.ReadFile(exchangePath(dir))
			if prior {
				require.NoError(t, readErr)
				assert.Equal(t, before, after)
			} else {
				assert.ErrorIs(t, readErr, os.ErrNotExist)
			}
			status, err := s.ExchangeRecordStatus(t.Context(), dir)
			require.NoError(t, err)
			assert.Equal(t, prior, status.Present)
			assert.Equal(t, "pending-outbound", status.Reason)
			var pending int
			require.NoError(t, s.store.QueryRow(t.Context(), `SELECT COUNT(*) FROM sync_events WHERE sync_status=?`, "pending").Scan(&pending))
			require.Equal(t, 1, pending, "completion cannot acknowledge locally authored work")
		})
	}
}

func TestExchangePull_PendingReasonWriteFails_ReturnsErrorAndPreservesReceipt(t *testing.T) {
	s, dir := exchangeFixture(t)
	exchangePublishReference(t, s, dir)
	before, err := os.ReadFile(exchangePath(dir))
	require.NoError(t, err)
	require.NoError(t, s.store.CreateNode(t.Context(), &model.Node{ID: "TEST-2", Project: "TEST", Seq: 2, Title: "local", Status: model.StatusOpen, Priority: 3, Weight: 1}))
	require.NoError(t, setOutboundSweep(t.Context(), s))
	_, err = s.store.WriteDB().ExecContext(t.Context(), `CREATE TRIGGER outbound_reason_fail BEFORE INSERT ON meta WHEN NEW.key='meta.sync.exchange_record.reason' BEGIN SELECT RAISE(FAIL,'diagnostic refused'); END`)
	require.NoError(t, err)
	owner, err := s.BeginExchangePull(t.Context(), dir)
	require.NoError(t, err)
	t.Cleanup(owner.Close)
	require.ErrorContains(t, owner.Complete(t.Context(), ExchangePullCompletion{CursorDrained: true, SweepCompleted: true}), "diagnostic refused")
	owner.Close()
	after, err := os.ReadFile(exchangePath(dir))
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestExchangePull_HeldOutbound_PreservesReceiptAndRecordsPending(t *testing.T) {
	s, dir := exchangeFixture(t)
	exchangePublishReference(t, s, dir)
	before, err := os.ReadFile(exchangePath(dir))
	require.NoError(t, err)
	require.NoError(t, s.store.CreateNode(t.Context(), &model.Node{ID: "TEST-2", Project: "TEST", Seq: 2, Title: "held local", Status: model.StatusOpen, Priority: 3, Weight: 1}))
	var id string
	require.NoError(t, s.store.QueryRow(t.Context(), `SELECT event_id FROM sync_events LIMIT 1`).Scan(&id))
	require.NoError(t, s.store.WithTx(t.Context(), func(tx *sql.Tx) error {
		return sqlite.QuarantineEvent(t.Context(), tx, sqlite.QuarantinedEvent{EventID: id, Source: "push", RawEvent: `{}`, Reason: "held", FirstSeen: "2026-10-01T00:00:00Z", LastAttempt: "2026-10-01T00:00:00Z"})
	}))
	require.NoError(t, setOutboundSweep(t.Context(), s))
	owner, err := s.BeginExchangePull(t.Context(), dir)
	require.NoError(t, err)
	t.Cleanup(owner.Close)
	require.NoError(t, owner.Complete(t.Context(), ExchangePullCompletion{CursorDrained: true, SweepCompleted: true}))
	owner.Close()
	status, err := s.ExchangeRecordStatus(t.Context(), dir)
	require.NoError(t, err)
	require.Equal(t, "pending-outbound", status.Reason)
	after, err := os.ReadFile(exchangePath(dir))
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestExchangePull_OutboundSnapshotFailure_PreservesCauseAndReceipt(t *testing.T) {
	for _, closed := range []bool{false, true} {
		t.Run(map[bool]string{false: "canceled", true: "closed store"}[closed], func(t *testing.T) {
			s, dir := exchangeFixture(t)
			exchangePublishReference(t, s, dir)
			before, err := os.ReadFile(exchangePath(dir))
			require.NoError(t, err)
			require.NoError(t, s.store.CreateNode(t.Context(), &model.Node{ID: "TEST-2", Project: "TEST", Seq: 2, Title: "local", Status: model.StatusOpen, Priority: 3, Weight: 1}))
			require.NoError(t, setOutboundSweep(t.Context(), s))
			owner, err := s.BeginExchangePull(t.Context(), dir)
			require.NoError(t, err)
			t.Cleanup(owner.Close)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if closed {
				require.NoError(t, s.store.Close())
			} else {
				cancel()
			}
			err = owner.Complete(ctx, ExchangePullCompletion{CursorDrained: true, SweepCompleted: true})
			if closed {
				require.ErrorContains(t, err, "closed")
			} else {
				require.ErrorIs(t, err, context.Canceled)
			}
			owner.Close()
			after, err := os.ReadFile(exchangePath(dir))
			require.NoError(t, err)
			require.Equal(t, before, after)
		})
	}
}

func TestExchangePull_SameIDAppliedAndPending_WithholdsReceipt(t *testing.T) {
	s, dir := exchangeFixture(t)
	exchangePublishReference(t, s, dir)
	before, err := os.ReadFile(exchangePath(dir))
	require.NoError(t, err)
	require.NoError(t, s.store.CreateNode(t.Context(), &model.Node{ID: "TEST-2", Project: "TEST", Seq: 2, Title: "local", Status: model.StatusOpen, Priority: 3, Weight: 1}))
	pending, err := s.store.ReadPendingEvents(t.Context(), 2)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	payload := string(pending[0].Payload)
	require.NoError(t, s.store.WithTx(t.Context(), func(tx *sql.Tx) error { return s.store.IdempotentApply(t.Context(), tx, pending[0]) }))
	var applied int
	require.NoError(t, s.store.QueryRow(t.Context(), `SELECT COUNT(*) FROM applied_events WHERE event_id=?`, pending[0].EventID).Scan(&applied))
	require.Equal(t, 1, applied)
	var state, retained string
	require.NoError(t, s.store.QueryRow(t.Context(), `SELECT sync_status,payload FROM sync_events WHERE event_id=?`, pending[0].EventID).Scan(&state, &retained))
	require.Equal(t, "pending", state)
	require.Equal(t, payload, retained)
	require.NoError(t, setOutboundSweep(t.Context(), s))
	owner, err := s.BeginExchangePull(t.Context(), dir)
	require.NoError(t, err)
	t.Cleanup(owner.Close)
	require.NoError(t, owner.Complete(t.Context(), ExchangePullCompletion{CursorDrained: true, SweepCompleted: true, Encountered: []string{pending[0].EventID}}))
	owner.Close()
	after, err := os.ReadFile(exchangePath(dir))
	require.NoError(t, err)
	require.Equal(t, before, after)
	status, err := s.ExchangeRecordStatus(t.Context(), dir)
	require.NoError(t, err)
	require.Equal(t, "pending-outbound", status.Reason)
}
