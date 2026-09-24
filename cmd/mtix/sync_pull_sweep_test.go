// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// PG-free tests of the late-event sweep logic (MTIX-95.5) against a fake
// hub: the window it asks for, which hub time it records, the order it
// applies in, and that no failure advances meta.sync.last_sweep_at. The
// real-Postgres behaviour is in sync_pull_sweep_pg_test.go.

// fakeLateHub serves a fixed hub log. It lists ids in (created_at,
// event_id) keyset order after the cursor it is given, as the hub does,
// records every cursor in calls, returns hubNows[i] on the i-th listing call
// (the last value repeats), and returns fetched events in REVERSE pull order
// so the caller must sort. listErr fails every listing call, or only call
// number failAtCall (1-based) when that is set.
type fakeLateHub struct {
	events      []*model.SyncEvent
	hubNows     []time.Time
	calls       []transport.EventIDCursor
	listErr     error
	failAtCall  int
	fetchErr    error
	moreOnEmpty bool
}

func (h *fakeLateHub) ListEventIDsSince(_ context.Context, after transport.EventIDCursor, limit int) (transport.EventIDPage, error) {
	h.calls = append(h.calls, after)
	call := len(h.calls)
	if h.listErr != nil && (h.failAtCall == 0 || h.failAtCall == call) {
		return transport.EventIDPage{}, h.listErr
	}
	pg := transport.EventIDPage{HubNow: h.hubNows[min(call, len(h.hubNows))-1]}
	if h.moreOnEmpty {
		pg.More = true
		return pg, nil
	}
	sorted := append([]*model.SyncEvent(nil), h.events...)
	sort.Slice(sorted, func(i, j int) bool {
		if !sorted[i].CreatedAt.Equal(sorted[j].CreatedAt) {
			return sorted[i].CreatedAt.Before(sorted[j].CreatedAt)
		}
		return sorted[i].EventID < sorted[j].EventID
	})
	for _, e := range sorted {
		if !e.CreatedAt.After(after.CreatedAt) &&
			(!e.CreatedAt.Equal(after.CreatedAt) || e.EventID <= after.EventID) {
			continue
		}
		if len(pg.IDs) == limit {
			pg.More = true
			break
		}
		pg.IDs = append(pg.IDs, e.EventID)
		pg.Next = transport.EventIDCursor{CreatedAt: e.CreatedAt, EventID: e.EventID}
	}
	return pg, nil
}

func (h *fakeLateHub) FetchEventsByID(_ context.Context, ids []string) ([]*model.SyncEvent, error) {
	if h.fetchErr != nil {
		return nil, h.fetchErr
	}
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	var out []*model.SyncEvent
	for i := len(h.events) - 1; i >= 0; i-- {
		if want[h.events[i].EventID] {
			out = append(out, h.events[i])
		}
	}
	return out, nil
}

// offlineEventsPushedAt is the hub created_at of offlineEvents' first event;
// the others follow one second apart.
var offlineEventsPushedAt = time.Date(2026, 9, 24, 9, 0, 0, 0, time.UTC)

// offlineEvents builds, on a separate store, a create of TEST-9 and two
// later description edits, and returns those three events in pull order,
// with hub created_at values one second apart from offlineEventsPushedAt.
func offlineEvents(t *testing.T) []*model.SyncEvent {
	t.Helper()
	ctx := context.Background()
	b, err := sqlite.New(filepath.Join(t.TempDir(), ".mtix"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = b.Close() })
	require.NoError(t, b.CreateNode(ctx, mkPGNode("TEST-9", "", 0, 9, "offline node")))
	for _, d := range []string{"first edit", "second edit"} {
		desc := d
		require.NoError(t, b.UpdateNode(ctx, "TEST-9", &store.NodeUpdate{Description: &desc}))
	}
	events, err := readPendingBatch(ctx, b, 100)
	require.NoError(t, err)
	require.Len(t, events, 3)
	for i, e := range events {
		e.CreatedAt = offlineEventsPushedAt.Add(time.Duration(i) * time.Second)
	}
	return events
}

// setLastSweep writes meta.sync.last_sweep_at on the peer store.
func setLastSweep(t *testing.T, v string) {
	t.Helper()
	_, err := app.store.WriteDB().ExecContext(context.Background(),
		`UPDATE meta SET value = ? WHERE key = 'meta.sync.last_sweep_at'`, v)
	require.NoError(t, err)
}

// lastSweep reads meta.sync.last_sweep_at from the peer store.
func lastSweep(t *testing.T) string {
	t.Helper()
	var v string
	require.NoError(t, app.store.QueryRow(context.Background(),
		`SELECT value FROM meta WHERE key = 'meta.sync.last_sweep_at'`).Scan(&v))
	return v
}

// The fake hub's clock readings are in a non-UTC zone (pgx returns
// timestamptz in the local zone), so the expected "...Z" strings prove the
// sweep converts to UTC even when the tests run with TZ=UTC. In UTC they are
// 2026-09-24T10:00:00.123456Z and 2026-09-24T10:00:05Z.
var (
	sweepHubZone = time.FixedZone("hub+05:30", 5*3600+1800)
	sweepHubT1   = time.Date(2026, 9, 24, 15, 30, 0, 123456000, sweepHubZone)
	sweepHubT2   = time.Date(2026, 9, 24, 15, 30, 5, 0, sweepHubZone)
)

// sweepMeta reads one late-event sweep meta value from the peer store.
func sweepMeta(t *testing.T, key string) string {
	t.Helper()
	var v string
	require.NoError(t, app.store.QueryRow(context.Background(),
		`SELECT value FROM meta WHERE key = ?`, key).Scan(&v), "meta %s must exist", key)
	return v
}

// requireNoFullSweepProgress asserts no sweep progress is recorded.
func requireNoFullSweepProgress(t *testing.T) {
	t.Helper()
	for _, key := range []string{"meta.sync.sweep_after_id",
		"meta.sync.sweep_after_created_at", "meta.sync.sweep_started_at"} {
		require.Equal(t, "", sweepMeta(t, key), key)
	}
}

// TestSweepLateEvents_FirstSweep_PagesFullHistoryAndRecordsFirstHubTime:
// with no recorded sweep (an empty value, or no meta row at all), the sweep
// pages the full id history without a warning, applies the missing events
// in pull order although the hub returned them reversed and across fetch
// chunks, reports the pages it compared, and records the hub time of the
// FIRST listing page (writing the meta row back when it was missing).
func TestSweepLateEvents_FirstSweep_PagesFullHistoryAndRecordsFirstHubTime(t *testing.T) {
	tests := []struct {
		name    string
		neverAt string
	}{
		{"empty last_sweep_at", `UPDATE meta SET value = '' WHERE key = 'meta.sync.last_sweep_at'`},
		{"last_sweep_at row missing", `DELETE FROM meta WHERE key = 'meta.sync.last_sweep_at'`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			_, err := app.store.WriteDB().ExecContext(context.Background(), tt.neverAt)
			require.NoError(t, err)
			hub := &fakeLateHub{events: offlineEvents(t), hubNows: []time.Time{sweepHubT1, sweepHubT2}}
			var stderr bytes.Buffer

			got, err := sweepLateEvents(context.Background(), &stderr, hub, app.store, 2)

			require.NoError(t, err)
			require.Equal(t, lateEventSweep{Recovered: 3, FullDiff: true}, got)
			require.Equal(t, []transport.EventIDCursor{{},
				{CreatedAt: hub.events[1].CreatedAt, EventID: hub.events[1].EventID}}, hub.calls,
				"from the zero position, three ids in pages of two: the second page starts after the second id")
			node, err := app.store.GetNode(context.Background(), "TEST-9")
			require.NoError(t, err)
			require.Equal(t, "second edit", node.Description, "create, then both edits, in Lamport order")
			require.Equal(t, "2026-09-24T10:00:00.123456Z", lastSweep(t))
			require.Contains(t, stderr.String(), "comparing the full hub event history once")
			require.Contains(t, stderr.String(), "late-event sweep: compared 3 hub event ids in 2 pages")
			require.NotContains(t, stderr.String(), "WARN", "never having swept is not a warning")
			requireNoFullSweepProgress(t)
		})
	}
}

// TestSweepLateEvents_RecordedSweep_ListsWindowFromHubTimeMinusOverlap: the
// window starts exactly 15 minutes before the recorded hub time, from an
// empty event id, and events the store already holds are not fetched again.
func TestSweepLateEvents_RecordedSweep_ListsWindowFromHubTimeMinusOverlap(t *testing.T) {
	initTestApp(t)
	events := offlineEvents(t)
	require.NoError(t, applyPullBatch(context.Background(), app.store, events[:2]))
	setLastSweep(t, "2026-09-24T09:00:00.5Z")
	hub := &fakeLateHub{events: events, hubNows: []time.Time{sweepHubT1}}

	got, err := sweepLateEvents(context.Background(), &bytes.Buffer{}, hub, app.store, 100)

	require.NoError(t, err)
	require.Equal(t, lateEventSweep{Recovered: 1}, got)
	require.Equal(t, []transport.EventIDCursor{{
		CreatedAt: time.Date(2026, 9, 24, 8, 45, 0, 500000000, time.UTC),
	}}, hub.calls, "one page from 15 minutes before the recorded time, never the full history")
	require.Equal(t, "2026-09-24T10:00:00.123456Z", lastSweep(t))
}

// TestSweepLateEvents_UnreadableSweepState_FallsBackToFreshFullDiff: a
// recorded sweep time, or a saved full-diff start time, that is not a time
// is reported, and the sweep diffs the full history from the start.
func TestSweepLateEvents_UnreadableSweepState_FallsBackToFreshFullDiff(t *testing.T) {
	tests := []struct {
		name     string
		stmts    []string
		wantWarn string
	}{
		{"last_sweep_at is not a time",
			[]string{`UPDATE meta SET value = 'not-a-time' WHERE key = 'meta.sync.last_sweep_at'`},
			`meta.sync.last_sweep_at "not-a-time" is not a time`},
		{"saved full-diff start is not a time", []string{
			`UPDATE meta SET value = 'some-id' WHERE key = 'meta.sync.sweep_after_id'`,
			`UPDATE meta SET value = '2026-09-24T09:00:00Z' WHERE key = 'meta.sync.sweep_after_created_at'`,
			`UPDATE meta SET value = 'garbage' WHERE key = 'meta.sync.sweep_started_at'`,
		}, `meta.sync.sweep_started_at "garbage" is not a time`},
		{"saved full-diff position is not a time", []string{
			`UPDATE meta SET value = 'some-id' WHERE key = 'meta.sync.sweep_after_id'`,
			`UPDATE meta SET value = 'garbage' WHERE key = 'meta.sync.sweep_after_created_at'`,
			`UPDATE meta SET value = '2026-09-24T09:00:00Z' WHERE key = 'meta.sync.sweep_started_at'`,
		}, `meta.sync.sweep_after_created_at "garbage" is not a time`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			for _, stmt := range tt.stmts {
				_, err := app.store.WriteDB().ExecContext(context.Background(), stmt)
				require.NoError(t, err)
			}
			hub := &fakeLateHub{hubNows: []time.Time{sweepHubT1}}
			var stderr bytes.Buffer

			got, err := sweepLateEvents(context.Background(), &stderr, hub, app.store, 10)

			require.NoError(t, err)
			require.True(t, got.FullDiff)
			require.Equal(t, []transport.EventIDCursor{{}}, hub.calls,
				"a fresh full diff starts from the zero position")
			require.Contains(t, stderr.String(), tt.wantWarn)
			require.Equal(t, "2026-09-24T10:00:00.123456Z", lastSweep(t))
			requireNoFullSweepProgress(t)
		})
	}
}

// TestSweepLateEvents_Interrupted_ResumesFromSavedProgress: a sweep that
// fails part-way (the listing times out, or an event cannot be applied)
// keeps the progress of every page it applied: the last listed position
// and the hub time read before its first page, while
// meta.sync.last_sweep_at keeps its value. The next sweep resumes listing
// AFTER the saved position, not from the start, and on completion records
// the FIRST start time as meta.sync.last_sweep_at and clears the progress.
// This matters most for the one-time full diff of a large hub, and holds
// for a windowed sweep too.
func TestSweepLateEvents_Interrupted_ResumesFromSavedProgress(t *testing.T) {
	listingTimesOut := func(ev []*model.SyncEvent) *fakeLateHub {
		return &fakeLateHub{events: ev, hubNows: []time.Time{sweepHubT1},
			listErr: errors.New("listing timed out"), failAtCall: 2}
	}
	secondPageUnappliable := func(ev []*model.SyncEvent) *fakeLateHub {
		bad := *ev[1]
		bad.OpType = model.OpType("not_an_op")
		return &fakeLateHub{events: []*model.SyncEvent{ev[0], &bad, ev[2]},
			hubNows: []time.Time{sweepHubT1}}
	}
	tests := []struct {
		name       string
		lastSweep  string
		first      func(events []*model.SyncEvent) *fakeLateHub
		wantErr    string
		wantResume string
	}{
		{"full diff, listing fails on the second page", "", listingTimesOut,
			"listing timed out", "resuming the full hub event comparison after "},
		{"full diff, second page cannot be applied", "", secondPageUnappliable,
			"apply late events", "resuming the full hub event comparison after "},
		{"windowed sweep, listing fails on the second page", "2026-09-24T08:59:00Z", listingTimesOut,
			"listing timed out", "late-event sweep: resuming after "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			ctx := context.Background()
			setLastSweep(t, tt.lastSweep)
			full := tt.lastSweep == ""
			events := offlineEvents(t)
			ids := eventIDs(events)
			sort.Strings(ids)
			require.Equal(t, eventIDs(events), ids, "precondition: one store's ids ascend with its clock")
			first := tt.first(events)

			got, err := sweepLateEvents(ctx, &bytes.Buffer{}, first, app.store, 1)

			require.Error(t, err)
			require.Contains(t, err.Error(), tt.wantErr)
			require.Equal(t, lateEventSweep{Recovered: 1, FullDiff: full}, got, "page one was applied")
			require.Equal(t, tt.lastSweep, lastSweep(t), "an unfinished sweep records no sweep time")
			require.Equal(t, ids[0], sweepMeta(t, "meta.sync.sweep_after_id"),
				"progress covers exactly the pages that were applied")
			require.Equal(t, "2026-09-24T09:00:00Z", sweepMeta(t, "meta.sync.sweep_after_created_at"))
			require.Equal(t, "2026-09-24T10:00:00.123456Z", sweepMeta(t, "meta.sync.sweep_started_at"))

			resumed := &fakeLateHub{events: events, hubNows: []time.Time{sweepHubT2}}
			var stderr bytes.Buffer
			got, err = sweepLateEvents(ctx, &stderr, resumed, app.store, 1)

			require.NoError(t, err)
			require.Equal(t, lateEventSweep{Recovered: 2, FullDiff: full}, got)
			require.Equal(t, []transport.EventIDCursor{
				{CreatedAt: events[0].CreatedAt, EventID: ids[0]},
				{CreatedAt: events[1].CreatedAt, EventID: ids[1]},
			}, resumed.calls, "the listing resumes after the saved position, not from the start")
			require.Contains(t, stderr.String(), tt.wantResume+ids[0])
			require.Equal(t, "2026-09-24T10:00:00.123456Z", lastSweep(t),
				"the next window is measured from the hub time before the FIRST page")
			requireNoFullSweepProgress(t)
			node, err := app.store.GetNode(ctx, "TEST-9")
			require.NoError(t, err)
			require.Equal(t, "second edit", node.Description)
		})
	}
}

// TestSweepLateEvents_ResumedFullDiff_ReportsPagesOfThisPull: a resumed full
// diff reports the ids and pages this pull compared, and records the saved
// start time. The interrupted pull had applied the first page.
func TestSweepLateEvents_ResumedFullDiff_ReportsPagesOfThisPull(t *testing.T) {
	initTestApp(t)
	events := offlineEvents(t)
	require.NoError(t, applyPullBatch(context.Background(), app.store, events[:1]))
	for key, v := range map[string]string{
		"meta.sync.sweep_after_id":         events[0].EventID,
		"meta.sync.sweep_after_created_at": "2026-09-24T09:00:00Z",
		"meta.sync.sweep_started_at":       "2026-09-24T08:00:00Z",
	} {
		_, err := app.store.WriteDB().ExecContext(context.Background(),
			`UPDATE meta SET value = ? WHERE key = ?`, v, key)
		require.NoError(t, err)
	}
	var stderr bytes.Buffer

	_, err := sweepLateEvents(context.Background(), &stderr,
		&fakeLateHub{events: events, hubNows: []time.Time{sweepHubT2}}, app.store, 1)

	require.NoError(t, err)
	require.Contains(t, stderr.String(), "late-event sweep: compared 2 hub event ids in 2 pages")
	require.Equal(t, "2026-09-24T08:00:00Z", lastSweep(t))
}

// TestResetLateEventSweep_ClearsSweepState: resetting the sweep state (as
// sync clone does) empties meta.sync.last_sweep_at and any saved progress,
// so the next pull diffs the full hub history from the start.
func TestResetLateEventSweep_ClearsSweepState(t *testing.T) {
	initTestApp(t)
	setLastSweep(t, "2026-09-24T09:00:00Z")
	for _, key := range []string{"meta.sync.sweep_after_id",
		"meta.sync.sweep_after_created_at", "meta.sync.sweep_started_at"} {
		_, err := app.store.WriteDB().ExecContext(context.Background(),
			`UPDATE meta SET value = 'set' WHERE key = ?`, key)
		require.NoError(t, err)
	}

	require.NoError(t, resetLateEventSweep(context.Background(), app.store))

	require.Equal(t, "", lastSweep(t))
	requireNoFullSweepProgress(t)
}

// TestSweepLateEvents_Failure_LeavesLastSweepUnchanged: a failed listing,
// fetch or apply, a hub that reports more ids after an empty page, or a
// page without a hub clock returns an error, and meta.sync.last_sweep_at
// keeps its value so the next pull retries the same window.
func TestSweepLateEvents_Failure_LeavesLastSweepUnchanged(t *testing.T) {
	boom := errors.New("hub unavailable")
	tests := []struct {
		name    string
		hub     func(events []*model.SyncEvent) *fakeLateHub
		wantErr string
	}{
		{"listing fails", func(ev []*model.SyncEvent) *fakeLateHub {
			return &fakeLateHub{events: ev, hubNows: []time.Time{sweepHubT1}, listErr: boom}
		}, "list hub event ids: hub unavailable"},
		{"fetch fails", func(ev []*model.SyncEvent) *fakeLateHub {
			return &fakeLateHub{events: ev, hubNows: []time.Time{sweepHubT1}, fetchErr: boom}
		}, "fetch late events: hub unavailable"},
		{"apply fails", func(ev []*model.SyncEvent) *fakeLateHub {
			bad := *ev[2]
			bad.OpType = model.OpType("not_an_op")
			return &fakeLateHub{events: []*model.SyncEvent{ev[0], ev[1], &bad},
				hubNows: []time.Time{sweepHubT1}}
		}, "apply late events"},
		{"more after an empty page", func(ev []*model.SyncEvent) *fakeLateHub {
			return &fakeLateHub{events: ev, hubNows: []time.Time{sweepHubT1}, moreOnEmpty: true}
		}, "hub reported more ids after an empty page"},
		{"no hub clock", func(ev []*model.SyncEvent) *fakeLateHub {
			return &fakeLateHub{events: ev, hubNows: []time.Time{{}}}
		}, "hub returned no clock reading"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			setLastSweep(t, "2026-09-24T09:00:00Z")

			_, err := sweepLateEvents(context.Background(), &bytes.Buffer{},
				tt.hub(offlineEvents(t)), app.store, 2)

			require.Error(t, err)
			require.Contains(t, err.Error(), tt.wantErr)
			require.Equal(t, "2026-09-24T09:00:00Z", lastSweep(t))
		})
	}
}

// TestMissingLocalEventIDs_HeldInEitherTable_NotMissing: an id is missing
// only when this store holds it in neither sync_events nor applied_events:
// not this store's own event (sync_events only, until its echo is pulled),
// not an applied foreign event (both tables), not an id recorded in
// applied_events only. The missing ids keep the page's order.
func TestMissingLocalEventIDs_HeldInEitherTable_NotMissing(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	events := offlineEvents(t)
	require.NoError(t, applyPullBatch(ctx, app.store, events[:1]))
	_, err := app.store.WriteDB().ExecContext(ctx,
		`INSERT INTO applied_events (event_id, applied_at, applied_by_lamport) VALUES (?, ?, 0)`,
		events[1].EventID, "2026-09-24T00:00:00Z")
	require.NoError(t, err)
	require.Zero(t, countRows(t, `SELECT COUNT(*) FROM sync_events WHERE event_id = ?`, events[1].EventID),
		"precondition: held in applied_events only")
	require.NoError(t, runCreate("own node", "", "", 3, "", "", "", "", ""))
	own, err := readPendingBatch(ctx, app.store, 10)
	require.NoError(t, err)
	require.Len(t, own, 1)
	require.Zero(t, countRows(t, `SELECT COUNT(*) FROM applied_events WHERE event_id = ?`, own[0].EventID),
		"precondition: own event held in sync_events only")

	got, err := missingLocalEventIDs(ctx, app.store, []string{"zz-not-held", events[2].EventID,
		own[0].EventID, events[1].EventID, events[0].EventID, "aa-not-held"})

	require.NoError(t, err)
	require.Equal(t, []string{"zz-not-held", events[2].EventID, "aa-not-held"}, got)
	none, err := missingLocalEventIDs(ctx, app.store, nil)
	require.NoError(t, err)
	require.Empty(t, none)
}

// countRows runs a COUNT query on the peer store.
func countRows(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, app.store.QueryRow(context.Background(), query, args...).Scan(&n))
	return n
}

// TestPrintLateEventSweep_ReportsFullDiffOrRecoveries: stdout names the
// recovered count after a full diff (even zero) or when anything was
// recovered, and stays silent for an empty windowed sweep.
func TestPrintLateEventSweep_ReportsFullDiffOrRecoveries(t *testing.T) {
	tests := []struct {
		name string
		in   lateEventSweep
		want string
	}{
		{"full diff, nothing missing", lateEventSweep{FullDiff: true},
			"late-event sweep (first run, full hub history): 0 late events recovered\n"},
		{"window with recoveries", lateEventSweep{Recovered: 2},
			"late-event sweep: 2 late events recovered\n"},
		{"window, nothing missing", lateEventSweep{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			printLateEventSweep(&out, tt.in)
			require.Equal(t, tt.want, out.String())
		})
	}
}
