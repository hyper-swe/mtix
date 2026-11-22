// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// Tests of pull against a number held by a local task that has NO create in
// the log (MTIX-112.17.16.39): a task written before sync, or whose creation
// was never logged. INVARIANT: a hub create on that number keeps BOTH tasks:
// the local one moves, with its subtree and every reference to it, to the next
// free number and keeps its uid; the hub's task takes the number; nothing is
// refused or lost.

const hubUID = "01a0f66f-7db8-7ef0-ace2-bbc04556f439"

// unloggedFamily makes MTIX-1 with child MTIX-1.1 and a sibling MTIX-2 that
// depends on MTIX-1.1, a pending edit of MTIX-1, then strips every create
// event so the tasks look written without a log entry.
func unloggedFamily(t *testing.T) (*sqlSetup, map[string]string) {
	t.Helper()
	s, raw := mutationTestStore(t)
	ctx := context.Background()
	mustCreateNode(t, s, "MTIX-1", "")
	mustCreateNode(t, s, "MTIX-1.1", "MTIX-1")
	mustCreateNode(t, s, "MTIX-2", "")
	require.NoError(t, s.AddDependency(ctx, &model.Dependency{FromID: "MTIX-2", ToID: "MTIX-1.1", DepType: model.DepTypeBlocks}))
	setDescription(t, s, "MTIX-1", "edited locally")
	_, err := raw.Exec(`DELETE FROM sync_events WHERE op_type = 'create_node'`)
	require.NoError(t, err)
	uids := map[string]string{}
	for _, id := range []string{"MTIX-1", "MTIX-1.1", "MTIX-2"} {
		uids[id] = nodeRow(t, raw, id)["uid"]
	}
	return &sqlSetup{s: s, raw: raw}, uids
}

func rowByUID(t *testing.T, raw *sql.DB, uid string) map[string]string {
	t.Helper()
	var id, parent, title string
	require.NoError(t, raw.QueryRow(
		`SELECT id, COALESCE(parent_id, ''), title FROM nodes WHERE uid = ?`, uid).Scan(&id, &parent, &title))
	return map[string]string{"id": id, "parent": parent, "title": title}
}

func TestApply_HubCreateOnNumberOfUnloggedLocalTask_KeepsBothTasks(t *testing.T) {
	fx, uids := unloggedFamily(t)
	create := foreignCreate(t, "MTIX-1", hubUID, "hub's task")

	require.NoError(t, applyEventsInTx(fx.s, []*model.SyncEvent{create}))

	hub := rowByUID(t, fx.raw, hubUID)
	require.Equal(t, "MTIX-1", hub["id"], "the hub's task takes the number")
	require.Equal(t, "hub's task", hub["title"])

	local := rowByUID(t, fx.raw, uids["MTIX-1"])
	require.Equal(t, "MTIX-3", local["id"], "the local task takes the next free number, keeping its uid")
	child := rowByUID(t, fx.raw, uids["MTIX-1.1"])
	require.Equal(t, "MTIX-3.1", child["id"])
	require.Equal(t, "MTIX-3", child["parent"])
	require.Equal(t, "MTIX-2", rowByUID(t, fx.raw, uids["MTIX-2"])["id"], "an unrelated task stays")

	require.Equal(t, 1, countRows(t, fx.raw,
		`SELECT COUNT(*) FROM dependencies WHERE from_id = 'MTIX-2' AND to_id = 'MTIX-3.1'`), "the dependency follows")
	require.Zero(t, countRows(t, fx.raw, `SELECT COUNT(*) FROM dependencies WHERE to_id = 'MTIX-1.1'`))
	require.Equal(t, 1, countRows(t, fx.raw,
		`SELECT COUNT(*) FROM sync_events WHERE uid = ? AND node_id = 'MTIX-3' AND sync_status = 'pending'`,
		uids["MTIX-1"]), "the pending edit is re-addressed")
	require.Zero(t, countRows(t, fx.raw, `SELECT COUNT(*) FROM sync_events WHERE node_id = 'MTIX-1' AND uid <> ?`, hubUID))
	require.Zero(t, countRows(t, fx.raw, `SELECT COUNT(*) FROM sync_quarantine`), "nothing is quarantined")
}

func TestApply_HubCreateOnNumberOfUnloggedLocalTask_ReplayIsIdempotent(t *testing.T) {
	fx, uids := unloggedFamily(t)
	create := foreignCreate(t, "MTIX-1", hubUID, "hub's task")
	require.NoError(t, applyEventsInTx(fx.s, []*model.SyncEvent{create}))
	require.NoError(t, applyEventsInTx(fx.s, []*model.SyncEvent{create}))

	require.Equal(t, "MTIX-3", rowByUID(t, fx.raw, uids["MTIX-1"])["id"])
	require.Equal(t, "MTIX-1", rowByUID(t, fx.raw, hubUID)["id"])
	require.Equal(t, 4, countRows(t, fx.raw, `SELECT COUNT(*) FROM nodes`))
}

// A holder whose creation the hub has (a create is in the log) keeps the
// first-writer-wins no-op; only the no-create holder moves.
func TestApply_HubCreateOnNumberOfLoggedLocalTask_StillFirstWriterWins(t *testing.T) {
	s, raw := replicaWithPendingNode(t)
	markEventsPushed(t, raw)
	require.NoError(t, applyEventsInTx(s, []*model.SyncEvent{foreignCreate(t, "MTIX-1", hubUID, "hub's task")}))
	require.Equal(t, "Test MTIX-1", nodeRow(t, raw, "MTIX-1")["title"])
	require.Equal(t, 1, countRows(t, raw, `SELECT COUNT(*) FROM nodes`))
}

type sqlSetup struct {
	s   *sqlite.Store
	raw *sql.DB
}
