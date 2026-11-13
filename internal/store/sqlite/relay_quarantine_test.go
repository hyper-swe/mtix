// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

func relayQuarantineRec(rs uint64, seen string) sqlite.RelayQuarantineRecord {
	return sqlite.RelayQuarantineRecord{
		RelayQuarantineKey: sqlite.RelayQuarantineKey{PeerID: "fedcba9876543210", PubEpoch: 1, RS: rs},
		SegmentNo:          1, EventID: "e-1", NodeID: "PROJ-1", OpType: "create_node", Lamport: 7,
		Reason: "first reason", Payload: []byte(`{"x":1}`), SeenAt: seen,
	}
}

// TestRecordRelayQuarantine_SameRecordTwice_CountsAttemptsOnly pins the upsert:
// one row per (peer, epoch, rs); a repeat sighting bumps attempts and
// last_seen and leaves first_seen, the first reason and the payload alone.
func TestRecordRelayQuarantine_SameRecordTwice_CountsAttemptsOnly(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.RecordRelayQuarantine(ctx, relayQuarantineRec(5, "2026-11-13T01:00:00Z")))
	again := relayQuarantineRec(5, "2026-11-13T02:00:00Z")
	again.Reason, again.Payload = "different reason", []byte("other")
	require.NoError(t, s.RecordRelayQuarantine(ctx, again))

	rows, err := s.ListRelayQuarantine(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, 2, rows[0].Attempts)
	require.Equal(t, "2026-11-13T01:00:00Z", rows[0].FirstSeen)
	require.Equal(t, "2026-11-13T02:00:00Z", rows[0].LastSeen)
	require.Equal(t, "first reason", rows[0].Reason)
	require.Equal(t, []byte(`{"x":1}`), rows[0].Payload)
}

// TestRecordRelayQuarantine_DifferentPositions_AreSeparateRows: the key is the
// stream position, so another rs, epoch or peer is another row, listed in order.
func TestRecordRelayQuarantine_DifferentPositions_AreSeparateRows(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	other := relayQuarantineRec(3, "2026-11-13T01:00:00Z")
	other.PubEpoch = 2
	for _, r := range []sqlite.RelayQuarantineRecord{
		relayQuarantineRec(9, "2026-11-13T01:00:00Z"), other, relayQuarantineRec(4, "2026-11-13T01:00:00Z"),
	} {
		require.NoError(t, s.RecordRelayQuarantine(ctx, r))
	}
	rows, err := s.ListRelayQuarantine(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 3)
	require.Equal(t, []uint64{4, 9, 3}, []uint64{rows[0].RS, rows[1].RS, rows[2].RS})
	c, err := s.CountRelayQuarantine(ctx)
	require.NoError(t, err)
	require.Equal(t, sqlite.RelayQuarantineCounts{Open: 3}, c)
}

// TestResolveRelayQuarantineTx_OpenRow_SettlesOnce settles an open row, keeps
// it as audit, refuses a second settle and rolls back with its transaction.
func TestResolveRelayQuarantineTx_OpenRow_SettlesOnce(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	rec := relayQuarantineRec(5, "2026-11-13T01:00:00Z")
	require.NoError(t, s.RecordRelayQuarantine(ctx, rec))
	res := sqlite.RelayResolution{Resolution: sqlite.RelayResolutionAccepted, Actor: "lane", Reason: "reviewed",
		At: time.Date(2026, 11, 13, 3, 0, 0, 0, time.UTC)}

	var settled bool
	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		var e error
		settled, e = sqlite.ResolveRelayQuarantineTx(ctx, tx, rec.RelayQuarantineKey, res)
		if e != nil {
			return e
		}
		return sql.ErrTxDone // force a rollback
	})
	require.Error(t, err)
	require.True(t, settled)
	c, err := s.CountRelayQuarantine(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, c.Open, "a rolled-back resolve leaves the row open")

	require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error {
		settled, err = sqlite.ResolveRelayQuarantineTx(ctx, tx, rec.RelayQuarantineKey, res)
		return err
	}))
	require.True(t, settled)
	require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error {
		settled, err = sqlite.ResolveRelayQuarantineTx(ctx, tx, rec.RelayQuarantineKey, res)
		return err
	}))
	require.False(t, settled, "already resolved")

	rows, err := s.ListRelayQuarantine(ctx)
	require.NoError(t, err)
	require.Equal(t, sqlite.RelayQuarantineResolved, rows[0].State)
	require.Equal(t, "lane", rows[0].ResolvedBy)
	require.Equal(t, "2026-11-13T03:00:00Z", rows[0].ResolvedAt)
	c, _ = s.CountRelayQuarantine(ctx)
	require.Equal(t, sqlite.RelayQuarantineCounts{Resolved: 1}, c)

	// A later sighting of a resolved record does not reopen it.
	require.NoError(t, s.RecordRelayQuarantine(ctx, relayQuarantineRec(5, "2026-11-13T04:00:00Z")))
	rows, _ = s.ListRelayQuarantine(ctx)
	require.Equal(t, sqlite.RelayQuarantineResolved, rows[0].State)
}

// TestRelayQuarantineSchema_StoreWithoutTable_GainsItAndKeepsSchemaVersion: a
// store created before MTIX-268 has no relay_quarantine table; opening it adds
// the table and leaves schema_version alone, like the other local-only tables.
func TestRelayQuarantineSchema_StoreWithoutTable_GainsItAndKeepsSchemaVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	s, err := sqlite.New(path, slog.Default())
	require.NoError(t, err)
	require.NoError(t, s.Close())
	db := newTestDB(t, path)
	var before string
	require.NoError(t, db.QueryRow("SELECT value FROM meta WHERE key='schema_version'").Scan(&before))
	_, err = db.Exec("DROP TABLE relay_quarantine")
	require.NoError(t, err)

	s, err = sqlite.New(path, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	require.NoError(t, s.RecordRelayQuarantine(context.Background(), relayQuarantineRec(1, "2026-11-13T01:00:00Z")))
	var after string
	require.NoError(t, db.QueryRow("SELECT value FROM meta WHERE key='schema_version'").Scan(&after))
	require.Equal(t, before, after)
}

// TestCreateNodeAllocated_InTxHookError_RollsTheCreateBack: the hook runs inside
// the create transaction, so its error leaves no node and no sync event, and a
// hook that succeeds sees the allocated id (MTIX-268).
func TestCreateNodeAllocated_InTxHookError_RollsTheCreateBack(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	mk := func() *model.Node {
		return &model.Node{Project: "PROJ", Title: "hooked", Status: model.StatusOpen, Priority: model.PriorityMedium,
			Weight: 1, NodeType: model.NodeTypeEpic, Creator: "a", CreatedAt: now, UpdatedAt: now,
			ContentHash: model.ComputeContentHash("hooked", "", "", "", nil)}
	}
	boom := errors.New("hook refused")
	err := s.CreateNodeAllocated(ctx, mk(), store.CreateNodeOptions{
		InTx: func(context.Context, *sql.Tx, *model.Node) error { return boom }})
	require.ErrorIs(t, err, boom)
	var nodes, events int
	require.NoError(t, s.QueryRow(ctx, `SELECT COUNT(*) FROM nodes`).Scan(&nodes))
	require.NoError(t, s.QueryRow(ctx, `SELECT COUNT(*) FROM sync_events`).Scan(&events))
	require.Zero(t, nodes)
	require.Zero(t, events)

	var seen string
	n := mk()
	require.NoError(t, s.CreateNodeAllocated(ctx, n, store.CreateNodeOptions{
		InTx: func(_ context.Context, _ *sql.Tx, node *model.Node) error { seen = node.ID; return nil }}))
	require.Equal(t, n.ID, seen)
	require.NotEmpty(t, seen)
}

// TestRelayEventRecreatedTx_ReadsCommittedStateInsideTheTransaction: the lookup
// that refuses a repeated recreate runs in the create's own transaction, so it
// sees a recreate committed after the caller listed the rows, ignores the row
// being resolved, and ignores accepted rows and rows of other events.
func TestRelayEventRecreatedTx_ReadsCommittedStateInsideTheTransaction(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	first, second, other := relayQuarantineRec(4, "2026-11-13T01:00:00Z"), relayQuarantineRec(9, "2026-11-13T01:00:00Z"), relayQuarantineRec(5, "2026-11-13T01:00:00Z")
	other.EventID = "e-other"
	for _, r := range []sqlite.RelayQuarantineRecord{first, second, other} {
		require.NoError(t, s.RecordRelayQuarantine(ctx, r))
	}
	look := func(key sqlite.RelayQuarantineKey, event string) *sqlite.RelayRecreated {
		var got *sqlite.RelayRecreated
		require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error {
			var err error
			got, err = sqlite.RelayEventRecreatedTx(ctx, tx, key, event)
			return err
		}))
		return got
	}
	require.Nil(t, look(second.RelayQuarantineKey, "e-1"), "nothing recreated yet")

	settle := func(r sqlite.RelayQuarantineRecord, resolution, as string) {
		require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error {
			_, err := sqlite.ResolveRelayQuarantineTx(ctx, tx, r.RelayQuarantineKey,
				sqlite.RelayResolution{Resolution: resolution, Actor: "a", Reason: "r", RecreatedAs: as, At: time.Now()})
			return err
		}))
	}
	settle(other, sqlite.RelayResolutionRecreate, "PROJ-8")
	require.Nil(t, look(second.RelayQuarantineKey, "e-1"), "another event's recreate does not count")
	settle(first, sqlite.RelayResolutionAccepted, "")
	require.Nil(t, look(second.RelayQuarantineKey, "e-1"), "an accept does not count")

	third := relayQuarantineRec(12, "2026-11-13T01:00:00Z")
	require.NoError(t, s.RecordRelayQuarantine(ctx, third))
	settle(third, sqlite.RelayResolutionRecreate, "PROJ-9")
	got := look(second.RelayQuarantineKey, "e-1")
	require.NotNil(t, got)
	require.Equal(t, sqlite.RelayRecreated{PeerID: third.PeerID, RS: 12, RecreatedAs: "PROJ-9"}, *got)
	require.Nil(t, look(third.RelayQuarantineKey, "e-1"), "the row itself is excluded")
	require.Nil(t, look(second.RelayQuarantineKey, ""), "an empty event id never matches")
}
