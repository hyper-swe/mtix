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
	"strings"
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

type exchangeResponseHub struct {
	*fakeLateHub
	mode  string
	extra *model.SyncEvent
}

func (h *exchangeResponseHub) FetchEventsByID(ctx context.Context, ids []string) ([]*model.SyncEvent, error) {
	chunk, err := h.fakeLateHub.FetchEventsByID(ctx, ids)
	if err != nil || len(chunk) == 0 {
		return chunk, err
	}
	switch h.mode {
	case "extra":
		return append(chunk, h.extra), nil
	case "missing":
		return nil, nil
	case "nil":
		return append(chunk, nil), nil
	case "duplicate":
		return append(chunk, chunk[0]), nil
	case "ambiguous":
		conflicting := *h.extra
		conflicting.EventID = chunk[0].EventID
		conflicting.LamportClock = chunk[0].LamportClock
		return append(chunk, &conflicting), nil
	}
	return chunk, nil
}

func TestExchangeRecord_FetchedResponse_UnknownCompletionDoesNotPublish(t *testing.T) {
	for _, mode := range []string{"extra", "missing", "duplicate", "ambiguous"} {
		t.Run(mode, func(t *testing.T) {
			owner, in, _ := exchangePullFixture(t)
			events := offlineEvents(t)
			hub := &exchangeResponseHub{fakeLateHub: &fakeLateHub{events: events, hubNows: []time.Time{sweepHubT1}}, mode: mode, extra: remoteCreateAt(t, "TEST-99", 4)}
			_, err := finishExchangeTestPull(t, owner, in, hub)
			require.NoError(t, err)
			if mode != "missing" {
				_, err = app.store.GetNode(t.Context(), "TEST-9")
				require.NoError(t, err, "requested valid events still apply")
			}
			if mode == "extra" {
				_, err = app.store.GetNode(t.Context(), "TEST-99")
				require.NoError(t, err, "extra valid event admission is unchanged")
			}
			_, err = receiptCheckAfterUnlock(t, owner)
			require.ErrorIs(t, err, service.ErrNoExchangeRecord)
		})
	}
}

func TestExchangeRecord_ReasonDiagnostics_FixedBoundedAndPriorPreserved(t *testing.T) {
	for _, source := range []string{"completion", "stored"} {
		for _, tc := range []struct{ name, value, want string }{
			{"empty", "", ""}, {"known publication", "publish-failed", "publish-failed"},
			{"known schema", "unsupported-schema", "unsupported-schema"},
			{"unknown", "private marker\n\x1b[31m", "unknown"},
			{"oversize", "private marker\n\x1b[31m" + strings.Repeat("x", 100000), "unknown"},
		} {
			t.Run(source+"/"+tc.name, func(t *testing.T) {
				owner, in, _ := exchangePullFixture(t)
				_, err := finishExchangeTestPull(t, owner, in, &fakeLateHub{hubNows: []time.Time{sweepHubT1}})
				require.NoError(t, err)
				path := filepath.Join(app.mtixDir, "data", "exchange-record.json")
				before, err := os.ReadFile(path)
				require.NoError(t, err)
				if source == "completion" {
					require.NoError(t, owner.Complete(t.Context(), service.ExchangePullCompletion{Reason: tc.value}))
				} else {
					require.NoError(t, app.store.WithTx(t.Context(), func(tx *sql.Tx) error {
						_, err := tx.ExecContext(t.Context(), `INSERT INTO meta(key,value) VALUES (?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, "meta.sync.exchange_record.reason", tc.value)
						return err
					}))
				}
				owner.Close()
				status, err := app.syncSvc.ExchangeRecordStatus(t.Context(), app.mtixDir)
				require.NoError(t, err)
				require.True(t, status.Present)
				require.LessOrEqual(t, len(status.Reason), 64)
				expected := tc.want
				if source == "completion" && tc.value == "" {
					expected = "partial-pull"
				}
				require.Equal(t, expected, status.Reason)
				after, err := os.ReadFile(path)
				require.NoError(t, err)
				require.Equal(t, before, after)
				report := appendExchangeRecordCheck(t.Context(), DoctorReport{OverallPass: true}, app.store)
				var text bytes.Buffer
				printDoctorTable(&text, report)
				require.NotContains(t, text.String(), "private marker")
				encoded, err := json.Marshal(status)
				require.NoError(t, err)
				require.NotContains(t, string(encoded), "private marker")
			})
		}
	}
}

func TestExchangeRecord_RunSyncPull_PublishFailureRetainsSpecificReason(t *testing.T) {
	pool := openCmdHub(t)
	initTestApp(t)
	pushToHub(t, pool, remoteCreateAt(t, "TEST-1", 1))
	require.NoError(t, os.Mkdir(filepath.Join(app.mtixDir, "data", "exchange-record.json"), 0o700))
	var stdout, stderr bytes.Buffer
	err := runSyncPull(t.Context(), &stdout, &stderr, nil, transport.Options{InsecureTLS: true}, 100)
	require.Error(t, err)
	_, err = app.store.GetNode(t.Context(), "TEST-1")
	require.NoError(t, err)
	var sweep, reason string
	require.NoError(t, app.store.QueryRow(t.Context(), `SELECT value FROM meta WHERE key=?`, "meta.sync.last_sweep_at").Scan(&sweep))
	require.NotEmpty(t, sweep)
	require.NoError(t, app.store.QueryRow(t.Context(), `SELECT value FROM meta WHERE key=?`, "meta.sync.exchange_record.reason").Scan(&reason))
	require.Equal(t, "publish-failed", reason)
}

func TestExchangeRecord_FetchedNil_RefusesWithoutPanic(t *testing.T) {
	owner, in, _ := exchangePullFixture(t)
	events := offlineEvents(t)
	hub := &exchangeResponseHub{fakeLateHub: &fakeLateHub{events: events, hubNows: []time.Time{sweepHubT1}}, mode: "nil"}
	_, err := finishExchangeTestPull(t, owner, in, hub)
	require.ErrorIs(t, err, model.ErrInvalidInput)
	require.Equal(t, "unknown-event", in.receipt.reason)
	_, err = receiptCheckAfterUnlock(t, owner)
	require.ErrorIs(t, err, service.ErrNoExchangeRecord)
}

func TestPullExchangeTracker_FetchedResponse_ExactChunkAndStickyControls(t *testing.T) {
	for _, tc := range []struct {
		name      string
		requested []string
		events    []*model.SyncEvent
		want      string
	}{
		{"empty", nil, nil, ""},
		{"exact", []string{"a"}, []*model.SyncEvent{{EventID: "a"}}, ""},
		{"other chunk", []string{"a"}, []*model.SyncEvent{{EventID: "b"}}, "unknown-event"},
		{"missing", []string{"a"}, nil, "unknown-event"},
		{"nil", []string{"a"}, []*model.SyncEvent{nil}, "unknown-event"},
		{"duplicate", []string{"a"}, []*model.SyncEvent{{EventID: "a"}, {EventID: "a"}}, "unknown-event"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tracker := newPullExchangeTracker(1000)
			tracker.qualifyFetched(tc.requested, tc.events)
			require.Equal(t, tc.want, tracker.reason)
		})
	}
	var absent *pullExchangeTracker
	absent.qualifyFetched([]string{"a"}, nil)
	limited := newPullExchangeTracker(1)
	limited.qualifyFetched([]string{"a"}, []*model.SyncEvent{{EventID: "a"}})
	require.Equal(t, "tracking-limit", limited.reason)
	limited.qualifyFetched(nil, nil)
	require.Equal(t, "tracking-limit", limited.reason)
}

func TestExchangeRecord_NoDSNPull_DiagnosticDoesNotScheduleMirror(t *testing.T) {
	t.Setenv(transport.EnvDSN, "")
	owner, in, _ := exchangePullFixture(t)
	_, err := finishExchangeTestPull(t, owner, in, &fakeLateHub{hubNows: []time.Time{sweepHubT1}})
	require.NoError(t, err)
	owner.Close()
	require.NoError(t, app.syncSvc.AutoExport(t.Context(), app.mtixDir))
	boardPath := filepath.Join(app.mtixDir, "tasks.json")
	receiptPath := filepath.Join(app.mtixDir, "data", "exchange-record.json")
	beforeBoard, err := os.ReadFile(boardPath)
	require.NoError(t, err)
	beforeReceipt, err := os.ReadFile(receiptPath)
	require.NoError(t, err)
	tail, err := app.store.JournalTail(t.Context())
	require.NoError(t, err)
	beforeRequests, err := app.store.ReadExportRequest(t.Context())
	require.NoError(t, err)
	closeExporter := wireMirrorExporter(app.logger)
	t.Cleanup(closeExporter)
	var stdout, stderr bytes.Buffer
	err = runSyncPull(t.Context(), &stdout, &stderr, nil, transport.Options{}, 1)
	require.ErrorContains(t, err, "mtix sync dsn: no DSN configured")
	var reason string
	require.NoError(t, app.store.QueryRow(t.Context(), `SELECT value FROM meta WHERE key=?`, "meta.sync.exchange_record.reason").Scan(&reason))
	require.Equal(t, "partial-pull", reason)
	afterBoard, err := os.ReadFile(boardPath)
	require.NoError(t, err)
	require.Equal(t, beforeBoard, afterBoard)
	afterReceipt, err := os.ReadFile(receiptPath)
	require.NoError(t, err)
	require.Equal(t, beforeReceipt, afterReceipt)
	afterTail, err := app.store.JournalTail(t.Context())
	require.NoError(t, err)
	require.Equal(t, tail, afterTail)
	prior, err := app.syncSvc.LastExchanged(t.Context(), app.mtixDir)
	require.NoError(t, err)
	require.Equal(t, "hub-pull", prior.Source)
	afterRequests, err := app.store.ReadExportRequest(t.Context())
	require.NoError(t, err)
	require.Equal(t, beforeRequests, afterRequests, "local diagnostic persistence does not advance export intent")
	pending, err := app.syncSvc.PendingExports(t.Context(), app.mtixDir)
	require.NoError(t, err)
	require.Zero(t, pending.Count, "local diagnostic persistence does not request a board export")
}
