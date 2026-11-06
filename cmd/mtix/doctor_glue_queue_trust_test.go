// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/attach"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/testutil"
)

// ownedLaneRow is a queue row as a registration in the store at root writes it:
// this store's board key and path, and the lane the agent registered from.
func ownedLaneRow(t *testing.T, root, agent, harness, lane string) sqlite.AttachPending {
	t.Helper()
	row := laneRow(t, agent, harness, lane)
	row.BoardKey, row.Path = storeKeyAt(t, root), root
	return row
}

func storeKeyAt(t *testing.T, root string) string {
	t.Helper()
	id, err := sqlite.ReadStoreInstanceIDFile(t.Context(), filepath.Join(root, ".mtix", "data", "mtix.db"))
	require.NoError(t, err)
	return id
}

// Rows are untrusted: only a row whose board key and path are this store's and
// whose lane lies inside the store is trusted for a printed command; the rest
// are counted as rejected and never reach a command.
func TestAcceptQueuedRows_ForeignOwnerKindOrLane_AreRejectedByCount(t *testing.T) {
	const key, root = "K-own", "/work/store"
	mk := func(kind, k, path, lane string) sqlite.AttachPending {
		raw, err := json.Marshal(map[string]string{"agent_id": "a", "harness": "pi", "lane_path": lane})
		require.NoError(t, err)
		return sqlite.AttachPending{Kind: kind, BoardKey: k, Path: path, Payload: string(raw)}
	}
	tests := []struct {
		name string
		row  sqlite.AttachPending
		ok   bool
	}{
		{"own lane row", mk("lane", key, root, root+"/lane"), true},
		{"own list row at the root", mk("list", key, root, root), true},
		{"other board key", mk("lane", "K-other", root, root+"/lane"), false},
		{"other board path", mk("lane", key, "/work/victim", root+"/lane"), false},
		{"lane in another board", mk("lane", key, root, "/work/victim"), false},
		{"lane in a sibling with the same prefix", mk("lane", key, root, root+"-evil"), false},
		{"lane climbing out", mk("lane", key, root, root+"/../victim"), false},
		{"unknown kind", mk("exec", key, root, root+"/lane"), false},
		{"oversized payload", mk("lane", key, root, root+"/lane+"+strings.Repeat("l", 4100)), false},
		{"oversized payload that is valid json", sqlite.AttachPending{Kind: "lane", BoardKey: key, Path: root, Payload: mk("lane", key, root, root+"/lane").Payload + strings.Repeat(" ", 4097)}, false},
		{"lane that is not a clean path", mk("lane", key, root, root+"/a/../lane"), false},
		{"lane that is not absolute", mk("lane", key, root, "lane"), false},
		{"agent id that is not plain", withAgent(t, mk("lane", key, root, root+"/lane"), "x; rm -f /"), false},
		{"row with no board key", mk("lane", "", root, root+"/lane"), false},
		{"payload that is not json", sqlite.AttachPending{Kind: "lane", BoardKey: key, Path: root, Payload: "nope"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			valid, rejected := acceptQueuedRows([]sqlite.AttachPending{tt.row}, key, root)
			if tt.ok {
				assert.Len(t, valid, 1)
				assert.Zero(t, rejected)
				return
			}
			assert.Empty(t, valid)
			assert.Equal(t, 1, rejected)
		})
	}
	valid, rejected := acceptQueuedRows([]sqlite.AttachPending{mk("lane", "", root, root+"/lane")}, "", root)
	assert.Empty(t, valid, "a store with no routing key owns no rows")
	assert.Equal(t, 1, rejected)
}

// The forged row from the review: an unlisted board whose queue names a listed
// victim board's path as the lane. The doctor row reports it by count and
// builds no command from it.
func TestDoctorGlueStrandedQueue_ForgedRowNamingAListedBoard_IsRejectedAndPrintsNoCommand(t *testing.T) {
	saveAndResetApp(t)
	st := testutil.OperatorState(t)
	victim := listedBoardCheckout(t, st)
	hostile := t.TempDir()
	initStoreAt(t, hostile)
	forged := laneRow(t, "evil", "pi", victim)
	forged.BoardKey, forged.Path = "evil-key", victim
	enqueueRow(t, hostile, forged)
	row := strandedRowFor(t, hostile)

	assert.NotContains(t, row.Fix, victim)
	assert.NotContains(t, row.Fix, "evil")
	assert.NotContains(t, row.Fix, "cd ")
	assert.Contains(t, row.Detail, "1 queued row(s) rejected")
}

func TestDoctorGlueStrandedQueue_ValidAndForgedRows_PrintOnlyTheValidCommand(t *testing.T) {
	saveAndResetApp(t)
	st := testutil.OperatorState(t)
	victim := listedBoardCheckout(t, st)
	hostile := t.TempDir()
	initStoreAt(t, hostile)
	lane := filepath.Join(hostile, "lane")
	require.NoError(t, os.MkdirAll(lane, 0o755))
	enqueueRow(t, hostile, ownedLaneRow(t, hostile, "good", "pi", lane))
	forged := ownedLaneRow(t, hostile, "evil", "pi", victim) // this store's key and path, a lane in another board
	enqueueRow(t, hostile, forged)
	row := strandedRowFor(t, hostile)

	assert.Contains(t, row.Fix, "cd '"+lane+"' && mtix agent register 'good' --harness 'pi'")
	assert.NotContains(t, row.Fix, victim)
	assert.NotContains(t, row.Fix, "evil")
	assert.Contains(t, row.Detail, "1 queued row(s) rejected")
	assert.Contains(t, row.Detail, "1 attach queue row(s)")
}

// strandedRowFor runs the production doctor deps in an unlisted board and
// returns its stranded-queue row.
func strandedRowFor(t *testing.T, root string) (row struct{ Fix, Detail string }) {
	t.Helper()
	t.Chdir(root)
	store, err := sqlite.New(filepath.Join(root, ".mtix", "data"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	app.store, app.mtixDir = store, filepath.Join(root, ".mtix")
	deps, err := productionAttachDoctorDeps(context.Background())
	require.NoError(t, err)
	got, ok := attachDoctorRows(t, deps, false)["glue.stranded-queue"]
	require.True(t, ok, "an unlisted board with queued rows is reported")
	return struct{ Fix, Detail string }{got.Fix, got.Detail}
}

// An unlisted board whose rows all fail the owner test has nothing to
// register: the row says so and prints the generic text, not a command.
func TestStrandedRegisterFix_NoTrustedRows_IsTheGenericText(t *testing.T) {
	assert.Contains(t, strandedRegisterFix(nil), "mtix agent register <agent-id> --harness <harness>")
}

// A board listed with an instance id whose store was recreated (a new instance
// id, as after rebuilding the store from an export) is declined; doctor says
// why and gives the recovery that exists.
func TestDoctorGlue_ListedBoardWithRecreatedStore_ShowsIdentityRowAndRecovery(t *testing.T) {
	saveAndResetApp(t)
	st := testutil.OperatorState(t)
	root := listedBoardCheckout(t, st)
	t.Chdir(root)
	deps, err := productionAttachDoctorDeps(context.Background())
	require.NoError(t, err)
	_, ok := attachDoctorRows(t, deps, false)["glue.store-identity"]
	require.False(t, ok, "a matching store needs no identity row")

	data := filepath.Join(root, ".mtix", "data")
	require.NoError(t, os.Remove(filepath.Join(data, "mtix.db")))
	for _, side := range []string{"mtix.db-wal", "mtix.db-shm"} {
		require.NoError(t, removeIfPresent(filepath.Join(data, side)))
	}
	initStoreAt(t, root)
	require.Equal(t, attach.ReasonIdentityMismatch, productionGluePredicate(context.Background()).Reason)

	got, ok := attachDoctorRows(t, deps, false)["glue.store-identity"]
	require.True(t, ok, "a declined listed board with a changed identity has a row")
	assert.Equal(t, service.DoctorWarn, got.Status)
	assert.Contains(t, got.Detail, "different store identity")
	assert.Contains(t, got.Fix, "mtix.db")
}

func removeIfPresent(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// withAgent returns row with its payload's agent id replaced.
func withAgent(t *testing.T, row sqlite.AttachPending, agent string) sqlite.AttachPending {
	t.Helper()
	var q map[string]string
	require.NoError(t, json.Unmarshal([]byte(row.Payload), &q))
	q["agent_id"] = agent
	raw, err := json.Marshal(q)
	require.NoError(t, err)
	row.Payload = string(raw)
	return row
}
