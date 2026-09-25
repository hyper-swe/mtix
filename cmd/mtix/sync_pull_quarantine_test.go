// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/sync/validator"
)

// PG-free tests of the pull quarantine (MTIX-95.11): every pulled event is
// checked against the FR-18.7 envelope caps and the Lamport jump bound
// before it is applied, applied in its own savepoint, and quarantined in
// the local sync_quarantine table when it fails, so the batch and the
// cursor continue past it; quarantined events are retried at the start of
// every pull (and at the end of a pull that applied something). The fake
// hub is fakeLateHub (sync_pull_sweep_test.go).

// testIngest returns the ingest settings of a test pull: progress and
// warnings to w (discarded when nil), this machine's clock, the default
// Lamport jump bound and the CLI version "test".
func testIngest(w io.Writer) pullIngest {
	if w == nil {
		w = io.Discard
	}
	return pullIngest{stderr: w, now: time.Now, maxJump: validator.DefaultMaxLamportJump, cliVersion: "test"}
}

// quarantined returns the peer store's sync_quarantine rows keyed by id.
func quarantined(t *testing.T) map[string]sqlite.QuarantinedEvent {
	t.Helper()
	page, err := app.store.QuarantinePage(context.Background(), nil, 1000)
	require.NoError(t, err)
	out := make(map[string]sqlite.QuarantinedEvent, len(page))
	for _, q := range page {
		out[q.EventID] = q
	}
	return out
}

// peerLamport reads the peer store's local Lamport clock.
func peerLamport(t *testing.T) int64 {
	t.Helper()
	var v int64
	require.NoError(t, app.store.QueryRow(context.Background(),
		`SELECT CAST(value AS INTEGER) FROM meta WHERE key = 'meta.sync.lamport'`).Scan(&v))
	return v
}

// requireNotApplied asserts that the peer holds e in neither sync_events
// nor applied_events: its apply left nothing behind.
func requireNotApplied(t *testing.T, e *model.SyncEvent) {
	t.Helper()
	require.Zero(t, countTestRows(t, `SELECT COUNT(*) FROM sync_events WHERE event_id = ?`, e.EventID),
		"no mirror row of %s survives", e.EventID)
	require.Zero(t, countTestRows(t, `SELECT COUNT(*) FROM applied_events WHERE event_id = ?`, e.EventID),
		"%s is not recorded as applied", e.EventID)
}

// requireQuarantinedAs asserts e is quarantined from source with a reason
// that contains want, with the raw event it was pulled as.
func requireQuarantinedAs(t *testing.T, e *model.SyncEvent, source, want string) sqlite.QuarantinedEvent {
	t.Helper()
	q, ok := quarantined(t)[e.EventID]
	require.Truef(t, ok, "event %s must be quarantined", e.EventID)
	require.Equal(t, source, q.Source)
	require.Contains(t, q.Reason, want)
	require.Equal(t, "test", q.CLIVersion)
	var raw model.SyncEvent
	require.NoError(t, json.Unmarshal([]byte(q.RawEvent), &raw))
	require.Equal(t, e.EventID, raw.EventID)
	require.Equal(t, e.LamportClock, raw.LamportClock)
	require.JSONEq(t, string(e.Payload), string(raw.Payload))
	return q
}

// oversizedPayload is a valid create_node payload just over the FR-18.7
// 64 KB cap.
func oversizedPayload() json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"title":"too big","description":%q}`,
		strings.Repeat("d", validator.MaxPayloadBytes)))
}

// TestPull_OversizedHubEvent_QuarantinedNotApplied: a pulled event that
// fails the FR-18.7 envelope validation (payload size or depth, Lamport or
// vector-clock caps, an id grammar) is quarantined, not applied, and does
// not fail the pull (MTIX-95.11 acceptance 1). Before this change an
// oversized create applied and a malformed one failed the whole pull.
func TestPull_OversizedHubEvent_QuarantinedNotApplied(t *testing.T) {
	tests := []struct {
		name         string
		mutate       func(e *model.SyncEvent)
		wantReason   string
		clockRefused bool
	}{
		{"payload over 64 KB", func(e *model.SyncEvent) { e.Payload = oversizedPayload() },
			"payload too large", false},
		{"payload nested deeper than 10", func(e *model.SyncEvent) {
			e.Payload = json.RawMessage(`{"title":"deep","x":` + strings.Repeat("[", 10) +
				strings.Repeat("]", 10) + `}`)
		}, "payload nesting too deep", false},
		{"lamport at 2^53", func(e *model.SyncEvent) { e.LamportClock = validator.MaxLamportClock },
			"lamport_clock at or above 2^53", true},
		{"vector clock entry at 2^53", func(e *model.SyncEvent) {
			e.VectorClock = model.VectorClock{e.AuthorID: model.MaxVectorClockValue}
		}, ">= 2^53", false},
		{"vector clock over 100 entries", func(e *model.SyncEvent) {
			vc := model.VectorClock{}
			for i := 0; i <= model.MaxVectorClockEntries; i++ {
				vc[fmt.Sprintf("author%d", i)] = 1
			}
			e.VectorClock = vc
		}, "entries (max 100)", false},
		{"author_id grammar", func(e *model.SyncEvent) { e.AuthorID = "Not Valid" }, "author_id", false},
		{"author_machine_hash grammar", func(e *model.SyncEvent) { e.AuthorMachineHash = "nothex" },
			"author_machine_hash", false},
		{"project_prefix grammar", func(e *model.SyncEvent) { e.ProjectPrefix = "test" }, "project_prefix", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			ctx := context.Background()
			bad := *offlineEvents(t)[0]
			tt.mutate(&bad)
			hub := &fakeLateHub{events: []*model.SyncEvent{&bad}, pullEvents: []*model.SyncEvent{&bad},
				hubNows: []time.Time{sweepHubT1}}
			var stderr bytes.Buffer

			got, err := pullThenSweep(ctx, testIngest(&stderr), hub, app.store, transport.PullCursor{}, 100)

			require.NoError(t, err, "a quarantined event does not fail the pull: %s", stderr.String())
			require.Zero(t, got.pulled, "nothing was applied")
			_, err = app.store.GetNode(ctx, "TEST-9")
			require.ErrorIs(t, err, model.ErrNotFound, "the event's node was not created")
			requireNotApplied(t, &bad)
			q := requireQuarantinedAs(t, &bad, "pull", tt.wantReason)
			require.Equal(t, 1, q.Attempts)
			require.Zero(t, peerLamport(t), "a quarantined event never advances the local clock")
			require.Contains(t, stderr.String(), "quarantined event "+bad.EventID)
			require.Empty(t, hub.fetched, "the late-event sweep leaves a quarantined event to the quarantine")
			if !tt.clockRefused {
				requireCursor(t, bad.LamportClock, "the cursor moves past a quarantined event")
				return
			}
			requireLaterEventViaCursorPass(t, hub)
		})
	}
}

// requireCursor asserts the peer's saved pull cursor.
func requireCursor(t *testing.T, want int64, msg string) {
	t.Helper()
	cursor, err := readLastPulledClock(context.Background(), app.store)
	require.NoError(t, err)
	require.Equal(t, want, cursor.Lamport, msg)
}

// requireLaterEventViaCursorPass asserts that the saved cursor did not move
// to a refused clock and that a later valid event still arrives through the
// cursor pass: the hub's next cursor pass returns a create stamped 1, which
// the sweep's listing does not hold, so only the cursor pass can deliver it.
func requireLaterEventViaCursorPass(t *testing.T, hub *fakeLateHub) {
	t.Helper()
	requireCursor(t, 0, "the cursor never moves to a refused clock")
	from, _, _ := linkDepEvents(t)
	hub.pullEvents = append(hub.pullEvents, from)
	got, err := pullThenSweep(context.Background(), testIngest(nil), hub, app.store, transport.PullCursor{}, 100)
	require.NoError(t, err)
	require.Equal(t, 1, got.pulled, "the later event arrives through the cursor pass")
	_, err = app.store.GetNode(context.Background(), "TEST-1")
	require.NoError(t, err)
	require.Empty(t, hub.fetched, "not through the late-event sweep")
	requireCursor(t, from.LamportClock, "the cursor moves to the later event")
}

// TestPull_FutureStampedHubEvent_AppliedWithWarning: the 24h clock-relative
// check only warns at ingest (review F-25): an event stamped two days ahead
// of this machine's clock is applied and a WARN names it.
func TestPull_FutureStampedHubEvent_AppliedWithWarning(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	ahead := *offlineEvents(t)[0]
	ahead.WallClockTS = time.Now().Add(48 * time.Hour).UnixMilli()
	hub := &fakeLateHub{pullEvents: []*model.SyncEvent{&ahead}, hubNows: []time.Time{sweepHubT1}}
	var stderr bytes.Buffer

	got, err := pullThenSweep(ctx, testIngest(&stderr), hub, app.store, transport.PullCursor{}, 100)

	require.NoError(t, err)
	require.Equal(t, 1, got.pulled)
	_, err = app.store.GetNode(ctx, "TEST-9")
	require.NoError(t, err, "the event applied")
	require.Empty(t, quarantined(t))
	require.Contains(t, stderr.String(),
		"WARN: pull: event "+ahead.EventID+" is stamped more than 24h ahead of this machine's clock")
}

// TestPull_ExtremeLamport_QuarantinedClockUnchanged: an event stamped more
// than sync.max_lamport_jump above the local clock is quarantined; neither
// the local clock nor the pull cursor moves to it, so the replica's next
// push still passes the push validation it failed before (MTIX-95.11
// acceptance 2). An event exactly the bound above the clock applies.
func TestPull_ExtremeLamport_QuarantinedClockUnchanged(t *testing.T) {
	tests := []struct {
		name        string
		maxJump     int64
		lamport     int64
		wantHeld    bool
		wantLamport int64
	}{
		{"default bound, clock just below 2^53", validator.DefaultMaxLamportJump,
			validator.MaxLamportClock - 1, true, 1},
		{"bound 10, eleven above", 10, 12, true, 1},
		{"bound 10, exactly ten above", 10, 11, false, 11},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			ctx := context.Background()
			events := offlineEvents(t)
			extreme := *events[1]
			extreme.LamportClock = tt.lamport
			hub := &fakeLateHub{pullEvents: []*model.SyncEvent{events[0], &extreme},
				hubNows: []time.Time{sweepHubT1}}
			in := testIngest(nil)
			in.maxJump = tt.maxJump

			_, err := pullThenSweep(ctx, in, hub, app.store, transport.PullCursor{}, 100)

			require.NoError(t, err)
			require.Equal(t, tt.wantLamport, peerLamport(t))
			cursor, err := readLastPulledClock(ctx, app.store)
			require.NoError(t, err)
			require.Equal(t, tt.wantLamport, cursor.Lamport, "the cursor never moves to a refused clock")
			if !tt.wantHeld {
				require.Empty(t, quarantined(t))
				return
			}
			requireNotApplied(t, &extreme)
			requireQuarantinedAs(t, &extreme, "pull", "sync.max_lamport_jump")

			require.NoError(t, runCreate("next local change", "", "", 3, "", "", "", "", ""))
			pending, err := readPendingBatch(ctx, app.store, 10)
			require.NoError(t, err)
			require.Len(t, pending, 1)
			require.Equal(t, tt.wantLamport+1, pending[0].LamportClock)
			require.NoError(t, validator.ValidateBatch(pending, time.Now().UTC(), nil),
				"the next push passes the hub's validation")
		})
	}
}

// TestPull_ExtremeLamport_RefetchedNextPullNotReapplied: the saved cursor
// stays below a refused clock while the pull pages past it in memory, so
// the pull ends, and the next pull fetches the refused events again,
// leaves them to the quarantine (no rewrite, no extra attempt) and still
// does not apply them. Two refused events one page apart show the
// in-memory paging: without it the loop would fetch the first one forever.
func TestPull_ExtremeLamport_RefetchedNextPullNotReapplied(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	events := offlineEvents(t)
	e1, e2 := *events[1], *events[2]
	e1.LamportClock = validator.MaxLamportClock - 2
	e2.LamportClock = validator.MaxLamportClock - 1
	hub := &fakeLateHub{pullEvents: []*model.SyncEvent{events[0], &e1, &e2},
		hubNows: []time.Time{sweepHubT1}}

	for pull := 0; pull < 2; pull++ {
		cursor, err := readLastPulledClock(ctx, app.store)
		require.NoError(t, err)
		_, err = pullThenSweep(ctx, testIngest(nil), hub, app.store, cursor, 1)
		require.NoError(t, err)
	}

	require.Equal(t, []int64{0, 1, e1.LamportClock, 1, e1.LamportClock}, hub.pullCalls,
		"each pull pages past the refused events in memory; the saved cursor stays at 1")
	q := quarantined(t)
	require.Equal(t, 2, q[e1.EventID].Attempts,
		"quarantined by the first pull and retried at its end (it applied the create); the second pull's cursor pass leaves it to the quarantine")
	require.Equal(t, 2, q[e2.EventID].Attempts)
	require.Equal(t, int64(1), peerLamport(t))
	requireNotApplied(t, &e1)
	requireNotApplied(t, &e2)
}

// TestPull_BatchContinuesPastQuarantine: each pulled event is applied in its
// own savepoint. An event whose apply fails (here an update of a field that
// apply rejects, after its mirror row was written) is rolled back and
// stored in sync_quarantine; the events after it in the same batch, and the
// later batches, still apply, and the cursor moves past it (MTIX-95.11
// acceptance 4). Before this change the whole batch rolled back and the
// pull failed on every attempt.
func TestPull_BatchContinuesPastQuarantine(t *testing.T) {
	for _, limit := range []int{100, 1} {
		t.Run(fmt.Sprintf("limit %d", limit), func(t *testing.T) {
			initTestApp(t)
			ctx := context.Background()
			events := offlineEvents(t)
			bad := *events[1]
			bad.Payload = json.RawMessage(`{"field_name":"not_a_field","new_value":"x"}`)
			hub := &fakeLateHub{events: []*model.SyncEvent{events[0], &bad, events[2]},
				pullEvents: []*model.SyncEvent{events[0], &bad, events[2]}, hubNows: []time.Time{sweepHubT1}}
			var stderr bytes.Buffer

			got, err := pullThenSweep(ctx, testIngest(&stderr), hub, app.store, transport.PullCursor{}, limit)

			require.NoError(t, err, stderr.String())
			require.Equal(t, 2, got.pulled, "the create and the last edit applied")
			node, err := app.store.GetNode(ctx, "TEST-9")
			require.NoError(t, err)
			require.Equal(t, "second edit", node.Description)
			requireNotApplied(t, &bad)
			requireQuarantinedAs(t, &bad, "pull", "not_a_field")
			cursor, err := readLastPulledClock(ctx, app.store)
			require.NoError(t, err)
			require.Equal(t, events[2].LamportClock, cursor.Lamport, "the cursor moves past the quarantined event")
			require.Equal(t, events[2].LamportClock, peerLamport(t))
			require.Empty(t, hub.fetched, "the sweep does not fetch the quarantined event again")
		})
	}
}

// linkDepEvents builds, on a separate store, TEST-1 and TEST-2 and a
// `blocks` dependency from TEST-1 to TEST-2, and returns the create of
// TEST-1, the create of TEST-2 and the link_dep, in pull order, with hub
// created_at values one second apart.
func linkDepEvents(t *testing.T) (createFrom, createTarget, link *model.SyncEvent) {
	t.Helper()
	ctx := context.Background()
	b, err := sqlite.New(filepath.Join(t.TempDir(), ".mtix"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = b.Close() })
	require.NoError(t, b.CreateNode(ctx, mkPGNode("TEST-1", "", 0, 1, "blocker")))
	require.NoError(t, b.CreateNode(ctx, mkPGNode("TEST-2", "", 0, 2, "blocked target")))
	require.NoError(t, b.AddDependency(ctx, &model.Dependency{
		FromID: "TEST-1", ToID: "TEST-2", DepType: model.DepTypeBlocks,
	}))
	events, err := readPendingBatch(ctx, b, 100)
	require.NoError(t, err)
	require.Len(t, events, 3)
	require.Equal(t, model.OpLinkDep, events[2].OpType)
	for i, e := range events {
		e.CreatedAt = offlineEventsPushedAt.Add(time.Duration(i) * time.Second)
	}
	return events[0], events[1], events[2]
}

// requireDependencyApplied asserts the peer holds the TEST-1 blocks TEST-2
// edge and that the link_dep left the quarantine.
func requireDependencyApplied(t *testing.T, link *model.SyncEvent) {
	t.Helper()
	require.Equal(t, 1, countTestRows(t,
		`SELECT COUNT(*) FROM dependencies WHERE from_id = 'TEST-1' AND to_id = 'TEST-2' AND dep_type = 'blocks'`))
	require.Equal(t, 1, countTestRows(t, `SELECT COUNT(*) FROM applied_events WHERE event_id = ?`, link.EventID))
	require.Empty(t, quarantined(t), "a successful retry removes the row")
}

// TestPull_LinkDepBeforeTargetArrives_QuarantinedThenRetriedAndApplied: a
// link_dep whose target node has not arrived is quarantined (the pull goes
// on); once the target arrives, the quarantined link_dep applies and its
// row is removed (MTIX-95.11 acceptance 3). The pull that brings the target
// retries the quarantine at its end; a pull that stopped before that retry
// is covered by the retry at the start of the next pull, which runs before
// the hub is contacted.
func TestPull_LinkDepBeforeTargetArrives_QuarantinedThenRetriedAndApplied(t *testing.T) {
	t.Run("retried at the end of the pull that brings the target", func(t *testing.T) {
		initTestApp(t)
		ctx := context.Background()
		from, target, link := linkDepEvents(t)
		first := &fakeLateHub{events: []*model.SyncEvent{from, link},
			pullEvents: []*model.SyncEvent{from, link}, hubNows: []time.Time{sweepHubT1}}
		_, err := pullThenSweep(ctx, testIngest(nil), first, app.store, transport.PullCursor{}, 100)
		require.NoError(t, err, "the link_dep without its target does not fail the pull")
		requireQuarantinedAs(t, link, "pull", "FOREIGN KEY")
		requireNotApplied(t, link)

		lateTarget := *target
		lateTarget.CreatedAt = sweepHubT1.Add(time.Second)
		second := &fakeLateHub{events: []*model.SyncEvent{from, &lateTarget, link},
			pullEvents: []*model.SyncEvent{from, target, link}, hubNows: []time.Time{sweepHubT2}}
		got, err := pullThenSweep(ctx, testIngest(nil), second, app.store, transport.CursorAt(link), 100)

		require.NoError(t, err)
		require.Equal(t, 1, got.sweep.Recovered, "the late-event sweep brings the target")
		require.Equal(t, 1, got.retried, "the end-of-pull retry applies the link_dep")
		requireDependencyApplied(t, link)
	})
	t.Run("retried at the start of the next pull", func(t *testing.T) {
		initTestApp(t)
		ctx := context.Background()
		from, target, link := linkDepEvents(t)
		_, err := applyPullBatch(ctx, testIngest(nil), app.store, quarantineSourcePull,
			[]*model.SyncEvent{from, link})
		require.NoError(t, err)
		requireQuarantinedAs(t, link, "pull", "FOREIGN KEY")
		_, err = applyPullBatch(ctx, testIngest(nil), app.store, quarantineSourcePull,
			[]*model.SyncEvent{target})
		require.NoError(t, err)
		require.Len(t, quarantined(t), 1, "precondition: the target arrived without a retry")
		t.Setenv(transport.EnvDSN, "")
		var stdout, stderr bytes.Buffer

		err = runSyncPull(ctx, &stdout, &stderr, nil, transport.Options{}, 100)

		require.ErrorContains(t, err, "mtix sync dsn:", "the hub is never reached here")
		requireDependencyApplied(t, link)
		require.Contains(t, stderr.String(), "1 quarantined events applied on retry")
	})
}

// TestRetryQuarantinedEvents_StillFailing_CountsAttemptKeepsRow: a retry
// that fails again keeps the row, counts the attempt and records its time;
// everything else stays as first recorded.
func TestRetryQuarantinedEvents_StillFailing_CountsAttemptKeepsRow(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	_, _, link := linkDepEvents(t)
	firstAt := time.Date(2026, 9, 25, 8, 0, 0, 0, time.UTC)
	retryAt := firstAt.Add(time.Hour)
	in := testIngest(nil)
	in.now = func() time.Time { return firstAt }
	_, err := applyPullBatch(ctx, in, app.store, quarantineSourcePull, []*model.SyncEvent{link})
	require.NoError(t, err)
	in.now = func() time.Time { return retryAt }
	in.cliVersion = "newer"

	got, err := retryQuarantinedEvents(ctx, in, app.store, 100)

	require.NoError(t, err)
	require.Equal(t, quarantineRetry{applied: 0, held: 1}, got)
	q := quarantined(t)[link.EventID]
	require.Equal(t, 2, q.Attempts)
	require.Equal(t, firstAt.Format(time.RFC3339Nano), q.FirstSeen)
	require.Equal(t, retryAt.Format(time.RFC3339Nano), q.LastAttempt)
	require.Equal(t, "test", q.CLIVersion, "only attempts and last_attempt change on a repeat")
	require.Contains(t, q.Reason, "FOREIGN KEY")
	require.Equal(t, "pull", q.Source)
}

// TestRetryQuarantinedEvents_LamportOrderAcrossPages: the retry reads the
// quarantine in Lamport order, limit rows at a time, so a node's create
// applies before its edits even when they sit on different pages; a raw
// event that cannot be decoded stays quarantined, with the attempt counted.
func TestRetryQuarantinedEvents_LamportOrderAcrossPages(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	events := offlineEvents(t)
	_, err := applyPullBatch(ctx, testIngest(nil), app.store, quarantineSourcePull,
		[]*model.SyncEvent{events[2], events[1]})
	require.NoError(t, err)
	require.Len(t, quarantined(t), 2, "precondition: both edits lack their node")
	require.NoError(t, app.store.WithTx(ctx, func(tx *sql.Tx) error {
		return sqlite.QuarantineEvent(ctx, tx, sqlite.QuarantinedEvent{EventID: "garbled", Source: "sweep",
			RawEvent: "not json", Reason: "r", FirstSeen: "t", LastAttempt: "t"})
	}))
	_, err = applyPullBatch(ctx, testIngest(nil), app.store, quarantineSourcePull, events[:1])
	require.NoError(t, err)

	got, err := retryQuarantinedEvents(ctx, testIngest(nil), app.store, 1)

	require.NoError(t, err)
	require.Equal(t, quarantineRetry{applied: 2, held: 1}, got)
	node, err := app.store.GetNode(ctx, "TEST-9")
	require.NoError(t, err)
	require.Equal(t, "second edit", node.Description, "the edits applied in Lamport order")
	q := quarantined(t)
	require.Len(t, q, 1)
	require.Equal(t, "r", q["garbled"].Reason, "a repeat never rewrites the reason")
	require.Equal(t, 2, q["garbled"].Attempts)
}

// mustApplyPullBatch applies events to the peer store through the cursor
// pass's ingest path and requires every one of them to apply.
func mustApplyPullBatch(t *testing.T, events []*model.SyncEvent) {
	t.Helper()
	held, err := applyPullBatch(context.Background(), testIngest(nil), app.store, quarantineSourcePull, events)
	require.NoError(t, err)
	require.Empty(t, held, "every event applies")
}

// TestSweepLateEvents_EventFailsApply_QuarantinedAndSweepCompletes: a
// recovered late event that fails its apply is quarantined with source
// "sweep" and unstaged in the same transaction; the other late events
// apply, and the sweep completes and records its hub time, where it used
// to stop the pull on every attempt.
func TestSweepLateEvents_EventFailsApply_QuarantinedAndSweepCompletes(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	events := offlineEvents(t)
	bad := *events[1]
	bad.Payload = json.RawMessage(`{"field_name":"not_a_field","new_value":"x"}`)
	hub := &fakeLateHub{events: []*model.SyncEvent{events[0], &bad, events[2]},
		hubNows: []time.Time{sweepHubT1}}
	var stderr bytes.Buffer

	got, err := sweepLateEvents(ctx, testIngest(&stderr), hub, app.store, 1)

	require.NoError(t, err)
	require.Equal(t, lateEventSweep{Recovered: 2, FullDiff: true}, got, "the quarantined event is not counted")
	requireQuarantinedAs(t, &bad, "sweep", "not_a_field")
	requireNotApplied(t, &bad)
	require.Empty(t, stagedIDs(t), "the quarantined event is unstaged with the others")
	require.Equal(t, "2026-09-24T10:00:00.123456Z", lastSweep(t), "the sweep completes")
	requireNoFullSweepProgress(t)
	node, err := app.store.GetNode(ctx, "TEST-9")
	require.NoError(t, err)
	require.Equal(t, "second edit", node.Description)
	require.Contains(t, stderr.String(), "quarantined event "+bad.EventID+" (sweep pass)")
}

// TestMissingLocalEventIDs_Quarantined_NotMissing: the sweep never stages a
// quarantined event again; the quarantine retries it.
func TestMissingLocalEventIDs_Quarantined_NotMissing(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	events := offlineEvents(t)
	held, err := applyPullBatch(ctx, testIngest(nil), app.store, quarantineSourcePull, events[1:2])
	require.NoError(t, err)
	require.Len(t, held, 1, "precondition: the edit without its node is quarantined")

	got, err := missingLocalEventIDs(ctx, app.store, eventIDs(events))

	require.NoError(t, err)
	require.Equal(t, []string{events[0].EventID, events[2].EventID}, got)
}

// TestApplyPullBatch_ContextEnds_NothingQuarantined: a pull whose context
// ends mid-batch fails the batch, which rolls back; the event it was on is
// not quarantined because of the cancellation.
func TestApplyPullBatch_ContextEnds_NothingQuarantined(t *testing.T) {
	initTestApp(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := offlineEvents(t)
	cancelAfterFirst := func(_ *sql.Tx, _ *model.SyncEvent) error {
		cancel()
		return nil
	}

	held, err := applyPullBatch(ctx, testIngest(nil), app.store, quarantineSourcePull, events, cancelAfterFirst)

	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, held)
	require.Empty(t, quarantined(t))
	for _, e := range events {
		requireNotApplied(t, e)
	}
}

// TestApplyPulledEvent_ContextEndedDuringApply_AbortsNotRejected: an apply
// whose context has ended is an abort (its savepoint cannot even open),
// never a rejection that would quarantine the event.
func TestApplyPulledEvent_ContextEndedDuringApply_AbortsNotRejected(t *testing.T) {
	initTestApp(t)
	ctx, cancel := context.WithCancel(context.Background())
	events := offlineEvents(t)
	err := app.store.WithTx(context.Background(), func(tx *sql.Tx) error {
		cancel()
		rejected, applyErr := applyPulledEvent(ctx, tx, events[0])
		require.Nil(t, rejected)
		return applyErr
	})
	require.ErrorIs(t, err, context.Canceled)
}

// TestApplyPullBatch_NilEvent_FailsBatch: a nil event can be neither
// applied nor quarantined (it has no id); the batch fails before any write
// instead of losing it.
func TestApplyPullBatch_NilEvent_FailsBatch(t *testing.T) {
	initTestApp(t)
	events := offlineEvents(t)
	_, err := applyPullBatch(context.Background(), testIngest(nil), app.store, quarantineSourcePull,
		[]*model.SyncEvent{events[0], nil})
	require.ErrorIs(t, err, model.ErrInvalidInput)
	require.ErrorContains(t, err, "event 1 is nil")
	require.Empty(t, quarantined(t))
	requireNotApplied(t, events[0])
}

// TestQuarantineReason_OneLineNoControlCharsCapped: a reason is stored and
// printed as one line without control characters (an event's own text can
// reach it), at most 500 runes.
func TestQuarantineReason_OneLineNoControlCharsCapped(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"plain", fmt.Errorf("apply x: not found"), "apply x: not found"},
		{"escape and newlines", fmt.Errorf("node \u001b[31mRED\u001b[0m\nline\ttab\r"), "node [31mRED[0m line tab"},
		{"capped", fmt.Errorf("%s", strings.Repeat("\u00e9", 600)), strings.Repeat("\u00e9", 500) + "..."},
		{"exactly the cap", fmt.Errorf("%s", strings.Repeat("a", 500)), strings.Repeat("a", 500)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, quarantineReason(tt.err))
		})
	}
}

// TestPrintQuarantineHeld_ReportsOnlyWhenHeld: the end of a pull reports
// the events still quarantined, and nothing when there are none.
func TestPrintQuarantineHeld_ReportsOnlyWhenHeld(t *testing.T) {
	for _, n := range []int{0, 1, 2} {
		t.Run(fmt.Sprintf("%d held", n), func(t *testing.T) {
			initTestApp(t)
			quarantineN(t, n)
			var stdout, stderr bytes.Buffer

			printQuarantineHeld(context.Background(), &stdout, &stderr, app.store)

			if n == 0 {
				require.Empty(t, stdout.String())
				return
			}
			require.Equal(t, fmt.Sprintf(
				"quarantine: %d pulled events held, not applied; retried on every pull (list them: mtix sync quarantine list)\n", n),
				stdout.String())
			require.Empty(t, stderr.String())
		})
	}
}

// TestNewPullIngest_ReadsMaxLamportJump: production wiring takes the bound
// from sync.max_lamport_jump, its default without a config, and this
// binary's version.
func TestNewPullIngest_ReadsMaxLamportJump(t *testing.T) {
	initTestApp(t)
	require.Equal(t, validator.DefaultMaxLamportJump, newPullIngest(io.Discard).maxJump)
	_, err := app.configSvc.Set("sync.max_lamport_jump", "77")
	require.NoError(t, err)

	in := newPullIngest(io.Discard)

	require.Equal(t, int64(77), in.maxJump)
	require.Equal(t, version, in.cliVersion)
	require.NotNil(t, in.now)
	app.configSvc = nil
	require.Equal(t, validator.DefaultMaxLamportJump, newPullIngest(io.Discard).maxJump)
}

// mustIdempotentApply applies e to the peer store directly through
// IdempotentApply, without the pull's checks.
func mustIdempotentApply(t *testing.T, e *model.SyncEvent) {
	t.Helper()
	require.NoError(t, app.store.WithTx(context.Background(), func(tx *sql.Tx) error {
		return sqlite.IdempotentApply(context.Background(), tx, e)
	}))
}
