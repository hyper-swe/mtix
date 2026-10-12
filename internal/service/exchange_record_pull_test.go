// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Service completion retains ownership and reports omitted newer exchanges.
package service

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExchangePull_ClosedOrContendedOwnership_Refused(t *testing.T) {
	s, dir := exchangeFixture(t)
	owner, err := s.BeginExchangePull(t.Context(), dir)
	require.NoError(t, err)
	_, err = s.BeginExchangePull(t.Context(), dir)
	require.ErrorIs(t, err, ErrNoExchangeRecord)
	owner.Close()
	owner.Close()
	require.ErrorIs(t, owner.Complete(t.Context(), ExchangePullCompletion{}), ErrNoExchangeRecord)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = s.BeginExchangePull(ctx, dir)
	require.ErrorIs(t, err, context.Canceled)
}

func TestExchangePull_IncompleteOrUnresolved_PreservesPriorProvenance(t *testing.T) {
	for _, kind := range []string{"cursor", "sweep", "tracking", "unknown ID", "bad cap", "closed store"} {
		t.Run(kind, func(t *testing.T) {
			s, dir := exchangeFixture(t)
			expected := exchangePublishReference(t, s, dir)
			before, err := os.ReadFile(exchangePath(dir))
			require.NoError(t, err)
			owner, err := s.BeginExchangePull(t.Context(), dir)
			require.NoError(t, err)
			completion := ExchangePullCompletion{CursorDrained: true, SweepCompleted: true}
			switch kind {
			case "cursor":
				completion.CursorDrained = false
			case "sweep":
				completion.SweepCompleted = false
			case "tracking":
				completion.Reason = "tracking-limit"
			case "unknown ID":
				completion.Encountered = []string{"missing"}
			case "bad cap":
				s.MaxImportSize = 0
			case "closed store":
				require.NoError(t, s.store.Close())
			}
			err = owner.Complete(t.Context(), completion)
			owner.Close()
			if kind == "bad cap" || kind == "closed store" {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			after, err := os.ReadFile(exchangePath(dir))
			require.NoError(t, err)
			require.Equal(t, before, after)
			s.MaxImportSize = DefaultMaxImportSize
			got, err := s.LastExchanged(t.Context(), dir)
			require.NoError(t, err)
			require.Equal(t, expected, got)
		})
	}
}

func TestExchangeRecord_InvalidPublisherStateAndPaths_ReturnErrors(t *testing.T) {
	for _, kind := range []string{"cap", "size", "cancel", "missing directory", "invalid record", "hash"} {
		t.Run(kind, func(t *testing.T) {
			s, dir := exchangeFixture(t)
			r, err := buildExchangeRecord(exchangeReference(t, exchangeCurrentJSON), "git-import", strings.Repeat("a", 64), "2026-10-01T00:00:00Z")
			require.NoError(t, err)
			ctx := t.Context()
			switch kind {
			case "cap":
				s.MaxImportSize = 0
			case "size":
				s.MaxImportSize = 1
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "missing directory":
				dir = dir + "/missing"
			case "invalid record":
				r.Format = 2
			case "hash":
				r.BoardFileHash = "bad"
			}
			require.Error(t, s.publishReceived(ctx, dir, r))
			if kind == "cancel" {
				require.ErrorIs(t, s.publishReceived(ctx, dir, r), context.Canceled)
			}
		})
	}
}

func TestExchangePull_ClosedIncomplete_PreservesReasonAndPriorReceipt(t *testing.T) {
	s, dir := exchangeFixture(t)
	expected := exchangePublishReference(t, s, dir)
	require.NoError(t, s.saveExchangeReason(t.Context(), "publish-failed"))
	owner, err := s.BeginExchangePull(t.Context(), dir)
	require.NoError(t, err)
	owner.Close()
	err = owner.Incomplete(t.Context())
	require.ErrorIs(t, err, ErrNoExchangeRecord)
	status, err := s.ExchangeRecordStatus(t.Context(), dir)
	require.NoError(t, err)
	require.Equal(t, "publish-failed", status.Reason)
	prior, err := s.LastExchanged(t.Context(), dir)
	require.NoError(t, err)
	require.Equal(t, expected, prior)
}

func TestExchangePull_CompletedSnapshot_LoadsOwnedHubBoundary(t *testing.T) {
	s, dir := exchangeFixture(t)
	require.NoError(t, s.store.WithTx(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(t.Context(), `UPDATE meta SET value=? WHERE key=?`, "2026-10-01T00:00:00Z", "meta.sync.last_sweep_at")
		return err
	}))
	owner, err := s.BeginExchangePull(t.Context(), dir)
	require.NoError(t, err)
	require.NoError(t, owner.Complete(t.Context(), ExchangePullCompletion{CursorDrained: true, SweepCompleted: true}))
	owner.Close()
	r, err := s.LastExchanged(t.Context(), dir)
	require.NoError(t, err)
	require.Equal(t, "hub-pull", r.Source)
	require.Empty(t, r.BoardFileHash)
	require.NotNil(t, r.Boundary)
	require.Equal(t, "2026-10-01T00:00:00Z", r.Boundary.LastSweepAt)
	r.Boundary.LastSweepAt = "changed by caller"
	again, err := s.LastExchanged(t.Context(), dir)
	require.NoError(t, err)
	require.Equal(t, "2026-10-01T00:00:00Z", again.Boundary.LastSweepAt)
}

func TestExchangePull_MalformedOwnedDatabase_PreservesPriorRecord(t *testing.T) {
	for _, tc := range []struct{ name, statement string }{
		{"sync_events", "DROP TABLE sync_events"}, {"sync_sweep_pending", "DROP TABLE sync_sweep_pending"},
		{"meta", "DROP TABLE meta"}, {"applied_events", "DROP TABLE applied_events"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, dir := exchangeFixture(t)
			prior := exchangePublishReference(t, s, dir)
			before, err := os.ReadFile(exchangePath(dir))
			require.NoError(t, err)
			owner, err := s.BeginExchangePull(t.Context(), dir)
			require.NoError(t, err)
			_, err = s.store.WriteDB().ExecContext(t.Context(), tc.statement)
			require.NoError(t, err)
			err = owner.Complete(t.Context(), ExchangePullCompletion{CursorDrained: true, SweepCompleted: true, Encountered: []string{"requested"}})
			require.ErrorContains(t, err, "no such table: "+tc.name)
			owner.Close()
			after, err := os.ReadFile(exchangePath(dir))
			require.NoError(t, err)
			require.Equal(t, before, after)
			r, err := s.LastExchanged(t.Context(), dir)
			require.NoError(t, err)
			require.Equal(t, prior, r)
		})
	}
}

func TestExchangeRecord_StatusContextAndStoreFailure_AreReported(t *testing.T) {
	s, dir := exchangeFixture(t)
	exchangePublishReference(t, s, dir)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := s.ExchangeRecordStatus(ctx, dir)
	require.ErrorIs(t, err, context.Canceled)
	owner, err := s.BeginExchangePull(t.Context(), dir)
	require.NoError(t, err)
	require.ErrorIs(t, owner.Complete(ctx, ExchangePullCompletion{Reason: "partial-pull"}), context.Canceled)
	owner.Close()
	require.NoError(t, s.store.Close())
	_, err = s.ExchangeRecordStatus(t.Context(), dir)
	require.ErrorContains(t, err, "closed")
}

func TestExchangePull_HeldIncomplete_PersistsReasonWithoutReplacingReceipt(t *testing.T) {
	s, dir := exchangeFixture(t)
	prior := exchangePublishReference(t, s, dir)
	before, err := os.ReadFile(exchangePath(dir))
	require.NoError(t, err)
	require.NoError(t, s.saveExchangeReason(t.Context(), "publish-failed"))
	owner, err := s.BeginExchangePull(t.Context(), dir)
	require.NoError(t, err)
	t.Cleanup(owner.Close)
	require.NoError(t, owner.Incomplete(t.Context()))
	var reason string
	require.NoError(t, s.store.QueryRow(t.Context(), `SELECT value FROM meta WHERE key=?`, exchangeReasonKey).Scan(&reason))
	require.Equal(t, "partial-pull", reason)
	owner.Close()
	after, err := os.ReadFile(exchangePath(dir))
	require.NoError(t, err)
	require.Equal(t, before, after)
	got, err := s.LastExchanged(t.Context(), dir)
	require.NoError(t, err)
	require.Equal(t, prior, got)
}

func TestExchangeRecord_StoredUnknownReason_IsBoundedWithoutReadMutation(t *testing.T) {
	s, dir := exchangeFixture(t)
	prior := exchangePublishReference(t, s, dir)
	marker := strings.Repeat("private-marker", 8192)
	require.NoError(t, s.store.WithTx(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(t.Context(), `INSERT INTO meta(key,value) VALUES (?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, exchangeReasonKey, marker)
		return err
	}))
	status, err := s.ExchangeRecordStatus(t.Context(), dir)
	require.NoError(t, err)
	require.True(t, status.Present)
	require.Equal(t, "unknown", status.Reason)
	var stored string
	require.NoError(t, s.store.QueryRow(t.Context(), `SELECT value FROM meta WHERE key=?`, exchangeReasonKey).Scan(&stored))
	require.Equal(t, marker, stored)
	got, err := s.LastExchanged(t.Context(), dir)
	require.NoError(t, err)
	require.Equal(t, prior, got)
	owner, err := s.BeginExchangePull(t.Context(), dir)
	require.NoError(t, err)
	t.Cleanup(owner.Close)
	require.NoError(t, owner.Complete(t.Context(), ExchangePullCompletion{Reason: marker}))
	owner.Close()
	require.NoError(t, s.store.QueryRow(t.Context(), `SELECT value FROM meta WHERE key=?`, exchangeReasonKey).Scan(&stored))
	require.Equal(t, "unknown", stored)
	got, err = s.LastExchanged(t.Context(), dir)
	require.NoError(t, err)
	require.Equal(t, prior, got)
}

func TestExchangePull_LocalDiagnosticWriteAndClear_DoNotNotifyBoard(t *testing.T) {
	for _, completion := range []bool{false, true} {
		t.Run(map[bool]string{false: "write", true: "clear"}[completion], func(t *testing.T) {
			s, dir := exchangeFixture(t)
			prior := exchangePublishReference(t, s, dir)
			require.NoError(t, s.saveExchangeReason(t.Context(), "tracking-limit"))
			_, err := s.store.WriteDB().ExecContext(t.Context(), `UPDATE meta SET value=? WHERE key=?`, "2026-10-01T00:00:00Z", "meta.sync.last_sweep_at")
			require.NoError(t, err)
			owner, err := s.BeginExchangePull(t.Context(), dir)
			require.NoError(t, err)
			t.Cleanup(owner.Close)
			notifications := 0
			s.store.SetOnCommit(func() { notifications++ })
			require.NoError(t, s.store.WithTx(t.Context(), func(tx *sql.Tx) error {
				_, err := tx.ExecContext(t.Context(), `UPDATE meta SET value=? WHERE key=?`, "2026-10-02T00:00:00Z", "meta.sync.last_sweep_at")
				return err
			}))
			require.Equal(t, 1, notifications, "ordinary committed work retains notification wiring")
			notifications = 0
			if completion {
				require.NoError(t, owner.Complete(t.Context(), ExchangePullCompletion{CursorDrained: true, SweepCompleted: true}))
			} else {
				require.NoError(t, owner.Incomplete(t.Context()))
			}
			owner.Close()
			status, err := s.ExchangeRecordStatus(t.Context(), dir)
			require.NoError(t, err)
			if completion {
				require.Empty(t, status.Reason)
				require.Equal(t, "hub-pull", status.Source)
			} else {
				require.Equal(t, "partial-pull", status.Reason)
				got, err := s.LastExchanged(t.Context(), dir)
				require.NoError(t, err)
				require.Equal(t, prior, got)
			}
			require.Zero(t, notifications, "local receipt diagnostics are not board mutations")
		})
	}
}
