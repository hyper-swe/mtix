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

// fakeLateHub serves a fixed hub log. It lists ids in event_id order
// (whatever the window: the window arguments are recorded for assertions),
// returns hubNows[i] on the i-th listing call (the last value repeats), and
// returns fetched events in REVERSE pull order so the caller must sort.
type fakeLateHub struct {
	events      []*model.SyncEvent
	hubNows     []time.Time
	sinceCalls  []transport.EventIDCursor
	allCalls    []string
	listErr     error
	fetchErr    error
	moreOnEmpty bool
}

func (h *fakeLateHub) page(afterID string, limit int) (transport.EventIDPage, error) {
	if h.listErr != nil {
		return transport.EventIDPage{}, h.listErr
	}
	call := len(h.sinceCalls) + len(h.allCalls) - 1
	pg := transport.EventIDPage{HubNow: h.hubNows[min(call, len(h.hubNows)-1)]}
	if h.moreOnEmpty {
		pg.More = true
		return pg, nil
	}
	ids := make([]string, 0, len(h.events))
	for _, e := range h.events {
		if e.EventID > afterID {
			ids = append(ids, e.EventID)
		}
	}
	sort.Strings(ids)
	if len(ids) > limit {
		ids, pg.More = ids[:limit], true
	}
	pg.IDs = ids
	if len(ids) > 0 {
		pg.Next = transport.EventIDCursor{EventID: ids[len(ids)-1]}
	}
	return pg, nil
}

func (h *fakeLateHub) ListEventIDsSince(_ context.Context, after transport.EventIDCursor, limit int) (transport.EventIDPage, error) {
	h.sinceCalls = append(h.sinceCalls, after)
	return h.page(after.EventID, limit)
}

func (h *fakeLateHub) ListAllEventIDs(_ context.Context, afterID string, limit int) (transport.EventIDPage, error) {
	h.allCalls = append(h.allCalls, afterID)
	return h.page(afterID, limit)
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

// offlineEvents builds, on a separate store, a create of TEST-9 and two
// later description edits, and returns those three events in pull order.
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

var (
	sweepHubT1 = time.Date(2026, 9, 24, 10, 0, 0, 123456000, time.UTC)
	sweepHubT2 = time.Date(2026, 9, 24, 10, 0, 5, 0, time.UTC)
)

// TestSweepLateEvents_FirstSweep_PagesFullHistoryAndRecordsFirstHubTime:
// with no recorded sweep, the sweep pages the full id history, applies the
// missing events in pull order although the hub returned them reversed and
// across fetch chunks, and records the hub time of the FIRST listing page.
func TestSweepLateEvents_FirstSweep_PagesFullHistoryAndRecordsFirstHubTime(t *testing.T) {
	initTestApp(t)
	hub := &fakeLateHub{events: offlineEvents(t), hubNows: []time.Time{sweepHubT1, sweepHubT2}}
	ids := eventIDs(hub.events)
	sort.Strings(ids)
	var stderr bytes.Buffer

	got, err := sweepLateEvents(context.Background(), &stderr, hub, app.store, 2)

	require.NoError(t, err)
	require.Equal(t, lateEventSweep{Recovered: 3, FullDiff: true}, got)
	require.Empty(t, hub.sinceCalls, "a first sweep never lists a window")
	require.Equal(t, []string{"", ids[1]}, hub.allCalls,
		"three ids in pages of two: the second page starts after the second id")
	node, err := app.store.GetNode(context.Background(), "TEST-9")
	require.NoError(t, err)
	require.Equal(t, "second edit", node.Description, "create, then both edits, in Lamport order")
	require.Equal(t, "2026-09-24T10:00:00.123456Z", lastSweep(t))
	require.Contains(t, stderr.String(), "comparing the full hub event history once")
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
	require.Empty(t, hub.allCalls, "a recorded sweep never diffs the full history")
	require.Equal(t, []transport.EventIDCursor{{
		CreatedAt: time.Date(2026, 9, 24, 8, 45, 0, 500000000, time.UTC),
	}}, hub.sinceCalls)
	require.Equal(t, "2026-09-24T10:00:00.123456Z", lastSweep(t))
}

// TestSweepLateEvents_UnreadableLastSweep_FallsBackToFullDiff: a recorded
// value that is not a time is reported and replaced by a full diff.
func TestSweepLateEvents_UnreadableLastSweep_FallsBackToFullDiff(t *testing.T) {
	initTestApp(t)
	setLastSweep(t, "not-a-time")
	hub := &fakeLateHub{hubNows: []time.Time{sweepHubT1}}
	var stderr bytes.Buffer

	got, err := sweepLateEvents(context.Background(), &stderr, hub, app.store, 10)

	require.NoError(t, err)
	require.True(t, got.FullDiff)
	require.Len(t, hub.allCalls, 1)
	require.Contains(t, stderr.String(), `meta.sync.last_sweep_at "not-a-time" is not a time`)
	require.Equal(t, "2026-09-24T10:00:00.123456Z", lastSweep(t))
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
