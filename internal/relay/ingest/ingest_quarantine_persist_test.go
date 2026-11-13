// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package ingest_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/relay/ingest"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/sync/clock"
	"github.com/stretchr/testify/require"
)

// collidingCreate publishes a create for PROJ-1 from the far peer after the
// local store already holds a different task under that id: the two peers took
// the same next task number (MTIX-268).
func collidingCreate(t *testing.T, r *peerRig) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	local := &model.Node{
		ID: "PROJ-1", Project: "PROJ", Depth: 0, Seq: 1, Title: "local",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1,
		NodeType: model.NodeTypeEpic, Creator: "local-agent",
		ContentHash: model.ComputeContentHash("local", "", "", "", nil),
		CreatedAt:   now, UpdatedAt: now,
	}
	require.NoError(t, r.store.CreateNode(ctx, local), "a local create the hub has not seen yet")
	remote := event(t, model.OpCreateNode, "PROJ-1", 2, &model.CreateNodePayload{Title: "remote same number"})
	remote.UID = clock.MustNewEventID()
	r.publish(t, fleetKey, marshalEvents(t, remote)...)
}

// TestRelayIngest_IDCollisionIsPersistedAndSurvivesReopen is the MTIX-268
// red-first test: a record skipped at ingest is a stored, listable row after
// the tick and after the store is reopened, not a line in one tick's output.
func TestRelayIngest_IDCollisionIsPersistedAndSurvivesReopen(t *testing.T) {
	r := newPeerRig(t)
	ctx := context.Background()
	collidingCreate(t, r)

	in, err := ingest.New(r.config())
	require.NoError(t, err)
	stats, err := in.IngestAll(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, stats.Quarantined)
	require.Equal(t, uint64(1), r.cursor(t).RS, "the cursor moved past the skipped record")

	check := func(st *sqlite.Store) {
		rows, err := st.ListRelayQuarantine(ctx)
		require.NoError(t, err)
		require.Len(t, rows, 1)
		row := rows[0]
		require.Equal(t, remotePeer, row.PeerID)
		require.Equal(t, uint64(1), row.RS)
		require.Equal(t, "PROJ-1", row.NodeID)
		require.Equal(t, string(model.OpCreateNode), row.OpType)
		require.Equal(t, sqlite.RelayQuarantineOpen, row.State)
		require.NotEmpty(t, row.Reason)
		require.Contains(t, string(row.Payload), "remote same number")
		require.Equal(t, 1, row.Attempts)
	}
	check(r.store)

	require.NoError(t, r.store.Close())
	reopened, err := sqlite.New(r.dbPath, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = reopened.Close() })
	check(reopened)
}

// TestRelayIngest_QuarantineWriteFailureFailsThePollAndKeepsTheCursor pins the
// order: a record is never skipped without a stored row, so a failed write
// fails the poll and the cursor stays before the record (MTIX-268).
func TestRelayIngest_QuarantineWriteFailureFailsThePollAndKeepsTheCursor(t *testing.T) {
	r := newPeerRig(t)
	good := event(t, model.OpCreateNode, "PROJ-1", 1, &model.CreateNodePayload{Title: "before"})
	bad := event(t, model.OpCreateNode, "PROJ-BAD", 2, &model.CreateNodePayload{Title: "garbage"})
	bad.AuthorID = "NOT A VALID AUTHOR"
	after := event(t, model.OpCreateNode, "PROJ-2", 3, &model.CreateNodePayload{Title: "after"})
	r.publish(t, fleetKey, marshalEvents(t, good, bad, after)...)

	boom := errors.New("quarantine table is unavailable")
	in, err := ingest.New(r.withFake(&fakeStore{errQuarantine: boom}))
	require.NoError(t, err)
	_, err = in.IngestAll(context.Background())
	require.ErrorIs(t, err, boom)
	require.Equal(t, uint64(1), r.cursor(t).RS, "cursor stays before the unrecorded record")
	_, err = r.store.GetNode(context.Background(), "PROJ-2")
	require.Error(t, err, "nothing past the unrecorded record was read")
}

// TestRelayIngest_UndecodablePayloadKeepsItsRawBytes: a payload that is not
// JSON is stored as the exact bytes received, with no node or op.
func TestRelayIngest_UndecodablePayloadKeepsItsRawBytes(t *testing.T) {
	r := newPeerRig(t)
	r.publish(t, fleetKey, []byte("not json \xff\xfe"))
	in, err := ingest.New(r.config())
	require.NoError(t, err)
	_, err = in.IngestAll(context.Background())
	require.NoError(t, err)
	rows, err := r.store.ListRelayQuarantine(context.Background())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, []byte("not json \xff\xfe"), rows[0].Payload)
	require.Empty(t, rows[0].NodeID)
}
