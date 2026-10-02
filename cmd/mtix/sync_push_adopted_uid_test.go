// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/relay/bootstrap"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/sync/pushlock"
)

// TestPushLoop_UIDAdoptedByMerge_EveryEventOfTheTaskCarriesTheAdoptedUID is
// the MTIX-95.31.16 probe on a fake hub: a task's creation, an edit and a
// comment are queued, a merge import then gives the task the file's uid,
// and the task is edited, given a child and a link before the push. Every
// event the push sends for the task, those queued before the adoption
// included, carries the adopted uid, and the child's events carry the
// child's own: no task's events reach the hub under two uids.
func TestPushLoop_UIDAdoptedByMerge_EveryEventOfTheTaskCarriesTheAdoptedUID(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("adopting", "", "", 3, "", "", "", "", ""))
	require.NoError(t, runCreate("other", "", "", 3, "", "", "", "", ""))
	require.NoError(t, runUpdate("TEST-1", "edit before", "", "", "", 0, "", ""))
	require.NoError(t, runComment("TEST-1", "comment before", ""))
	adopted := adoptFileUID(t, "TEST-1")
	require.NoError(t, runUpdate("TEST-1", "edit after", "", "", "", 0, "", ""))
	require.NoError(t, runCreate("child", "TEST-1", "", 3, "", "", "", "", ""))

	hub := newFakePushHub()
	require.NoError(t, pushWith(t, hub))
	require.Zero(t, pushHoldCount(t), "nothing is held")

	seen := 0
	for _, e := range hub.events {
		switch e.NodeID {
		case "TEST-1":
			seen++
			require.Equal(t, adopted, e.UID, "%s of TEST-1 carries the adopted uid", e.OpType)
		case "TEST-1.1", "TEST-2":
			require.NotEqual(t, adopted, e.UID, "%s of %s keeps its own uid", e.OpType, e.NodeID)
		}
	}
	require.GreaterOrEqual(t, seen, 4, "creation, two edits and a comment reached the hub")
	requireSent(t, hub, "TEST-1", model.OpCreateNode)
	requireSent(t, hub, "TEST-1.1", model.OpCreateNode)
}

// TestPushLoop_UIDAdoptedMidPush_ImportWaitsForThePush is the MTIX-95.31.16
// review S1 probe: a merge import that would adopt a uid for a task with
// pending events runs while a push holds the push lock, between the hub
// committing the batch and the local mark (the markPushedFailKey seam). The
// import must not change the task's events then: it is refused, so the mark
// finds the creation it sent, nothing is sent twice, and once the push ends
// the same import adopts the uid.
func TestPushLoop_UIDAdoptedMidPush_ImportWaitsForThePush(t *testing.T) {
	initTestApp(t)
	require.NotEmpty(t, app.mtixDir)
	require.NoError(t, runCreate("adopting", "", "", 3, "", "", "", "", ""))
	create := eventIDFor(t, "TEST-1", model.OpCreateNode)

	lock, err := pushlock.Acquire(app.mtixDir)
	require.NoError(t, err)
	var adoptErr error
	tried := false
	midPush := context.WithValue(context.Background(), markPushedFailKey{}, func([]string) error {
		if !tried { // once: a later batch of the same push must not adopt again
			tried = true
			_, adoptErr = tryAdoptFileUID(t, "TEST-1")
		}
		return nil
	})
	hub := newFakePushHub()
	var stderr bytes.Buffer
	_, err = pushLoop(midPush, &stderr, hub, app.store)
	require.NoError(t, err)
	require.NoError(t, lock.Release())

	require.Error(t, adoptErr, "the import is refused while a push runs")
	require.ErrorContains(t, adoptErr, "push is running")
	require.Equal(t, "pushed", syncStatusOf(t, create), "the mark found the creation the push sent")
	require.NoError(t, pushWith(t, hub))
	sent := 0
	for _, call := range hub.calls {
		for _, id := range call {
			if id == create {
				sent++
			}
		}
	}
	require.Equal(t, 1, sent, "the creation is sent once")

	adopted, err := tryAdoptFileUID(t, "TEST-1")
	require.NoError(t, err, "the import goes through once the push has ended")
	node, err := app.store.GetNode(context.Background(), "TEST-1")
	require.NoError(t, err)
	require.Equal(t, adopted, node.UID)
}

// TestPushLoop_HeldCreationAdoptedThenRenumbered_LaterChangeStillHeld is the
// MTIX-95.31.16 scope item: a held creation whose task adopts the file's uid
// and is then renumbered on this machine (TEST-1 to TEST-5) still holds a
// change made under the new number. The creation's held copy follows the
// adopted uid, so the task is recognized by uid and no longer only by the
// number the creation names.
func TestPushLoop_HeldCreationAdoptedThenRenumbered_LaterChangeStillHeld(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("held", "", "", 3, "", overWireCap(), "", "", ""))
	require.NoError(t, runCreate("other", "", "", 3, "", "", "", "", ""))
	hub := newFakePushHub()
	require.NoError(t, pushWith(t, hub))

	adopted := adoptFileUID(t, "TEST-1")
	root := eventIDFor(t, "TEST-1", model.OpCreateNode)
	require.Equal(t, adopted, root, "the held creation carries the adopted uid as its event id")
	require.NoError(t, app.store.RenumberSubtree(context.Background(), "TEST-1", 5))
	require.NoError(t, runUpdate("TEST-5", "edit under the new number", "", "", "", 0, "", ""))
	require.NoError(t, runUpdate("TEST-2", "unrelated edit", "", "", "", 0, "", ""))
	require.NoError(t, pushWith(t, hub))

	requireHeldDependent(t, hub, "TEST-5", model.OpUpdateField, root)
	require.False(t, hub.sent(root), "the creation stays held")
	requireSent(t, hub, "TEST-2", model.OpUpdateField)
}

// TestPushLoop_UIDAdoptedByAutoImport_EveryEventCarriesTheAdoptedUID is the
// MTIX-95.31.16 probe for the automatic import after a git pull (replace
// mode): it adopts the file's uid for a same-title task and carries it onto
// the task's pending events, so the push after it is not stalled by a
// renumber it cannot resolve and sends the task's creation and later
// changes under one uid.
func TestPushLoop_UIDAdoptedByAutoImport_EveryEventCarriesTheAdoptedUID(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("adopting", "", "", 3, "", "", "", "", ""))
	require.NoError(t, runCreate("other", "", "", 3, "", "", "", "", ""))
	require.NoError(t, runUpdate("TEST-1", "edit before", "", "", "", 0, "", ""))
	adopted, err := autoImportFileUID(t, "TEST-1")
	require.NoError(t, err)
	node, err := app.store.GetNode(context.Background(), "TEST-1")
	require.NoError(t, err)
	require.Equal(t, adopted, node.UID, "the automatic import adopted the file's uid")
	require.NoError(t, runUpdate("TEST-1", "edit after", "", "", "", 0, "", ""))
	require.NoError(t, runCreate("child", "TEST-1", "", 3, "", "", "", "", ""))

	hub := newFakePushHub()
	require.NoError(t, pushWith(t, hub))
	seen := 0
	for _, e := range hub.events {
		if e.NodeID == "TEST-1" {
			seen++
			require.Equal(t, adopted, e.UID, "%s of TEST-1 carries the adopted uid", e.OpType)
		}
	}
	require.GreaterOrEqual(t, seen, 3, "creation and both edits reached the hub")
	require.Zero(t, pushHoldCount(t))
}

// TestImport_PushRunning_NonAdoptingMergeGoesThrough checks the push lock
// is taken only for a merge that adopts a uid: a merge that adopts none
// writes while a push runs.
func TestImport_PushRunning_NonAdoptingMergeGoesThrough(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("shared", "", "", 3, "", "", "", "", ""))
	data, err := app.store.Export(context.Background(), "", "")
	require.NoError(t, err)
	data.Nodes[0].Title = "retitled by a teammate"
	require.NoError(t, sqlite.RecomputeExportChecksum(data))
	board, err := json.Marshal(data)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "board.json")
	require.NoError(t, os.WriteFile(path, board, 0o600))

	lock, err := pushlock.Acquire(app.mtixDir)
	require.NoError(t, err)
	defer func() { _ = lock.Release() }()
	require.NoError(t, runImport(path, importFlags{mode: "merge"}), "no uid is adopted: no lock is needed")
}

// TestImport_AdoptingMerge_LeavesThePushLockFree checks a merge that adopts
// a uid releases the push lock when it ends, so a push can run after it.
func TestImport_AdoptingMerge_LeavesThePushLockFree(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("adopting", "", "", 3, "", "", "", "", ""))
	adoptFileUID(t, "TEST-1")
	lock, err := pushlock.Acquire(app.mtixDir)
	require.NoError(t, err, "the lock is free after the import")
	require.NoError(t, lock.Release())
}

// otherUIDExporter is a bootstrap Exporter whose copy of the local board
// holds TEST-1 under another uid, as a peer that assigned it later would.
type otherUIDExporter struct{ uid string }

func (e otherUIDExporter) Export(ctx context.Context, project, version string) (*sqlite.ExportData, error) {
	data, err := app.store.Export(ctx, project, version)
	if err != nil {
		return nil, err
	}
	data.Nodes[0].UID = e.uid
	return data, sqlite.RecomputeExportChecksum(data)
}

// TestRelayClone_AdoptingUIDDuringPush_IsRefused checks a relay clone whose
// snapshot gives a task the snapshot's uid takes the push lock like the
// merge import does: while a push runs the clone is refused and writes
// nothing, and once the push has ended the uid is adopted.
func TestRelayClone_AdoptingUIDDuringPush_IsRefused(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("adopting", "", "", 3, "", "", "", "", ""))
	local, err := app.store.GetNode(context.Background(), "TEST-1")
	require.NoError(t, err)
	uid, err := model.NewBackfillUID()
	require.NoError(t, err)
	dir := t.TempDir()
	_, err = bootstrap.ExportSnapshot(context.Background(), bootstrap.ExportRequest{
		Store: otherUIDExporter{uid: uid}, RelayDir: dir, ExportedBy: "0123456789abcdef",
		CreatedAt: time.Now().UTC(), Positions: map[string]uint64{},
	})
	require.NoError(t, err)

	lock, err := pushlock.Acquire(app.mtixDir)
	require.NoError(t, err)
	err = runRelayCloneImport(context.Background(), &cobra.Command{Use: "clone"}, dir, importFlags{})
	require.ErrorContains(t, err, "push is running")
	require.NoError(t, lock.Release())
	node, err := app.store.GetNode(context.Background(), "TEST-1")
	require.NoError(t, err)
	require.Equal(t, local.UID, node.UID, "nothing was written")
}
