// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests that kill the F31 survivors of round 3 (MTIX-112.17.16.16, round 4).
package sqlite_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// renumberCollision returns a store holding REC-1 (Local task) and the file of
// a teammate who holds a different task under REC-1: a merge renumbers the
// local one to REC-2.
func renumberCollision(t *testing.T, synced bool) (*sqlite.Store, *sqlite.ExportData) {
	t.Helper()
	local, teammate := newTestStore(t), newTestStore(t)
	createSameIDTask(t, local, "REC-1", "", 1, taskUID(t), "Local task")
	if synced {
		markJournalPushed(t, local)
	}
	createSameIDTask(t, teammate, "REC-1", "", 1, taskUID(t), "Teammate task")
	return local, exportOf(t, teammate)
}

// TestImportMerge_LocalRenumberOnAnUnsyncedStore_IsNotNamed: a store that has
// not synced has no hub copy to disagree with, so a renumber is not named (M14).
func TestImportMerge_LocalRenumberOnAnUnsyncedStore_IsNotNamed(t *testing.T) {
	local, file := renumberCollision(t, false)

	_, res, err := local.ImportReconcile(context.Background(), file,
		sqlite.ImportReconcileOptions{Mode: sqlite.ImportModeMerge, Confirm: true})

	require.NoError(t, err)
	require.Empty(t, fieldsNamed(res, "REC-2"))
	require.Empty(t, res.JournalLocalOnly)
}

// TestImportMerge_LocalRenumberOnARelayClone_IsNamed: a relay snapshot clone
// syncs with no journal row; the caller says so (AssumeSynced) and the
// renumber is named like any other (M15).
func TestImportMerge_LocalRenumberOnARelayClone_IsNamed(t *testing.T) {
	local, file := renumberCollision(t, false)

	_, res, err := local.ImportReconcile(context.Background(), file,
		sqlite.ImportReconcileOptions{Mode: sqlite.ImportModeMerge, Confirm: true, AssumeSynced: true})

	require.NoError(t, err)
	require.Contains(t, fieldsNamed(res, "REC-2"), "id (was REC-1)")
}

// TestImportMerge_DoneTaskWithFullProgress_ProgressIsDerivedNotNamed: a peer
// sets progress to 1.0 for a done task, so the file's 1.0 agrees (M17).
func TestImportMerge_DoneTaskWithFullProgress_ProgressIsDerivedNotNamed(t *testing.T) {
	importer, _ := peerPair(t, 1)
	markJournalPushed(t, importer)
	data := exportOf(t, importer)
	data.Nodes[0].Status, data.Nodes[0].ClosedAt, data.Nodes[0].Progress = "done", "2026-01-02T00:00:00Z", 1
	require.NoError(t, sqlite.RecomputeExportChecksum(data))

	res := mergeTheirs(t, importer, data)

	require.NotContains(t, fieldsNamed(res, "REC-1"), "progress")
}

// TestImportMerge_NewLeafProgress_NamedOnlyWhenItIsNotZero: a peer starts a new
// leaf at 0, so a file's other value is named and 0 is not (M17).
func TestImportMerge_NewLeafProgress_NamedOnlyWhenItIsNotZero(t *testing.T) {
	for _, tt := range []struct {
		name     string
		progress float64
		named    bool
	}{{"zero", 0, false}, {"half", 0.5, true}} {
		t.Run(tt.name, func(t *testing.T) {
			importer, _ := peerPair(t, 1)
			markJournalPushed(t, importer)
			data := exportOf(t, importer)
			addChildOf(t, data, "REC-1", "REC-1.1")
			for i := range data.Nodes {
				if data.Nodes[i].ID == "REC-1.1" {
					data.Nodes[i].Progress = tt.progress
				}
			}
			require.NoError(t, sqlite.RecomputeExportChecksum(data))

			res := mergeTheirs(t, importer, data)

			if tt.named {
				require.Contains(t, fieldsNamed(res, "REC-1.1"), "progress")
			} else {
				require.NotContains(t, fieldsNamed(res, "REC-1.1"), "progress")
			}
		})
	}
}

// TestImportMerge_PreviousStatusOnAnUnblockedTask_IsNamed: no op carries the
// previous status of a task the merge does not block (M18).
func TestImportMerge_PreviousStatusOnAnUnblockedTask_IsNamed(t *testing.T) {
	importer, _ := peerPair(t, 1)
	markJournalPushed(t, importer)
	data := exportOf(t, importer)
	data.Nodes[0].PreviousStatus = "in_progress"
	data.Nodes[0].Title = "Retitled"
	data.Nodes[0].ContentHash = plainHash("Retitled")
	require.NoError(t, sqlite.RecomputeExportChecksum(data))

	res := mergeTheirs(t, importer, data)

	require.Contains(t, fieldsNamed(res, "REC-1"), "previous_status")
}

// TestImportMerge_BlankTimesInTheFile_AreNotNamed: a file without a time does
// not set one, so it differs from nothing (M19). The file must carry the
// created and updated times, so the closed time is the one left blank.
func TestImportMerge_BlankTimesInTheFile_AreNotNamed(t *testing.T) {
	importer, _ := peerPair(t, 1)
	markJournalPushed(t, importer)
	_, err := importer.WriteDB().Exec(`UPDATE nodes SET closed_at = '2026-01-02T00:00:00Z' WHERE id = 'REC-1'`)
	require.NoError(t, err)
	data := exportOf(t, importer)
	data.Nodes[0].Title = "Retitled"
	data.Nodes[0].ContentHash = plainHash("Retitled")
	data.Nodes[0].ClosedAt = ""
	require.NoError(t, sqlite.RecomputeExportChecksum(data))

	res := mergeTheirs(t, importer, data)

	require.NotContains(t, fieldsNamed(res, "REC-1"), "closed_at")
}

// TestLocalOnlyImports_AClaimSettlesTheStatusAndTheAssignee: a claim event
// carries both fields, so a record of both settles with it (M20).
func TestLocalOnlyImports_AClaimSettlesTheStatusAndTheAssignee(t *testing.T) {
	s := syncedStoreWithTask(t)
	require.NoError(t, s.RecordLocalOnlyImports(context.Background(),
		[]sqlite.LocalOnlyImport{{ID: "REC-1", Fields: []string{"assignee", "status"}}}))

	applyHub(t, s, hubEvent(t, "REC-1", model.OpClaim, &model.ClaimPayload{AgentID: "bob"}, 5))

	live, _ := liveOnly(t, s)
	require.Empty(t, live)
}

// TestImportMerge_NoJournalDryRun_IsNotRefusedByTheCap: a merge that journals
// nothing plans no events in the dry run either, so the cap, already reached,
// does not refuse it (M25).
func TestImportMerge_NoJournalDryRun_IsNotRefusedByTheCap(t *testing.T) {
	s := syncedStoreWithTask(t)
	retitleStore(t, s, "REC-1", "Pending edit")
	_, err := s.WriteDB().Exec(`UPDATE meta SET value = '1' WHERE key = 'sync.max_queue_size'`)
	require.NoError(t, err)
	data := exportOf(t, s)
	data.Nodes[0].Title = "Imported"
	data.Nodes[0].ContentHash = plainHash("Imported")
	require.NoError(t, sqlite.RecomputeExportChecksum(data))

	_, _, err = s.ImportReconcile(context.Background(), data, sqlite.ImportReconcileOptions{
		Mode: sqlite.ImportModeMerge, NoJournal: true, BeforeWrite: func() error { return nil }})

	require.NoError(t, err)
	require.Equal(t, "Imported", titleOf(t, s, "REC-1"))
}

// TestImportMerge_NewAnnotationWithAddressee_NamesTheAddressee: no comment
// event carries the addressee, so a new annotation that has one is named.
func TestImportMerge_NewAnnotationWithAddressee_NamesTheAddressee(t *testing.T) {
	s := syncedStoreWithTask(t)
	data := withAnnotation(t, s, model.Annotation{ID: survivorAnnID, Author: "bob", Text: "note",
		Addressee: "alice", CreatedAt: sameIDTime})

	res := mergeTheirs(t, s, data)

	require.Contains(t, fieldsNamed(res, "REC-1"), "annotations.addressee")
	require.Equal(t, 1, countJournal(t, s, `op_type = 'comment' AND sync_status = 'pending'`))
}

// TestImportMerge_AgentStateOnAChangedTask_IsJournaledAndNotNamed: an
// update_field carries agent_state for a task the log holds, so the peer
// agrees and the Local only line does not name it (M29).
func TestImportMerge_AgentStateOnAChangedTask_IsJournaledAndNotNamed(t *testing.T) {
	importer, _ := peerPair(t, 1)
	data := exportOf(t, importer)
	data.Nodes[0].AgentState = "working"
	require.NoError(t, sqlite.RecomputeExportChecksum(data))

	res := mergeTheirs(t, importer, data)

	require.NotContains(t, fieldsNamed(res, "REC-1"), "agent_state")
	require.Equal(t, 1, countJournal(t, importer,
		`op_type = 'update_field' AND sync_status = 'pending' AND payload LIKE '%agent_state%'`))
}

// TestImportMerge_HeldDependencyOnAnOpenTask_IsNotReblocked: a task that was
// not blocked before the merge stays as the file left it, though a blocks
// dependency on it exists; only a task blocked before is held blocked (M28).
func TestImportMerge_HeldDependencyOnAnOpenTask_IsNotReblocked(t *testing.T) {
	importer, peer := peerPair(t, 2)
	for _, s := range []*sqlite.Store{importer, peer} {
		_, err := s.WriteDB().Exec(`INSERT INTO dependencies (from_id, to_id, dep_type, created_at)
			VALUES ('REC-2', 'REC-1', 'blocks', '2026-01-01T00:00:00Z')`)
		require.NoError(t, err)
	}
	data := exportOf(t, importer)
	data.Nodes[0].Title = "Retitled"
	data.Nodes[0].ContentHash = plainHash("Retitled")
	require.NoError(t, sqlite.RecomputeExportChecksum(data))

	mergeTheirs(t, importer, data)
	replicate(t, importer, peer)

	require.Equal(t, "open", viewOf(t, importer, "REC-1").Status)
	require.Equal(t, viewOf(t, importer, "REC-1"), viewOf(t, peer, "REC-1"))
}

// TestImportMerge_RelaySnapshotClone_KeepsTheUIDWithNoJournal: a relay
// snapshot clone has no create row and its merge journals nothing (NoJournal),
// yet peers know its tasks by their uid: AssumeSynced keeps the uid (ruling 3).
func TestImportMerge_RelaySnapshotClone_KeepsTheUIDWithNoJournal(t *testing.T) {
	clone, teammate := newTestStore(t), newTestStore(t)
	mine := createSameIDTask(t, clone, "REC-1", "", 1, backfilledUID(t), "Shared task")
	createSameIDTask(t, teammate, "REC-1", "", 1, backfilledUID(t), "Shared task")

	_, _, err := clone.ImportReconcile(context.Background(), exportOf(t, teammate), sqlite.ImportReconcileOptions{
		Mode: sqlite.ImportModeMerge, AssumeSynced: true, NoJournal: true})

	require.NoError(t, err)
	var uid string
	require.NoError(t, clone.WriteDB().QueryRow(`SELECT uid FROM nodes WHERE id = 'REC-1'`).Scan(&uid))
	require.Equal(t, mine, uid)
}

// TestBackfillDryRun_NonEmptyJournal_CountsOnlyTheTasksTheLogLacks: the dry run
// reports what the real backfill then sends, not the whole board.
func TestBackfillDryRun_NonEmptyJournal_CountsOnlyTheTasksTheLogLacks(t *testing.T) {
	importer, _ := halfFilledPair(t)
	data := exportOf(t, importer)
	addChildOf(t, data, "REC-2", "REC-2.1")
	mergeTheirs(t, importer, data)

	dry, err := importer.BackfillDryRun(context.Background())
	require.NoError(t, err)
	sent, err := importer.Backfill(context.Background(), false)
	require.NoError(t, err)

	require.Equal(t, 2, sent.NodeCount, "REC-2 and REC-2.1")
	require.Equal(t, sent.NodeCount, dry.NodeCount)
	require.Equal(t, sent.CreateEvents, dry.CreateEvents)
}
