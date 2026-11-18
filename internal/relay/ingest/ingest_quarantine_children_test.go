// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package ingest_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/relay/ingest"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// ingestAll runs one poll and fails the test on a poll error.
func ingestAll(t *testing.T, r *peerRig) ingest.Stats {
	t.Helper()
	in, err := ingest.New(r.config())
	require.NoError(t, err)
	stats, err := in.IngestAll(context.Background())
	require.NoError(t, err)
	return stats
}

// publishAfterCollision runs the colliding create through one poll, lets the
// local task's own creation settle (it was pending when the number collided,
// which is why the far peer's create was skipped), and publishes the follow-up
// events from the far peer for a later poll. Once the local task is no longer
// pending, nothing else keeps a follow-up off it.
func publishAfterCollision(t *testing.T, r *peerRig, follow ...model.SyncEvent) {
	t.Helper()
	collidingCreate(t, r)
	stats := ingestAll(t, r)
	require.Equal(t, 1, stats.Quarantined, "the colliding create is skipped, as in MTIX-268")
	settleLocalCreate(t, r)
	if len(follow) > 0 {
		r.publish(t, fleetKey, marshalEvents(t, follow...)...)
	}
}

// settleLocalCreate marks the local task's creation as no longer pending.
func settleLocalCreate(t *testing.T, r *peerRig) {
	t.Helper()
	require.NoError(t, r.store.WithTx(context.Background(), func(tx *sqlTx) error {
		_, err := tx.ExecContext(context.Background(),
			`UPDATE sync_events SET sync_status = 'pushed' WHERE node_id = 'PROJ-1' AND op_type = 'create_node'`)
		return err
	}))
}

func relayRows(t *testing.T, st *sqlite.Store) map[string]sqlite.RelayQuarantineRow {
	t.Helper()
	rows, err := st.ListRelayQuarantine(context.Background())
	require.NoError(t, err)
	out := map[string]sqlite.RelayQuarantineRow{}
	for _, row := range rows {
		out[row.EventID] = row
	}
	return out
}

func localParentOf(t *testing.T, st *sqlite.Store, id string) (parent string, found bool) {
	t.Helper()
	_, err := st.GetNode(context.Background(), id)
	if errors.Is(err, model.ErrNotFound) {
		return "", false
	}
	require.NoError(t, err)
	n, err := st.GetNode(context.Background(), id)
	require.NoError(t, err)
	return n.ParentID, true
}

// TestRelayIngest_ChildOfSkippedParentIsQuarantinedNotAttachedLocally is the
// MTIX-273 red-first test: after two peers took the same task number, the far
// peer's later child of its own task must not attach under the local task that
// holds the number. It is stored in the relay quarantine, with a reason that
// says the parent was skipped, and it survives a store reopen.
func TestRelayIngest_ChildOfSkippedParentIsQuarantinedNotAttachedLocally(t *testing.T) {
	r := newPeerRig(t)
	child := event(t, model.OpCreateNode, "PROJ-1.1", 3, &model.CreateNodePayload{Title: "child of the far task", ParentID: "PROJ-1"})
	publishAfterCollision(t, r, child)

	stats := ingestAll(t, r)
	require.Equal(t, 1, stats.Quarantined, "the child")
	require.Equal(t, uint64(2), r.cursor(t).RS, "the cursor moved past both")

	_, found := localParentOf(t, r.store, "PROJ-1.1")
	require.False(t, found, "the child must not exist under the local task")
	local, err := r.store.GetNode(context.Background(), "PROJ-1")
	require.NoError(t, err)
	require.Equal(t, "local", local.Title)

	check := func(st *sqlite.Store) {
		row, ok := relayRows(t, st)[child.EventID]
		require.True(t, ok, "the child is listed by the relay quarantine")
		require.Equal(t, "PROJ-1.1", row.NodeID)
		require.Equal(t, sqlite.RelayQuarantineOpen, row.State)
		require.Contains(t, row.Reason, "parent")
		require.Contains(t, row.Reason, "skipped")
		require.Contains(t, row.Reason, "its parent task")
		require.NotContains(t, row.Reason, "took that number", "the skip cause is not known here, so it is not claimed")
		require.NotContains(t, row.Reason, "create_node", "user terms, never the internal op name")
		require.Contains(t, string(row.Payload), "child of the far task")
	}
	check(r.store)
	require.NoError(t, r.store.Close())
	reopened, err := sqlite.New(r.dbPath, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = reopened.Close() })
	check(reopened)
}

// A grandchild of a skipped child is held the same way: the chain is walked one
// parent at a time through the stored rows.
func TestRelayIngest_GrandchildOfSkippedChildIsQuarantined(t *testing.T) {
	r := newPeerRig(t)
	child := event(t, model.OpCreateNode, "PROJ-1.1", 3, &model.CreateNodePayload{Title: "child", ParentID: "PROJ-1"})
	grand := event(t, model.OpCreateNode, "PROJ-1.1.1", 4, &model.CreateNodePayload{Title: "grandchild", ParentID: "PROJ-1.1"})
	publishAfterCollision(t, r, child, grand)

	stats := ingestAll(t, r)
	require.Equal(t, 2, stats.Quarantined)
	_, found := localParentOf(t, r.store, "PROJ-1.1.1")
	require.False(t, found)
	_, ok := relayRows(t, r.store)[grand.EventID]
	require.True(t, ok)
}

// A create under a parent the far peer did not have skipped still applies.
func TestRelayIngest_ChildOfAppliedParentStillApplies(t *testing.T) {
	r := newPeerRig(t)
	publishAfterCollision(t, r,
		event(t, model.OpCreateNode, "PROJ-2", 3, &model.CreateNodePayload{Title: "fine"}),
		event(t, model.OpCreateNode, "PROJ-2.1", 4, &model.CreateNodePayload{Title: "fine child", ParentID: "PROJ-2"}))

	stats := ingestAll(t, r)
	require.Equal(t, 0, stats.Quarantined)
	require.Equal(t, 2, stats.Applied)
	parent, found := localParentOf(t, r.store, "PROJ-2.1")
	require.True(t, found)
	require.Equal(t, "PROJ-2", parent)
}

// Other operations the far peer aimed at its own skipped task are held too, so
// none of them lands on the local task that holds the number.
func TestRelayIngest_OperationsOnSkippedTaskAreQuarantined(t *testing.T) {
	other := event(t, model.OpCreateNode, "PROJ-2", 3, &model.CreateNodePayload{Title: "other"})
	newTitle := json.RawMessage(`"hijacked"`)
	tests := []struct {
		name string
		ev   model.SyncEvent
		role string // how the reason names the skipped task
	}{
		{"field update", event(t, model.OpUpdateField, "PROJ-1", 4, &model.UpdateFieldPayload{FieldName: "title", NewValue: newTitle}), "the task it changes"},
		{"status change", event(t, model.OpTransitionStatus, "PROJ-1", 4,
			&model.TransitionStatusPayload{From: model.StatusOpen, To: model.StatusInProgress}), "the task it changes"},
		{"comment", event(t, model.OpComment, "PROJ-1", 4, &model.CommentPayload{AuthorID: "remote", Body: "for the far task"}), "the task it changes"},
		{"dependency on it", event(t, model.OpLinkDep, "PROJ-2", 4, &model.LinkDepPayload{DependsOnNodeID: "PROJ-1", DepType: "blocks"}), "the task it depends on"},
		{"dependency removed from it", event(t, model.OpUnlinkDep, "PROJ-2", 4,
			&model.UnlinkDepPayload{DependsOnNodeID: "PROJ-1", DepType: "blocks"}), "the task it depends on"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newPeerRig(t)
			publishAfterCollision(t, r, other, tt.ev)
			stats := ingestAll(t, r)
			require.Equal(t, 1, stats.Quarantined, "this record; the other task applies")
			row, ok := relayRows(t, r.store)[tt.ev.EventID]
			require.True(t, ok, "listed by the relay quarantine")
			require.Contains(t, row.Reason, "skipped")
			require.Contains(t, row.Reason, tt.role, "the reason names the task as this record kind uses it")
			require.NotContains(t, row.Reason, "took that number", "the skip cause is not known here, so it is not claimed")
			require.Contains(t, row.Reason, "could change a different local task")
			local, err := r.store.GetNode(context.Background(), "PROJ-1")
			require.NoError(t, err)
			require.Equal(t, "local", local.Title)
			require.Equal(t, model.StatusOpen, local.Status)
			require.Empty(t, local.Annotations)
			deps, err := r.store.GetBlockers(context.Background(), "PROJ-2")
			require.NoError(t, err)
			require.Empty(t, deps)
		})
	}
}

// A resolved parent does not release its children on ingest: a child that
// arrives after the parent was recreated elsewhere stays in the quarantine, and
// is mapped only by resolving it explicitly (MTIX-268 resolve path).
func TestRelayIngest_ChildOfRecreatedParentStaysQuarantined(t *testing.T) {
	r := newPeerRig(t)
	collidingCreate(t, r)
	ingestAll(t, r)
	settleLocalCreate(t, r)
	parent := relayRows(t, r.store)
	require.Len(t, parent, 1)
	var key sqlite.RelayQuarantineKey
	for _, row := range parent {
		key = row.RelayQuarantineKey
	}
	require.NoError(t, r.store.WithTx(context.Background(), func(tx *sqlTx) error {
		ok, err := sqlite.ResolveRelayQuarantineTx(context.Background(), tx, key, sqlite.RelayResolution{
			Resolution: sqlite.RelayResolutionRecreate, Actor: "op", Reason: "test", RecreatedAs: "PROJ-9",
			At: time.Now().UTC(),
		})
		require.True(t, ok)
		return err
	}))

	child := event(t, model.OpCreateNode, "PROJ-1.1", 3, &model.CreateNodePayload{Title: "late child", ParentID: "PROJ-1"})
	r.publish(t, fleetKey, marshalEvents(t, child)...)
	stats := ingestAll(t, r)
	require.Equal(t, 1, stats.Quarantined)
	_, found := localParentOf(t, r.store, "PROJ-1.1")
	require.False(t, found, "not attached under the local task and not remapped under PROJ-9 either")
	_, found = localParentOf(t, r.store, "PROJ-9.1")
	require.False(t, found)
	row := relayRows(t, r.store)[child.EventID]
	require.Equal(t, sqlite.RelayQuarantineOpen, row.State)
}

// A failed lookup of the skipped-parent rows is a store failure, not a verdict:
// the poll fails and the cursor stays before the record.
func TestRelayIngest_SkippedParentLookupFailureKeepsTheCursor(t *testing.T) {
	r := newPeerRig(t)
	child := event(t, model.OpCreateNode, "PROJ-2.1", 2, &model.CreateNodePayload{Title: "child", ParentID: "PROJ-2"})
	r.publish(t, fleetKey, marshalEvents(t, event(t, model.OpCreateNode, "PROJ-2", 1, &model.CreateNodePayload{Title: "p"}), child)...)
	boom := errors.New("quarantine lookup is unavailable")
	in, err := ingest.New(r.withFake(&fakeStore{errHeldLookup: boom}))
	require.NoError(t, err)
	_, err = in.IngestAll(context.Background())
	require.ErrorIs(t, err, boom)
	require.Equal(t, uint64(0), r.cursor(t).RS, "nothing was applied past the failed lookup")
	require.False(t, strings.Contains(err.Error(), "create_node"))
}
