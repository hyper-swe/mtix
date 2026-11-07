// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests for MTIX-112.17.16.16 round 5, rule G: a merge journals only field
// edits and transitions on a task the sync log holds, and the links between
// such tasks; a task it creates is written here and named unsent; the
// dependent of a blocker the merge resolves agrees with a peer.
package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

type exportDepRow = struct {
	FromID    string `json:"from_id"`
	ToID      string `json:"to_id"`
	DepType   string `json:"dep_type"`
	CreatedAt string `json:"created_at"`
}

func addFileDep(t *testing.T, data *sqlite.ExportData, from, to, typ string) {
	t.Helper()
	data.Dependencies = append(data.Dependencies, exportDepRow{from, to, typ, sameIDTime.Format(time.RFC3339)})
	require.NoError(t, sqlite.RecomputeExportChecksum(data))
}

func fileIdx(t *testing.T, data *sqlite.ExportData, id string) int {
	t.Helper()
	for i := range data.Nodes {
		if data.Nodes[i].ID == id {
			return i
		}
	}
	require.Fail(t, "task not in the file", id)
	return -1
}

// TestImportMerge_NewTask_IsWrittenNamedUnsentAndNeverJournaled: a task the
// file adds, with a status, a comment, a child and links, is written here,
// named unsent, and nothing about it or under it or linked to it is journaled.
func TestImportMerge_NewTask_IsWrittenNamedUnsentAndNeverJournaled(t *testing.T) {
	importer, peer := peerPair(t, 1)
	bob := newTestStore(t)
	createPlainTask(t, bob, "REC-2", 2, "", sameIDTime)
	data := exportOf(t, bob)
	n := &data.Nodes[fileIdx(t, data, "REC-2")]
	n.Status = string(model.StatusDone)
	n.Annotations = []model.Annotation{{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", Author: "bob", Text: "note", CreatedAt: sameIDTime}}
	addChildOf(t, data, "REC-2", "REC-2.1")
	data.Nodes = append(data.Nodes, exportOf(t, importer).Nodes...)
	data.NodeCount = len(data.Nodes)
	addFileDep(t, data, "REC-2", "REC-1", "blocks")

	res := mergeTheirs(t, importer, data)

	require.Zero(t, countJournal(t, importer, `sync_status = 'pending'`), "no event for a new task, its child or its link")
	require.ElementsMatch(t, []string{"REC-2", "REC-2.1"}, res.UnsentTasks)
	require.NotEmpty(t, titleOf(t, importer, "REC-2"), "the task is written here")
	require.Contains(t, fieldsNamed(res, "REC-1"), "status (blocked by a link that stays local)")
	replicate(t, importer, peer)
	require.Equal(t, "open", viewOf(t, peer, "REC-1").Status, "the peer never saw the unsent link")
}

// TestImportMerge_AuthorPushesLater_CreateAppliesOverTheMergedRowAndEveryReplicaAgrees:
// Bob creates a task and edits it before pushing; Alice merges his export;
// the order of the two pushes does not matter and every replica ends with
// Bob's values, with nothing held.
func TestImportMerge_AuthorPushesLater_CreateAppliesOverTheMergedRowAndEveryReplicaAgrees(t *testing.T) {
	for _, aliceFirst := range []bool{true, false} {
		name := "bob pushes first"
		if aliceFirst {
			name = "alice pushes first"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			alice, hub := peerPair(t, 1)
			bob := newTestStore(t)
			createPlainTask(t, bob, "REC-2", 2, "", sameIDTime)
			title := "Bob edited once"
			require.NoError(t, bob.UpdateNode(ctx, "REC-2", &store.NodeUpdate{Title: &title}))
			file := exportOf(t, bob)
			file.Nodes[fileIdx(t, file, "REC-2")].ContentHash = plainHash(title)
			require.NoError(t, sqlite.RecomputeExportChecksum(file))

			mergeTheirs(t, alice, file)
			if aliceFirst {
				replicate(t, alice, hub)
			}
			later := "Bob edited again"
			require.NoError(t, bob.UpdateNode(ctx, "REC-2", &store.NodeUpdate{Title: &later}))
			replicateToAll(t, bob, hub, alice) // Bob's push, then Alice's pull of it
			if !aliceFirst {
				replicate(t, alice, hub)
			}

			require.Equal(t, later, titleOf(t, hub, "REC-2"))
			require.Equal(t, viewOf(t, hub, "REC-2"), viewOf(t, alice, "REC-2"))
			require.Zero(t, countJournal(t, alice, `sync_status = 'pending'`), "nothing for Alice to push or hold")
		})
	}
}

// TestImportMerge_EditToAnUnsentTask_IsNotJournaled: a second merge that edits
// the task the first one created journals nothing and still names it.
func TestImportMerge_EditToAnUnsentTask_IsNotJournaled(t *testing.T) {
	importer, _ := peerPair(t, 1)
	bob := newTestStore(t)
	createPlainTask(t, bob, "REC-2", 2, "", sameIDTime)
	mergeTheirs(t, importer, exportOf(t, bob))
	title := "Edited by Bob"
	require.NoError(t, bob.UpdateNode(context.Background(), "REC-2", &store.NodeUpdate{Title: &title}))
	file := exportOf(t, bob)
	file.Nodes[fileIdx(t, file, "REC-2")].ContentHash = plainHash(title)
	require.NoError(t, sqlite.RecomputeExportChecksum(file))

	res := mergeTheirs(t, importer, file)

	require.Zero(t, countJournal(t, importer, `sync_status = 'pending' AND node_id = 'REC-2'`))
	require.Equal(t, []string{"REC-2"}, res.UnsentTasks)
	require.Equal(t, title, titleOf(t, importer, "REC-2"))
}

// TestImportMerge_LinkToADeletedTaskWithNoCreate_IsNamedAndItsBlockIsLocalOnly:
// a link whose end is deleted and was never created in the log is not sent;
// the block it causes on a live task is named.
func TestImportMerge_LinkToADeletedTaskWithNoCreate_IsNamedAndItsBlockIsLocalOnly(t *testing.T) {
	importer, peer := halfFilledPair(t)
	require.NoError(t, importer.DeleteNode(context.Background(), "REC-2", false, "alice"))
	_, err := importer.WriteDB().Exec(`DELETE FROM sync_events WHERE node_id = 'REC-2'`)
	require.NoError(t, err)
	data := exportOf(t, importer)
	addFileDep(t, data, "REC-2", "REC-1", "blocks")

	res := mergeTheirs(t, importer, data)

	require.Contains(t, res.UnsentTasks, "REC-2")
	require.Contains(t, fieldsNamed(res, "REC-1"), "status (blocked by a link that stays local)")
	replicate(t, importer, peer)
	require.Equal(t, "open", viewOf(t, peer, "REC-1").Status)
}

// TestImportMerge_BlockerResolvedByTheFile_DependentAgreesWithAPeer: the
// importer's status for a dependent after a merge is what a peer computes
// applying the journaled transition, for every state the file gives the
// blocker, and a repeat emits nothing.
func TestImportMerge_BlockerResolvedByTheFile_DependentAgreesWithAPeer(t *testing.T) {
	for _, tc := range []struct {
		name, before, fileStatus string
		fileDeleted              bool
		want                     string
	}{
		{"file resolves the blocker", "open", "done", false, "in_progress"},
		{"file cancels the blocker", "open", "cancelled", false, "in_progress"},
		{"file invalidates the blocker", "open", "invalidated", false, "in_progress"},
		{"file reopens a done blocker", "done", "open", false, "blocked"},
		{"file deletes the blocker", "open", "open", true, "blocked"},
		{"file leaves the blocker open", "open", "open", false, "blocked"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			importer, peer := inProgressPair(t)
			_, err := importer.WriteDB().Exec(`UPDATE nodes SET status = ? WHERE id = 'REC-2'`, tc.before)
			require.NoError(t, err)
			_, err = peer.WriteDB().Exec(`UPDATE nodes SET status = ? WHERE id = 'REC-2'`, tc.before)
			require.NoError(t, err)
			setup := exportOf(t, importer)
			addFileDep(t, setup, "REC-2", "REC-1", "blocks")
			mergeTheirs(t, importer, setup) // REC-1 is blocked here and on the peer
			replicate(t, importer, peer)
			require.Equal(t, "blocked", viewOf(t, importer, "REC-1").Status)
			file := exportOf(t, importer)
			file.Nodes[fileIdx(t, file, "REC-1")].Status = string(model.StatusInProgress)
			blocker := &file.Nodes[fileIdx(t, file, "REC-2")]
			blocker.Status = tc.fileStatus
			if tc.fileDeleted {
				blocker.DeletedAt, blocker.DeletedBy = "2026-01-02T00:00:00Z", "bob"
			}
			require.NoError(t, sqlite.RecomputeExportChecksum(file))

			mergeTheirs(t, importer, file)
			replicate(t, importer, peer)
			first := countJournal(t, importer, `1 = 1`)
			mergeTheirs(t, importer, file)

			require.Equal(t, tc.want, viewOf(t, importer, "REC-1").Status, "importer")
			require.Equal(t, viewOf(t, peer, "REC-1"), viewOf(t, importer, "REC-1"), "peer")
			// The deletion time is the file's here and the event's on the peer; it is
			// named local only (deleted_at), so the replicas agree that it is deleted.
			here, there := viewOf(t, importer, "REC-2"), viewOf(t, peer, "REC-2")
			require.Equal(t, there.DeletedAt != "", here.DeletedAt != "", "blocker deleted on both or neither")
			here.DeletedAt, there.DeletedAt = "", ""
			require.Equal(t, there, here, "blocker")
			require.Equal(t, first, countJournal(t, importer, `1 = 1`), "the repeat emits nothing")
		})
	}
}
