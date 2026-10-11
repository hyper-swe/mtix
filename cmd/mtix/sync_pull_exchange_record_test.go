// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Receipt completion follows real cursor, sweep and quarantine transactions.
package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

type exchangeTestHub struct {
	*fakeLateHub
	drop      bool
	emptyMore bool
	during    func()
}

func (h *exchangeTestHub) PullEvents(ctx context.Context, c transport.PullCursor, limit int) ([]*model.SyncEvent, bool, error) {
	if h.during != nil {
		h.during()
	}
	if h.emptyMore {
		return nil, true, nil
	}
	return h.fakeLateHub.PullEvents(ctx, c, limit)
}
func (h *exchangeTestHub) FetchEventsByID(ctx context.Context, ids []string) ([]*model.SyncEvent, error) {
	if h.drop {
		return nil, nil
	}
	return h.fakeLateHub.FetchEventsByID(ctx, ids)
}

func exchangePullFixture(t *testing.T) (*service.ExchangePull, pullIngest, *bytes.Buffer) {
	if runtime.GOOS == "windows" {
		t.Skip("receipt publication is unavailable on this platform")
	}
	t.Helper()
	initTestApp(t)
	owner, err := app.syncSvc.BeginExchangePull(t.Context(), app.mtixDir)
	require.NoError(t, err)
	t.Cleanup(owner.Close)
	var stderr bytes.Buffer
	in := testIngest(&stderr)
	in.receipt = newPullExchangeTracker(app.syncSvc.MaxImportSize)
	return owner, in, &stderr
}

func finishExchangeTestPull(t *testing.T, owner *service.ExchangePull, in pullIngest, hub pullSweepHub) (pullOutcome, error) {
	t.Helper()
	outcome, err := pullThenSweep(t.Context(), in, hub, app.store, transport.PullCursor{}, 1)
	if err != nil {
		return outcome, err
	}
	return outcome, completePullExchange(t.Context(), owner, in.receipt, outcome)
}

func receiptCheckAfterUnlock(t *testing.T, owner *service.ExchangePull) (*service.ExchangeRecord, error) {
	t.Helper()
	owner.Close()
	return app.syncSvc.LastExchanged(t.Context(), app.mtixDir)
}

func TestExchangeRecord_WrittenAfterDrainedPull(t *testing.T) {
	owner, in, _ := exchangePullFixture(t)
	events := offlineEvents(t)
	hub := &exchangeTestHub{fakeLateHub: &fakeLateHub{events: events, pullEvents: events, hubNows: []time.Time{sweepHubT1}}}
	hub.during = func() {
		_, err := os.Stat(filepath.Join(app.mtixDir, "data", "exchange-record.json"))
		require.ErrorIs(t, err, os.ErrNotExist)
	}
	out, err := finishExchangeTestPull(t, owner, in, hub)
	require.NoError(t, err)
	require.Equal(t, 3, out.batches)
	require.True(t, out.sweep.Completed)
	got, err := receiptCheckAfterUnlock(t, owner)
	require.NoError(t, err)
	require.Equal(t, "hub-pull", got.Source)
	require.Empty(t, got.BoardFileHash)
	require.Equal(t, int64(3), got.Boundary.PullClock)
	require.Equal(t, events[2].EventID, got.Boundary.PullEventID)
	require.Equal(t, sweepHubT1.UTC().Format(time.RFC3339Nano), got.Boundary.LastSweepAt)
	require.Zero(t, got.Boundary.Unresolved)
	require.Zero(t, got.Boundary.Pending)
	require.Equal(t, 1, got.NodeCount)
}

func TestExchangeRecord_SamePullRetryAndUnrelatedQuarantine_Qualifies(t *testing.T) {
	owner, in, _ := exchangePullFixture(t)
	events := offlineEvents(t)
	holdExchangeEvent(t, events[1])
	other := *events[2]
	other.EventID = "unrelated-old"
	other.Payload = json.RawMessage(`{}`)
	holdExchangeEvent(t, &other)
	hub := &fakeLateHub{events: events, pullEvents: events, hubNows: []time.Time{sweepHubT1}}
	out, err := finishExchangeTestPull(t, owner, in, hub)
	require.NoError(t, err)
	require.Positive(t, out.retried)
	_, err = receiptCheckAfterUnlock(t, owner)
	require.NoError(t, err)
	require.Contains(t, quarantined(t), other.EventID)
	require.NotContains(t, quarantined(t), events[1].EventID)
}

func holdExchangeEvent(t *testing.T, e *model.SyncEvent) {
	t.Helper()
	raw, err := json.Marshal(e)
	require.NoError(t, err)
	require.NoError(t, app.store.WithTx(t.Context(), func(tx *sql.Tx) error {
		return sqlite.QuarantineEvent(t.Context(), tx, sqlite.QuarantinedEvent{EventID: e.EventID, Source: "pull", RawEvent: string(raw), Reason: "held", FirstSeen: "2026-10-01T00:00:00Z", LastAttempt: "2026-10-01T00:00:00Z"})
	}))
}

func TestExchangeRecord_OwnJournalSweepAndDuplicate_Qualifies(t *testing.T) {
	owner, in, _ := exchangePullFixture(t)
	require.NoError(t, app.store.CreateNode(t.Context(), mkPGNode("TEST-1", "", 0, 1, "own")))
	own, err := readPendingBatch(t.Context(), app.store, 100)
	require.NoError(t, err)
	require.Len(t, own, 1)
	own[0].CreatedAt = sweepHubT1.Add(-time.Second)
	var applied int
	require.NoError(t, app.store.QueryRow(t.Context(), `SELECT COUNT(*) FROM applied_events WHERE event_id=?`, own[0].EventID).Scan(&applied))
	require.Zero(t, applied)
	hub := &fakeLateHub{events: own, hubNows: []time.Time{sweepHubT1}}
	_, err = finishExchangeTestPull(t, owner, in, hub)
	require.NoError(t, err)
	got, err := receiptCheckAfterUnlock(t, owner)
	require.NoError(t, err)
	require.Zero(t, got.Boundary.Unresolved)
	require.NoError(t, app.store.QueryRow(t.Context(), `SELECT COUNT(*) FROM applied_events WHERE event_id=?`, own[0].EventID).Scan(&applied))
	require.Zero(t, applied)
}

func TestExchangeRecord_IncompletePulls_DoNotPublish(t *testing.T) {
	for _, kind := range []string{"missing fetch", "empty more", "held", "tracking limit", "cursor error", "sweep error"} {
		t.Run(kind, func(t *testing.T) {
			owner, in, _ := exchangePullFixture(t)
			events := offlineEvents(t)
			hub := &exchangeTestHub{fakeLateHub: &fakeLateHub{events: events, hubNows: []time.Time{sweepHubT1}}}
			switch kind {
			case "missing fetch":
				hub.drop = true
			case "empty more":
				hub.emptyMore = true
			case "held":
				bad := *events[1]
				bad.Payload = json.RawMessage(`{}`)
				hub.events = []*model.SyncEvent{&bad}
			case "tracking limit":
				in.receipt = newPullExchangeTracker(1)
			case "cursor error":
				hub.pullErr = errors.New("owned cursor refusal")
			case "sweep error":
				hub.listErr = errors.New("owned sweep refusal")
			}
			_, err := finishExchangeTestPull(t, owner, in, hub)
			if kind == "cursor error" || kind == "sweep error" {
				require.Error(t, err)
				require.NoError(t, owner.Incomplete(t.Context()))
			} else {
				require.NoError(t, err)
			}
			_, err = receiptCheckAfterUnlock(t, owner)
			require.ErrorIs(t, err, service.ErrNoExchangeRecord)
			status, err := app.syncSvc.ExchangeRecordStatus(t.Context(), app.mtixDir)
			require.NoError(t, err)
			require.NotEmpty(t, status.Reason)
		})
	}
}

func TestExchangeRecord_ZeroEventAndRepeatedDrain_Qualifies(t *testing.T) {
	owner, in, _ := exchangePullFixture(t)
	hub := &fakeLateHub{hubNows: []time.Time{sweepHubT1}}
	_, err := finishExchangeTestPull(t, owner, in, hub)
	require.NoError(t, err)
	first, err := receiptCheckAfterUnlock(t, owner)
	require.NoError(t, err)
	require.Zero(t, first.NodeCount)
	next, err := app.syncSvc.BeginExchangePull(t.Context(), app.mtixDir)
	require.NoError(t, err)
	t.Cleanup(next.Close)
	in.receipt = newPullExchangeTracker(app.syncSvc.MaxImportSize)
	_, err = finishExchangeTestPull(t, next, in, hub)
	require.NoError(t, err)
	second, err := receiptCheckAfterUnlock(t, next)
	require.NoError(t, err)
	require.Equal(t, first.BoardHash, second.BoardHash)
}

func TestPullExchangeTracker_AccountingAndUnknownCompletion_AreSticky(t *testing.T) {
	tracker := newPullExchangeTracker(66)
	tracker.observe([]string{"id", "id"})
	require.Zero(t, tracker.remaining)
	require.Empty(t, tracker.reason)
	require.Len(t, tracker.encountered(), 1)
	tracker.observe([]string{"next"})
	require.Equal(t, "tracking-limit", tracker.reason)
	tracker.observe([]string{"id"})
	require.Equal(t, "tracking-limit", tracker.reason)
	for _, ids := range [][]string{{""}, {string(make([]byte, 129))}} {
		tr := newPullExchangeTracker(1000)
		tr.observe(ids)
		require.Equal(t, "unknown-event", tr.reason)
	}
	tr := newPullExchangeTracker(1000)
	tr.events([]*model.SyncEvent{nil})
	require.Equal(t, "unknown-event", tr.reason)
	tr = newPullExchangeTracker(0)
	require.Equal(t, "invalid-cap", tr.reason)
	tr = newPullExchangeTracker(1000)
	tr.cursorPage(0, true)
	tr.cursorPage(1, false)
	require.Equal(t, "partial-pull", tr.reason)
}

func TestExchangeRecord_FalseSweepCompletion_PreservesPriorRecord(t *testing.T) {
	owner, in, _ := exchangePullFixture(t)
	hub := &fakeLateHub{hubNows: []time.Time{sweepHubT1}}
	_, err := finishExchangeTestPull(t, owner, in, hub)
	require.NoError(t, err)
	path := filepath.Join(app.mtixDir, "data", "exchange-record.json")
	prior, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, app.store.WithTx(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(t.Context(), `INSERT INTO sync_sweep_pending(event_id,lamport_clock) VALUES (?,?)`, "staged", 1)
		return err
	}))
	completed, err := finishLateEventSweep(t.Context(), app.store, sweepHubT1)
	require.NoError(t, err)
	require.False(t, completed)
	in.receipt.drained = true
	require.NoError(t, completePullExchange(t.Context(), owner, in.receipt, pullOutcome{sweep: lateEventSweep{Completed: completed}}))
	got, err := receiptCheckAfterUnlock(t, owner)
	require.NoError(t, err)
	require.Equal(t, "hub-pull", got.Source)
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, prior, after)
}
