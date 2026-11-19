// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// A link_dep whose end is missing (MTIX-112.17.16.38): the hub holds the
// edge but not the create of one task it joins (deleted from the log, or
// pushed to a hub that lacks it). Pull quarantines it; clone must treat it
// the same way: quarantined, named by sync doctor, the clone completes.

// linkDepMissingEnds names which creates the hub log lacks, for the table.
var linkDepMissingEnds = []struct {
	name string
	keep func(from, target *model.SyncEvent) []*model.SyncEvent
}{
	{"target missing", func(from, _ *model.SyncEvent) []*model.SyncEvent { return []*model.SyncEvent{from} }},
	{"source missing", func(_, target *model.SyncEvent) []*model.SyncEvent { return []*model.SyncEvent{target} }},
	{"both missing", func(_, _ *model.SyncEvent) []*model.SyncEvent { return nil }},
}

func TestCheckThenClone_LinkDepWithMissingEnd_QuarantinedNamedAndCloneCompletes(t *testing.T) {
	for _, tt := range linkDepMissingEnds {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			from, target, link := linkDepEvents(t)
			rest := tt.keep(from, target)
			all := append(append([]*model.SyncEvent{}, rest...), link)
			var stderr bytes.Buffer
			hub := &fakeLateHub{pullEvents: all}

			pulled, _, stage, err := checkThenClone(context.Background(), &stderr, hub, transport.PullCursor{}, 10)

			require.NoError(t, err, stage)
			require.Equal(t, len(rest), pulled, "every event but the link_dep applies")
			requireNotApplied(t, link)
			q := quarantined(t)[link.EventID]
			require.Equal(t, quarantineSourcePull, q.Source)
			require.Contains(t, q.Reason, sqlite.CloneOrphanReason, "the row records the clone origin")
			require.Contains(t, stderr.String(), format.EventID(link.EventID), "the clone names the link_dep")
			ok, detail := checkQuarantinedEvents(context.Background(), app.store)
			require.False(t, ok, "sync doctor reports it")
			require.Contains(t, detail, "(1 of them clone orphans)", "doctor names the one clone orphan")
		})
	}
}

func TestApplyPullBatch_LinkDepWithMissingEnd_QuarantinedSameAsClone(t *testing.T) {
	for _, tt := range linkDepMissingEnds {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			from, target, link := linkDepEvents(t)
			batch := append(append([]*model.SyncEvent{}, tt.keep(from, target)...), link)

			_, err := applyPullBatch(context.Background(), testIngest(nil), app.store, quarantineSourcePull, batch)

			require.NoError(t, err)
			requireNotApplied(t, link)
			requireQuarantinedAs(t, link, quarantineSourcePull, "FOREIGN KEY")
		})
	}
}

// quarantineProbeHub records the most quarantined events it sees, on each
// page request, so a test can prove an event was quarantined DURING the
// clone and not merely applied in order.
type quarantineProbeHub struct {
	*fakeLateHub
	t    *testing.T
	seen int
}

func (h *quarantineProbeHub) PullEvents(ctx context.Context, after transport.PullCursor, limit int) ([]*model.SyncEvent, bool, error) {
	if n := len(quarantined(h.t)); n > h.seen {
		h.seen = n
	}
	return h.fakeLateHub.PullEvents(ctx, after, limit)
}

// stampLinkDepClocks gives the three events explicit Lamport clocks so the
// hub serves them in that order: the link_dep comes between the two creates.
func stampLinkDepClocks(from, target, link *model.SyncEvent, fromAt, linkAt, targetAt int64) {
	from.LamportClock, link.LamportClock, target.LamportClock = fromAt, linkAt, targetAt
}

func TestCloneLoop_LinkDepWhoseEndComesLater_QuarantinedThenAppliedOnce(t *testing.T) {
	tests := []struct {
		name   string
		clocks [3]int64 // Lamport of the source create, the link_dep, the target create
	}{
		{"target arrives later", [3]int64{1, 2, 3}},
		{"source arrives later", [3]int64{3, 2, 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			from, target, link := linkDepEvents(t)
			stampLinkDepClocks(from, target, link, tt.clocks[0], tt.clocks[1], tt.clocks[2])
			hub := &quarantineProbeHub{t: t, fakeLateHub: &fakeLateHub{
				pullEvents: []*model.SyncEvent{from, link, target}}}

			pulled, _, err := cloneLoop(context.Background(), &bytes.Buffer{}, hub, app.store, transport.PullCursor{}, 1)

			require.NoError(t, err)
			require.Equal(t, 1, hub.seen, "the link_dep was quarantined while the clone ran")
			require.Equal(t, 3, pulled)
			requireDependencyApplied(t, link)
		})
	}
}

func TestCloneThenPull_LinkDepWhoseEndArrivesAfterTheClone_AppliedByRetry(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	from, target, link := linkDepEvents(t)
	stampLinkDepClocks(from, target, link, 1, 2, 3)
	hub := &fakeLateHub{pullEvents: []*model.SyncEvent{from, link}}

	_, _, err := cloneLoop(ctx, &bytes.Buffer{}, hub, app.store, transport.PullCursor{}, 10)

	require.NoError(t, err)
	requireNotApplied(t, link)
	requireQuarantinedAs(t, link, quarantineSourcePull, sqlite.CloneOrphanReason)
	_, err = applyPullBatch(ctx, testIngest(nil), app.store, quarantineSourcePull, []*model.SyncEvent{target})
	require.NoError(t, err)
	t.Setenv(transport.EnvDSN, "")
	var stdout, stderr bytes.Buffer

	err = runSyncPull(ctx, &stdout, &stderr, nil, transport.Options{}, 100)

	require.ErrorContains(t, err, "mtix sync dsn:", "the hub is never reached here")
	requireDependencyApplied(t, link)
}

func TestIsOrphanFailure_LinkDep_OnlyAMissingEndIsAnOrphan(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	from, target, link := linkDepEvents(t)
	require.NoError(t, applyBatch(ctx, testIngest(nil), app.store, []*model.SyncEvent{from}))
	fault := errors.New("insert failed")
	garbled := *link
	garbled.Payload = []byte(`{"depends_on_node_id":5}`)
	tests := []struct {
		name  string
		e     *model.SyncEvent
		ready bool // the target exists too
		want  bool
	}{
		{"target absent", link, false, true},
		{"both ends present, other fault", link, true, false},
		{"undecodable payload", &garbled, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.ready {
				require.NoError(t, applyBatch(ctx, testIngest(nil), app.store, []*model.SyncEvent{target}))
			}
			require.NoError(t, app.store.WithTx(ctx, func(tx *sql.Tx) error {
				got, err := isOrphanFailure(ctx, tx, tt.e, fault)
				require.NoError(t, err)
				require.Equal(t, tt.want, got)
				return nil
			}))
		})
	}
	t.Run("source resolved by uid, event node id absent", func(t *testing.T) {
		requireLinkDepSourceVerdict(t, false, false, false)
	})
	t.Run("source uid held only by a soft-deleted row, event node id absent", func(t *testing.T) {
		requireLinkDepSourceVerdict(t, true, false, true)
	})
	t.Run("event without a uid names the source by node id", func(t *testing.T) {
		requireLinkDepSourceVerdict(t, false, true, false)
	})
	t.Run("failed end lookup is an error", func(t *testing.T) {
		cancelled, cancel := context.WithCancel(ctx)
		require.NoError(t, app.store.WithTx(ctx, func(tx *sql.Tx) error {
			cancel()
			got, err := isOrphanFailure(cancelled, tx, link, fault)
			require.Error(t, err)
			require.False(t, got)
			return nil
		}))
	})
}

func TestIsOrphanFailure_LinkDep_RenumberedSourceIsCountedOnce(t *testing.T) {
	tests := []struct {
		name         string
		targetExists bool
		want         bool
	}{
		{"target absent, source number held by another task", false, true},
		{"both ends present under the renumbered source", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			ctx := context.Background()
			_, target, link := linkDepEvents(t)
			// TEST-1 is now another task; the edge's source moved to TEST-5
			// and is named by its uid, the number in the event being stale.
			stale := remoteCreateAt(t, "TEST-1", 1)
			moved := remoteCreateAt(t, "TEST-5", 2)
			batch := []*model.SyncEvent{stale, moved}
			if tt.targetExists {
				batch = append(batch, target)
			}
			require.NoError(t, applyBatch(ctx, testIngest(nil), app.store, batch))
			edge := *link
			edge.NodeID, edge.UID = "TEST-1", moved.UID
			require.NoError(t, app.store.WithTx(ctx, func(tx *sql.Tx) error {
				got, err := isOrphanFailure(ctx, tx, &edge, errors.New("insert failed"))
				require.NoError(t, err)
				require.Equal(t, tt.want, got)
				return nil
			}))
		})
	}
}

// requireLinkDepSourceVerdict pins how the end check resolves the edge's
// source: the live holder of the event uid, else the event node id. The
// target exists. The edge's node id TEST-9 is held by no task. With uid
// set, the source task lives at TEST-5 and is soft-deleted when deleted is
// true; with noUID, the event names TEST-5 by number alone.
func requireLinkDepSourceVerdict(t *testing.T, deleted, noUID, want bool) {
	t.Helper()
	initTestApp(t)
	ctx := context.Background()
	_, target, link := linkDepEvents(t)
	moved := remoteCreateAt(t, "TEST-5", 1)
	require.NoError(t, applyBatch(ctx, testIngest(nil), app.store, []*model.SyncEvent{moved, target}))
	if deleted {
		// Soft-delete the source, as a delete event would.
		require.NoError(t, app.store.WithTx(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx,
				`UPDATE nodes SET deleted_at = ? WHERE id = ?`, "2026-06-01T00:00:00Z", "TEST-5")
			return err
		}))
	}
	edge := *link
	edge.NodeID, edge.UID = "TEST-9", moved.UID
	if noUID {
		edge.NodeID, edge.UID = "TEST-5", ""
	}
	require.NoError(t, app.store.WithTx(ctx, func(tx *sql.Tx) error {
		got, err := isOrphanFailure(ctx, tx, &edge, errors.New("insert failed"))
		require.NoError(t, err)
		require.Equal(t, want, got)
		return nil
	}))
}
