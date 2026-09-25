// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// Real-Postgres tests for the late-event sweep of `mtix sync pull`
// (MTIX-95.5; ADR-006 D5, review F-43). Pull asks the hub for
// lamport_clock > cursor, so an event that an offline store pushes with a
// Lamport clock below a busier peer's cursor is never returned by the
// cursor loop. The sweep that follows the cursor loop lists the hub event
// ids created since the previous sweep's hub time (minus a 15-minute
// overlap), fetches the ones this store does not hold and applies them.
//
// Every test here is gated on MTIX_PG_TEST_DSN (requireCmdPG) and skips
// without it. The peer is app.store (it pulls with the real runSyncPull);
// the offline writer is a second real store that pushes with the real
// pushLoop.

// sweepFixture is one hub, the peer (app.store) and an offline store B.
type sweepFixture struct {
	dsn  string
	pool *transport.Pool
	b    *sqlite.Store
}

// newSweepFixture opens a fresh hub, initializes the peer as app.store and
// opens the offline store B with its own author identity.
func newSweepFixture(t *testing.T) *sweepFixture {
	t.Helper()
	dsn := requireCmdPG(t)
	pool := openCmdHub(t)
	initTestApp(t)
	b, err := sqlite.New(filepath.Join(t.TempDir(), ".mtix"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = b.Close() })
	_, err = b.WriteDB().ExecContext(context.Background(),
		`UPDATE meta SET value = 'agent-b' WHERE key = 'meta.sync.author_id'`)
	require.NoError(t, err)
	return &sweepFixture{dsn: dsn, pool: pool, b: b}
}

// seedSharedNode creates TEST-1 on the peer, pushes it, and lets B pull it,
// so both stores hold the node before B goes offline.
func (f *sweepFixture) seedSharedNode(t *testing.T) {
	t.Helper()
	require.NoError(t, runCreate("shared", "", "", 3, "", "", "", "", ""))
	f.pushPeer(t)
	var stderr bytes.Buffer
	_, _, err := pullLoop(context.Background(), testIngest(&stderr), f.pool, f.b, 0, 100)
	require.NoError(t, err, "B pulls the shared node: %s", stderr.String())
	_, err = f.b.GetNode(context.Background(), "TEST-1")
	require.NoError(t, err, "B must hold TEST-1 before going offline")
}

// editPeer makes n description edits on the peer, raising its Lamport clock
// well above B's.
func (f *sweepFixture) editPeer(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		desc := fmt.Sprintf("peer edit %d", i)
		require.NoError(t, app.store.UpdateNode(context.Background(), "TEST-1",
			&store.NodeUpdate{Description: &desc}))
	}
}

// pushPeer pushes the peer's pending events with the real pushLoop.
func (f *sweepFixture) pushPeer(t *testing.T) {
	t.Helper()
	var stderr bytes.Buffer
	_, _, _, _, err := pushLoop(context.Background(), &stderr, f.pool, app.store)
	require.NoError(t, err, "peer push: %s", stderr.String())
}

// pushB pushes B's pending events and returns them as they were queued.
func (f *sweepFixture) pushB(t *testing.T) []*model.SyncEvent {
	t.Helper()
	ctx := context.Background()
	pending, err := readPendingBatch(ctx, f.b, 1000)
	require.NoError(t, err)
	var stderr bytes.Buffer
	_, _, _, _, err = pushLoop(ctx, &stderr, f.pool, f.b)
	require.NoError(t, err, "B push: %s", stderr.String())
	return pending
}

// pullPeer runs the real `mtix sync pull` on the peer and returns stdout.
func (f *sweepFixture) pullPeer(t *testing.T, limit int) string {
	t.Helper()
	stdout, _ := f.pullPeerStreams(t, limit)
	return stdout
}

// pullPeerStreams runs the real `mtix sync pull` on the peer, requires it to
// succeed, and returns its stdout and stderr.
func (f *sweepFixture) pullPeerStreams(t *testing.T, limit int) (string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := f.runPeerPull(&stdout, &stderr, limit)
	require.NoError(t, err, "peer pull: %s", stderr.String())
	return stdout.String(), stderr.String()
}

// runPeerPull runs the real `mtix sync pull` on the peer and returns its
// error.
func (f *sweepFixture) runPeerPull(stdout, stderr *bytes.Buffer, limit int) error {
	return runSyncPull(context.Background(), stdout, stderr,
		[]string{f.dsn}, transport.Options{InsecureTLS: true}, limit)
}

// peerMeta returns one meta value of the peer store.
func (f *sweepFixture) peerMeta(t *testing.T, key string) string {
	t.Helper()
	var v string
	require.NoError(t, app.store.QueryRow(context.Background(),
		`SELECT value FROM meta WHERE key = ?`, key).Scan(&v), "meta %s must exist", key)
	return v
}

// hubEventCount returns the number of events on the hub.
func (f *sweepFixture) hubEventCount(t *testing.T) int {
	t.Helper()
	var n int
	require.NoError(t, f.pool.Inner().QueryRow(context.Background(),
		`SELECT count(*) FROM sync_events`).Scan(&n))
	return n
}

// peerCursor returns the peer's Lamport pull cursor.
func (f *sweepFixture) peerCursor(t *testing.T) int64 {
	t.Helper()
	c, err := readLastPulledClock(context.Background(), app.store)
	require.NoError(t, err)
	return c
}

// appliedOnPeer reports whether the peer recorded eventID in applied_events,
// i.e. whether the peer received that event.
func (f *sweepFixture) appliedOnPeer(t *testing.T, eventID string) bool {
	t.Helper()
	var n int
	require.NoError(t, app.store.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM applied_events WHERE event_id = ?`, eventID).Scan(&n))
	return n == 1
}

// hubNow reads the hub's clock.
func (f *sweepFixture) hubNow(t *testing.T) time.Time {
	t.Helper()
	var now time.Time
	require.NoError(t, f.pool.Inner().QueryRow(context.Background(),
		`SELECT now()`).Scan(&now))
	return now
}

// setHubCreatedAt rewrites one hub event's created_at, to place it relative
// to a sweep window. The test hub is a throwaway database.
func (f *sweepFixture) setHubCreatedAt(t *testing.T, eventIDs []string, at time.Time) {
	t.Helper()
	tag, err := f.pool.Inner().Exec(context.Background(),
		`UPDATE sync_events SET created_at = $1 WHERE event_id = ANY($2)`, at, eventIDs)
	require.NoError(t, err)
	require.Equal(t, int64(len(eventIDs)), tag.RowsAffected())
}

// peerLastSweep returns the peer's meta.sync.last_sweep_at, parsed.
func (f *sweepFixture) peerLastSweep(t *testing.T) time.Time {
	t.Helper()
	var raw string
	require.NoError(t, app.store.QueryRow(context.Background(),
		`SELECT value FROM meta WHERE key = 'meta.sync.last_sweep_at'`).Scan(&raw),
		"meta.sync.last_sweep_at must exist")
	at, err := time.Parse(time.RFC3339Nano, raw)
	require.NoError(t, err, "meta.sync.last_sweep_at %q must be an RFC3339 hub time", raw)
	// pgx returns timestamptz in the local zone; the stored value is UTC.
	require.Truef(t, strings.HasSuffix(raw, "Z"),
		"meta.sync.last_sweep_at %q must be recorded in UTC", raw)
	return at
}

// eventIDs returns the ids of events.
func eventIDs(events []*model.SyncEvent) []string {
	ids := make([]string, 0, len(events))
	for _, e := range events {
		ids = append(ids, e.EventID)
	}
	return ids
}

// TestPullSweep_LateLowLamportPush_IsPulled is ADR-006 S6 against a real
// hub: B clones the node and goes offline, the peer's cursor moves past
// B's clock, B pushes an edit stamped below the peer's cursor, and the
// peer's next pull applies it (MTIX-95.5 acceptance 1).
func TestPullSweep_LateLowLamportPush_IsPulled(t *testing.T) {
	f := newSweepFixture(t)
	ctx := context.Background()
	f.seedSharedNode(t)
	f.editPeer(t, 10)
	f.pushPeer(t)
	f.pullPeer(t, 100)
	cursor := f.peerCursor(t)

	title := "offline edit from B"
	require.NoError(t, f.b.UpdateNode(ctx, "TEST-1", &store.NodeUpdate{Title: &title}))
	late := f.pushB(t)
	require.Len(t, late, 1)
	require.Less(t, late[0].LamportClock, cursor,
		"precondition: B's event is stamped below the peer's cursor")

	out := f.pullPeer(t, 100)

	got, err := app.store.GetNode(ctx, "TEST-1")
	require.NoError(t, err)
	require.Equal(t, title, got.Title, "the late edit must reach the peer")
	require.True(t, f.appliedOnPeer(t, late[0].EventID),
		"the late event must be recorded in applied_events")
	require.Contains(t, out, "1 late events recovered")
	require.Equal(t, cursor, f.peerCursor(t),
		"the sweep must not move the Lamport cursor")
}

// TestPullSweep_RecoveredLateClaim_DoesNotRevertNewerDone: a claim that B
// made offline, older than the peer's done, is recovered by the sweep. It
// must go through the MTIX-95.10 winner rule: the node stays done with the
// peer's assignee, and the claim is recorded as received (MTIX-95.5
// acceptance 2, review F-43). Asserting the applied_events row is what
// makes this test fail before the sweep: without it the claim is never
// pulled and the node stays done anyway.
func TestPullSweep_RecoveredLateClaim_DoesNotRevertNewerDone(t *testing.T) {
	f := newSweepFixture(t)
	ctx := context.Background()
	f.seedSharedNode(t)
	f.editPeer(t, 5)
	require.NoError(t, app.store.ClaimNode(ctx, "TEST-1", "agent-a"))
	require.NoError(t, app.store.TransitionStatus(ctx, "TEST-1",
		model.StatusDone, "finished", "agent-a"))
	f.pushPeer(t)
	f.pullPeer(t, 100)
	cursor := f.peerCursor(t)

	require.NoError(t, f.b.ClaimNode(ctx, "TEST-1", "agent-b"))
	late := f.pushB(t)
	require.Len(t, late, 1)
	require.Equal(t, model.OpClaim, late[0].OpType)
	require.Less(t, late[0].LamportClock, cursor,
		"precondition: B's claim is stamped below the peer's cursor")

	out := f.pullPeer(t, 100)

	require.True(t, f.appliedOnPeer(t, late[0].EventID),
		"the late claim must be received (recorded in applied_events)")
	require.Contains(t, out, "1 late events recovered")
	got, err := app.store.GetNode(ctx, "TEST-1")
	require.NoError(t, err)
	require.Equal(t, model.StatusDone, got.Status,
		"an older recovered claim must not revert the newer done")
	require.Equal(t, "agent-a", got.Assignee,
		"the losing claim must not change the assignee")
}

// TestPullSweep_FirstRunFullScan_RecoversHistoricalGaps: a store that has
// never swept (meta.sync.last_sweep_at empty, as after an upgrade) diffs
// the full hub id history once, in pages, and applies every event it is
// missing, including events far older than any sweep window (MTIX-95.5
// acceptance 3). The pull's --limit of 2 is the sweep's page size: the
// full diff reports on stderr that it compared every hub id in
// ceil(ids / 2) pages, and the recovered events are fetched and applied
// in several batches, where B's create must apply before B's edit of the
// same node.
func TestPullSweep_FirstRunFullScan_RecoversHistoricalGaps(t *testing.T) {
	f := newSweepFixture(t)
	ctx := context.Background()
	f.seedSharedNode(t)
	f.editPeer(t, 10)
	f.pushPeer(t)
	f.pullPeer(t, 100)
	cursor := f.peerCursor(t)

	title := "historical edit from B"
	require.NoError(t, f.b.UpdateNode(ctx, "TEST-1", &store.NodeUpdate{Title: &title}))
	require.NoError(t, f.b.CreateNode(ctx, mkPGNode("TEST-2", "", 0, 2, "B's node")))
	desc := "written by B while offline"
	require.NoError(t, f.b.UpdateNode(ctx, "TEST-2", &store.NodeUpdate{Description: &desc}))
	late := f.pushB(t)
	require.Len(t, late, 3)
	for _, e := range late {
		require.Less(t, e.LamportClock, cursor,
			"precondition: every B event is stamped below the peer's cursor")
	}
	f.setHubCreatedAt(t, eventIDs(late), f.hubNow(t).Add(-30*24*time.Hour))
	_, err := app.store.WriteDB().ExecContext(ctx,
		`UPDATE meta SET value = '' WHERE key = 'meta.sync.last_sweep_at'`)
	require.NoError(t, err)

	hubIDs := f.hubEventCount(t)
	out, errOut := f.pullPeerStreams(t, 2)

	require.Contains(t, out, "3 late events recovered")
	require.Contains(t, errOut, fmt.Sprintf("late-event sweep: compared %d hub event ids in %d pages",
		hubIDs, (hubIDs+1)/2), "the full diff pages by --limit")
	for _, e := range late {
		require.Truef(t, f.appliedOnPeer(t, e.EventID),
			"historical event %s (%s) must be recovered", e.EventID, e.OpType)
	}
	shared, err := app.store.GetNode(ctx, "TEST-1")
	require.NoError(t, err)
	require.Equal(t, title, shared.Title)
	created, err := app.store.GetNode(ctx, "TEST-2")
	require.NoError(t, err, "B's node must be recovered")
	require.Equal(t, desc, created.Description)
	require.False(t, f.peerLastSweep(t).IsZero(),
		"the first sweep records its hub time")
}

// TestPullSweep_UsesHubClock: the sweep window and meta.sync.last_sweep_at
// come from the hub's clock, so a skewed client clock changes nothing: the
// recorded sweep time lies between two hub-clock readings taken around the
// pull, and a late event is still recovered (MTIX-95.5 acceptance 4).
func TestPullSweep_UsesHubClock(t *testing.T) {
	tests := []struct {
		name string
		skew time.Duration
	}{
		{"client clock three hours ahead", 3 * time.Hour},
		{"client clock three hours behind", -3 * time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newSweepFixture(t)
			ctx := context.Background()
			f.seedSharedNode(t)
			f.editPeer(t, 5)
			f.pushPeer(t)
			app.store.SetClock(func() time.Time { return time.Now().UTC().Add(tt.skew) })

			before := f.hubNow(t)
			f.pullPeer(t, 100)
			after := f.hubNow(t)
			requireWithin(t, f.peerLastSweep(t), before, after)

			title := "late edit under a skewed peer clock"
			require.NoError(t, f.b.UpdateNode(ctx, "TEST-1", &store.NodeUpdate{Title: &title}))
			late := f.pushB(t)
			require.Len(t, late, 1)

			before = f.hubNow(t)
			f.pullPeer(t, 100)
			after = f.hubNow(t)
			requireWithin(t, f.peerLastSweep(t), before, after)
			require.True(t, f.appliedOnPeer(t, late[0].EventID),
				"a skewed client clock must not hide a late event")
		})
	}
}

// TestPullSweep_EditListedBeforeItsCreate_AppliesInLamportOrder: the sweep
// lists hub ids in (created_at, event_id) order, which is not causal for
// one client's own events: one push transaction gives them all the same
// created_at, and after a clock step-back a later edit can carry a smaller
// event id than the create it edits (a renumbered create can also be pushed
// after its edits). Here B's edit of its own new node is listed one page
// before the node's create, for --limit 1, 2 and 3. Listing only stages the
// missing ids; the sweep applies them after the listing, in Lamport order,
// so the create applies first and the pull recovers the node (MTIX-95.5).
func TestPullSweep_EditListedBeforeItsCreate_AppliesInLamportOrder(t *testing.T) {
	for _, limit := range []int{1, 2, 3} {
		t.Run(fmt.Sprintf("limit %d", limit), func(t *testing.T) {
			f := newSweepFixture(t)
			ctx := context.Background()
			f.seedSharedNode(t)
			f.editPeer(t, 5)
			f.pushPeer(t)
			f.pullPeer(t, 100)
			require.NoError(t, f.b.CreateNode(ctx, mkPGNode("TEST-2", "", 0, 2, "B's node")))
			desc := "edited right after the create"
			require.NoError(t, f.b.UpdateNode(ctx, "TEST-2", &store.NodeUpdate{Description: &desc}))
			late := f.pushB(t) // one push transaction: one created_at for both
			require.Len(t, late, 2)
			create, edit := late[0], late[1]
			require.Equal(t, model.OpCreateNode, create.OpType)
			require.Less(t, create.LamportClock, edit.LamportClock)
			f.listEditBeforeCreate(t, create, edit, limit)
			_, err := app.store.WriteDB().ExecContext(ctx,
				`UPDATE meta SET value = '' WHERE key = 'meta.sync.last_sweep_at'`)
			require.NoError(t, err)

			out := f.pullPeer(t, limit)

			require.Contains(t, out, "2 late events recovered")
			node, err := app.store.GetNode(ctx, "TEST-2")
			require.NoError(t, err, "the create must apply before its edit")
			require.Equal(t, desc, node.Description)
			var pending int
			require.NoError(t, app.store.QueryRow(ctx,
				`SELECT COUNT(*) FROM sync_sweep_pending`).Scan(&pending))
			require.Zero(t, pending, "every staged event was applied")
		})
	}
}

// listEditBeforeCreate rewrites the hub so the full diff lists edit as the
// last id of its first page and create as the first id of the next page:
// edit gets an event id that sorts before create's (as after a client clock
// step-back), limit-1 of the peer's own events are listed first, and the
// pair comes right after them with one created_at (one push). The test hub
// is a throwaway database.
func (f *sweepFixture) listEditBeforeCreate(t *testing.T, create, edit *model.SyncEvent, limit int) {
	t.Helper()
	ctx := context.Background()
	stepBackID := "00" + edit.EventID[2:]
	require.Less(t, stepBackID, create.EventID)
	base := f.hubNow(t).Add(-24 * time.Hour)
	var fillers []string
	rows, err := f.pool.Inner().Query(ctx,
		`SELECT event_id FROM sync_events WHERE event_id <> ALL($1) ORDER BY event_id LIMIT $2`,
		[]string{create.EventID, edit.EventID}, limit-1)
	require.NoError(t, err)
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		fillers = append(fillers, id)
	}
	require.NoError(t, rows.Err())
	require.Len(t, fillers, limit-1)
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`UPDATE sync_events SET event_id = $1 WHERE event_id = $2`, []any{stepBackID, edit.EventID}},
		{`UPDATE sync_events SET created_at = $1 WHERE event_id = ANY($2)`, []any{base, fillers}},
		{`UPDATE sync_events SET created_at = $1 WHERE event_id = ANY($2)`,
			[]any{base.Add(time.Second), []string{stepBackID, create.EventID}}},
	} {
		_, err := f.pool.Inner().Exec(ctx, stmt.sql, stmt.args...)
		require.NoError(t, err)
	}
	var first []string
	rows, err = f.pool.Inner().Query(ctx,
		`SELECT event_id FROM sync_events ORDER BY created_at, event_id LIMIT $1`, limit+1)
	require.NoError(t, err)
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		first = append(first, id)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, append(append([]string{}, fillers...), stepBackID, create.EventID), first,
		"precondition: the edit ends page one and its create starts page two")
}

// TestPullSweep_FirstRunUnappliableEvent_QuarantinedSweepCompletes: a
// first full diff that meets a late event it cannot apply (its hub payload
// names a field apply rejects) quarantines that event with source "sweep"
// and completes: the other late events apply, nothing stays staged, and
// meta.sync.last_sweep_at records the hub time read before the first page
// (MTIX-95.5, MTIX-95.11). The pull succeeds and reports the held event.
// Before MTIX-95.11 the event stopped the sweep on every pull. (Resuming an
// interrupted sweep from its saved progress is covered by the PG-free tests
// in sync_pull_sweep_test.go.)
func TestPullSweep_FirstRunUnappliableEvent_QuarantinedSweepCompletes(t *testing.T) {
	f := newSweepFixture(t)
	ctx := context.Background()
	f.seedSharedNode(t)
	f.editPeer(t, 5)
	f.pushPeer(t)
	f.pullPeer(t, 100)
	title := "historical edit from B"
	require.NoError(t, f.b.UpdateNode(ctx, "TEST-1", &store.NodeUpdate{Title: &title}))
	require.NoError(t, f.b.CreateNode(ctx, mkPGNode("TEST-2", "", 0, 2, "B's node")))
	desc := "written by B while offline"
	require.NoError(t, f.b.UpdateNode(ctx, "TEST-2", &store.NodeUpdate{Description: &desc}))
	late := f.pushB(t)
	require.Len(t, late, 3)
	_, err := f.pool.Inner().Exec(ctx,
		`UPDATE sync_events SET payload = '{"field_name":"not_a_field","new_value":"x"}'::jsonb
		 WHERE event_id = $1`, late[2].EventID)
	require.NoError(t, err)
	_, err = app.store.WriteDB().ExecContext(ctx,
		`UPDATE meta SET value = '' WHERE key = 'meta.sync.last_sweep_at'`)
	require.NoError(t, err)

	before := f.hubNow(t)
	out, errOut := f.pullPeerStreams(t, 1)
	after := f.hubNow(t)

	require.Contains(t, errOut, "quarantined event "+late[2].EventID+" (sweep pass)")
	require.Contains(t, out, "2 late events recovered")
	require.Contains(t, out, "quarantine: 1 pulled events held")
	q := quarantined(t)[late[2].EventID]
	require.Equal(t, "sweep", q.Source)
	require.Contains(t, q.Reason, "not_a_field")
	require.False(t, f.appliedOnPeer(t, late[2].EventID))
	require.True(t, f.appliedOnPeer(t, late[0].EventID))
	require.True(t, f.appliedOnPeer(t, late[1].EventID))
	require.Empty(t, f.stagedOnPeer(t))
	recorded := f.peerLastSweep(t)
	requireWithin(t, recorded, before, after)
	for _, key := range []string{"meta.sync.sweep_after_id",
		"meta.sync.sweep_after_created_at", "meta.sync.sweep_started_at"} {
		require.Equalf(t, "", f.peerMeta(t, key), "%s is cleared on completion", key)
	}
	created, err := app.store.GetNode(ctx, "TEST-2")
	require.NoError(t, err)
	require.Equal(t, "B's node", created.Title)
}

// stagedOnPeer returns the event ids staged in the peer's sweep table.
func (f *sweepFixture) stagedOnPeer(t *testing.T) []string {
	t.Helper()
	rows, err := app.store.Query(context.Background(),
		`SELECT event_id FROM sync_sweep_pending ORDER BY event_id`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		ids = append(ids, id)
	}
	require.NoError(t, rows.Err())
	return ids
}

// TestRunSyncClone_ResetsLateEventSweepState: a clone rebuilds the store
// from the hub, so it clears meta.sync.last_sweep_at, any saved sweep
// progress and any staged ids; the first pull after it diffs the full hub
// history.
func TestRunSyncClone_ResetsLateEventSweepState(t *testing.T) {
	dsn := requireCmdPG(t)
	_ = openCmdHub(t)
	initTestApp(t)
	ctx := context.Background()
	for _, key := range []string{"meta.sync.last_sweep_at", "meta.sync.sweep_after_id",
		"meta.sync.sweep_after_created_at", "meta.sync.sweep_started_at"} {
		_, err := app.store.WriteDB().ExecContext(ctx,
			`INSERT INTO meta (key, value) VALUES (?, 'stale') ON CONFLICT(key) DO UPDATE SET value = 'stale'`, key)
		require.NoError(t, err)
	}
	_, err := app.store.WriteDB().ExecContext(ctx,
		`INSERT INTO sync_sweep_pending (event_id) VALUES ('stale-staged-id')`)
	require.NoError(t, err)
	quarantineN(t, 1)

	var stdout, stderr bytes.Buffer
	require.NoError(t, runSyncClone(ctx, &stdout, &stderr,
		[]string{dsn}, transport.Options{InsecureTLS: true}, false, 100), stderr.String())

	for _, key := range []string{"meta.sync.last_sweep_at", "meta.sync.sweep_after_id",
		"meta.sync.sweep_after_created_at", "meta.sync.sweep_started_at"} {
		var v string
		require.NoError(t, app.store.QueryRow(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&v))
		require.Equalf(t, "", v, "%s must be reset by clone", key)
	}
	var staged int
	require.NoError(t, app.store.QueryRow(ctx, `SELECT COUNT(*) FROM sync_sweep_pending`).Scan(&staged))
	require.Zero(t, staged, "clone clears the staged ids")
	require.Empty(t, quarantined(t), "clone clears the quarantine (MTIX-95.11): it rebuilds the store from the hub")
}

// TestRunSyncPull_SweepEventFailsApply_QuarantinedPullSucceeds: when a
// recovered event cannot be applied, it is quarantined (MTIX-95.11) and the
// pull succeeds: no sync error is counted (meta.sync.consecutive_errors),
// meta.sync.last_sweep_at advances, and the event is not applied. The late
// event's hub payload is rewritten to name a field that apply rejects; the
// test hub is a throwaway database. Before MTIX-95.11 the pull failed with
// the late-event sweep error on every attempt.
func TestRunSyncPull_SweepEventFailsApply_QuarantinedPullSucceeds(t *testing.T) {
	f := newSweepFixture(t)
	ctx := context.Background()
	f.seedSharedNode(t)
	f.editPeer(t, 5)
	f.pushPeer(t)
	f.pullPeer(t, 100)
	lastSweep := f.peerMeta(t, "meta.sync.last_sweep_at")
	require.Equal(t, "0", f.peerMeta(t, "meta.sync.consecutive_errors"))

	title := "offline edit that cannot apply"
	require.NoError(t, f.b.UpdateNode(ctx, "TEST-1", &store.NodeUpdate{Title: &title}))
	late := f.pushB(t)
	require.Len(t, late, 1)
	_, err := f.pool.Inner().Exec(ctx,
		`UPDATE sync_events SET payload = '{"field_name":"not_a_field","new_value":"x"}'::jsonb
		 WHERE event_id = $1`, late[0].EventID)
	require.NoError(t, err)

	var stdout, stderr bytes.Buffer
	err = f.runPeerPull(&stdout, &stderr, 100)

	require.NoError(t, err, stderr.String())
	require.Equal(t, "0", f.peerMeta(t, "meta.sync.consecutive_errors"))
	require.NotEqual(t, lastSweep, f.peerMeta(t, "meta.sync.last_sweep_at"),
		"the sweep completed and moved its window")
	require.False(t, f.appliedOnPeer(t, late[0].EventID))
	require.Equal(t, "sweep", quarantined(t)[late[0].EventID].Source)
	require.Contains(t, stdout.String(), "pull complete")
	require.Contains(t, stdout.String(), "quarantine: 1 pulled events held")
}

// requireWithin asserts lo <= got <= hi.
func requireWithin(t *testing.T, got, lo, hi time.Time) {
	t.Helper()
	require.Falsef(t, got.Before(lo), "last sweep %s is before hub time %s", got, lo)
	require.Falsef(t, got.After(hi), "last sweep %s is after hub time %s", got, hi)
}

// TestPullSweep_Window_StartsFifteenMinutesBeforeLastSweep: the sweep lists
// hub events created since the previous sweep's hub time minus a 15-minute
// overlap. An event committed late by a push transaction that started 14
// minutes before the previous sweep is recovered; one created 16 minutes
// before it is outside the window (only the first sweep diffs the full
// history) (MTIX-95.5 acceptance 1).
func TestPullSweep_Window_StartsFifteenMinutesBeforeLastSweep(t *testing.T) {
	tests := []struct {
		name      string
		before    time.Duration
		recovered bool
	}{
		{"created 14 minutes before the last sweep", 14 * time.Minute, true},
		{"created 16 minutes before the last sweep", 16 * time.Minute, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newSweepFixture(t)
			ctx := context.Background()
			f.seedSharedNode(t)
			f.editPeer(t, 5)
			f.pushPeer(t)
			f.pullPeer(t, 100)
			last := f.peerLastSweep(t)

			title := "late edit near the window edge"
			require.NoError(t, f.b.UpdateNode(ctx, "TEST-1", &store.NodeUpdate{Title: &title}))
			late := f.pushB(t)
			require.Len(t, late, 1)
			f.setHubCreatedAt(t, eventIDs(late), last.Add(-tt.before))

			f.pullPeer(t, 100)

			require.Equal(t, tt.recovered, f.appliedOnPeer(t, late[0].EventID))
		})
	}
}
