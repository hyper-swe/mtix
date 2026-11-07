// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests that kill the F31 survivors of round 4 and the S2/S3 findings of
// round 4 (MTIX-112.17.16.16, round 5).
package sqlite_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// TestImportMerge_LinkWithOneHeldEnd_IsNotAppliedWhicheverEnd (E7): a link
// with an end the log holds is named, not applied or journaled, whichever
// end is the held one.
func TestImportMerge_LinkWithOneHeldEnd_IsNotAppliedWhicheverEnd(t *testing.T) {
	for _, tc := range []struct{ name, from, to string }{{"unsent from", "REC-2", "REC-1"}, {"unsent to", "REC-1", "REC-2"}} {
		t.Run(tc.name, func(t *testing.T) {
			importer, _ := halfFilledPair(t)
			data := exportOf(t, importer)
			addFileDep(t, data, tc.from, tc.to, "related")

			res := mergeTheirs(t, importer, data)

			require.Zero(t, countJournal(t, importer, `op_type = 'link_dep' AND sync_status = 'pending'`))
			require.Equal(t, []string{"mtix dep add " + tc.from + " " + tc.to + " --type related"}, res.SkippedDeps)
			require.Zero(t, res.DepsImported)
		})
	}
}

// TestImportMerge_LinkBetweenTwoHeldTasks_IsNamedNotJournaledOrApplied: the
// counterpart of the test above for two held ends (round 6: no merge changes a
// dependency on a task the log holds).
func TestImportMerge_LinkBetweenTwoHeldTasks_IsNamedNotJournaledOrApplied(t *testing.T) {
	importer, peer := peerPair(t, 2)
	data := exportOf(t, importer)
	addFileDep(t, data, "REC-2", "REC-1", "related")

	res := mergeTheirs(t, importer, data)

	require.Zero(t, countJournal(t, importer, `op_type = 'link_dep' AND sync_status = 'pending'`))
	require.Equal(t, []string{"mtix dep add REC-2 REC-1 --type related"}, res.SkippedDeps)
	require.Empty(t, res.UnsentTasks)
	replicate(t, importer, peer)
}

// TestImportMerge_NewTaskCreatedDeleted_IsNamedUnsentAndNotJournaled (E10).
func TestImportMerge_NewTaskCreatedDeleted_IsNamedUnsentAndNotJournaled(t *testing.T) {
	importer, _ := peerPair(t, 1)
	bob := newTestStore(t)
	createPlainTask(t, bob, "REC-2", 2, "", sameIDTime)
	require.NoError(t, bob.DeleteNode(context.Background(), "REC-2", false, "bob"))

	res := mergeTheirs(t, importer, exportOf(t, bob))

	require.Equal(t, []string{"REC-2"}, res.UnsentTasks)
	require.Zero(t, countJournal(t, importer, `sync_status = 'pending'`))
}

// TestImportMerge_FileDeletesAHeldTask_JournalsTheDelete (E1): a delete by
// this merge is still carried; one made before the merge takes no event.
func TestImportMerge_FileDeletesAHeldTask_JournalsTheDelete(t *testing.T) {
	importer, peer := peerPair(t, 1)
	data := exportOf(t, importer)
	data.Nodes[0].DeletedAt, data.Nodes[0].DeletedBy = "2026-01-02T00:00:00Z", "bob"
	require.NoError(t, sqlite.RecomputeExportChecksum(data))

	res := mergeTheirs(t, importer, data)
	replicate(t, importer, peer)

	require.Empty(t, res.UnsentTasks)
	require.NotEmpty(t, viewOf(t, peer, "REC-1").DeletedAt)
}

// TestImportMerge_DeletedTask_IsNotNamedUnsentAndTakesNoWorkflowChoice (N2,
// F52): a deleted task that stays deleted is named as skipped only, and a file
// that differs from it needs no --prefer.
func TestImportMerge_DeletedTask_IsNotNamedUnsentAndTakesNoWorkflowChoice(t *testing.T) {
	importer, _ := peerPair(t, 1)
	require.NoError(t, importer.DeleteNode(context.Background(), "REC-1", false, "alice"))
	data := exportOf(t, importer)
	data.Nodes[0].Status, data.Nodes[0].DeletedAt, data.Nodes[0].DeletedBy = string(model.StatusDone), "", ""
	require.NoError(t, sqlite.RecomputeExportChecksum(data))

	_, res, err := importer.ImportReconcile(context.Background(), data, sqlite.ImportReconcileOptions{Mode: sqlite.ImportModeMerge})

	require.NoError(t, err, "no workflow conflict is raised for a task that stays deleted")
	require.Empty(t, res.WorkflowResolved)
	require.Empty(t, res.UnsentTasks)
	require.Equal(t, []string{"REC-1"}, res.SkippedDeleted)
}

// TestImportMerge_LegacyCreateRowWithoutUID_HoldsTheTaskByNumber (E5): a
// create row with no uid, as an old store wrote, holds the task by its number.
func TestImportMerge_LegacyCreateRowWithoutUID_HoldsTheTaskByNumber(t *testing.T) {
	importer, _ := peerPair(t, 1)
	_, err := importer.WriteDB().Exec(`UPDATE sync_events SET uid = NULL WHERE node_id = 'REC-1'`)
	require.NoError(t, err)
	_, err = importer.WriteDB().Exec(`UPDATE nodes SET uid = '' WHERE id = 'REC-1'`)
	require.NoError(t, err)

	// A uid-less pair is compared by title, so the edit must leave the title alone.
	data := exportOf(t, importer)
	data.Nodes[0].Description = "Edited description"
	data.Nodes[0].ContentHash = model.ComputeContentHash(data.Nodes[0].Title, "Edited description", "", "", nil)
	require.NoError(t, sqlite.RecomputeExportChecksum(data))

	res := mergeTheirs(t, importer, data)

	require.Empty(t, res.UnsentTasks)
	require.Equal(t, 1, countJournal(t, importer, `op_type = 'update_field' AND node_id = 'REC-1' AND sync_status = 'pending'`))
}

// TestBackfill_WithARelayCursor_StillRefuses (P1).
func TestBackfill_WithARelayCursor_StillRefuses(t *testing.T) {
	importer, _ := halfFilledPair(t)
	_, err := importer.WriteDB().Exec(`INSERT INTO relay_ingest_cursor (peer_id, segment_no, rs, pub_epoch) VALUES ('peer-a', 1, 5, 1)`)
	require.NoError(t, err)

	_, err = importer.Backfill(context.Background(), false)

	require.True(t, errors.Is(err, sqlite.ErrBackfillSyncEventsNonEmpty), "%v", err)
}

// TestBackfill_Partial_SkipsDeletedTasksAndSendsLinksToTheMissingOnes (P2, P3,
// P6): a deleted task is not sent, a link with an end in the set is, a link
// between held tasks is not sent again, and the dry run counts what the real
// run emits.
func TestBackfill_Partial_SkipsDeletedTasksAndSendsLinksToTheMissingOnes(t *testing.T) {
	importer, peer := peerPair(t, 2)
	createPlainTask(t, importer, "REC-3", 3, "", sameIDTime)
	createPlainTask(t, importer, "REC-4", 4, "", sameIDTime)
	require.NoError(t, importer.AddDependency(context.Background(), &model.Dependency{FromID: "REC-1", ToID: "REC-2", DepType: model.DepTypeRelated}))
	require.NoError(t, importer.AddDependency(context.Background(), &model.Dependency{FromID: "REC-3", ToID: "REC-1", DepType: model.DepTypeRelated}))
	replicateKeepingNothing(t, importer, peer, "REC-3", "REC-4")
	require.NoError(t, importer.DeleteNode(context.Background(), "REC-4", false, "alice"))
	dry, err := importer.BackfillDryRun(context.Background())
	require.NoError(t, err)

	res, err := importer.Backfill(context.Background(), false)

	require.NoError(t, err)
	require.Equal(t, dry, res, "the dry run counts what the run emits")
	require.Equal(t, 1, res.CreateEvents, "REC-3 only: REC-4 is deleted")
	require.Equal(t, 1, res.LinkDepEvents, "the link to REC-3 only")
}

// replicateKeepingNothing applies the journal to peer, then removes the create
// rows of the named tasks from the importer's journal, so the log lacks them.
func replicateKeepingNothing(t *testing.T, from, to *sqlite.Store, ids ...string) {
	t.Helper()
	for _, id := range ids {
		_, err := from.WriteDB().Exec(`DELETE FROM sync_events WHERE node_id = ?`, id)
		require.NoError(t, err)
	}
	replicate(t, from, to)
}

// TestBackfill_Partial_ChildOfADeletedParentWithNoCreate_IsNamedNotSent (F53).
func TestBackfill_Partial_ChildOfADeletedParentWithNoCreate_IsNamedNotSent(t *testing.T) {
	importer, _ := halfFilledPair(t)
	createSameIDTask(t, importer, "REC-3", "", 3, taskUID(t), "Parent")
	createSameIDTask(t, importer, "REC-3.1", "REC-3", 1, taskUID(t), "Child")
	_, err := importer.WriteDB().Exec(`DELETE FROM sync_events WHERE node_id IN ('REC-3', 'REC-3.1')`)
	require.NoError(t, err)
	_, err = importer.WriteDB().Exec(`UPDATE nodes SET deleted_at = '2026-01-02T00:00:00Z' WHERE id = 'REC-3'`)
	require.NoError(t, err)

	res, err := importer.Backfill(context.Background(), false)

	require.NoError(t, err)
	require.Equal(t, []string{"REC-3.1"}, res.NotSent)
	require.Equal(t, 1, res.CreateEvents, "REC-2 only")
}

// TestImportMerge_NoOpMergeOverTheCap_IsNotRefusedWhenPendingEventsPassTheCap
// (J2): the queue holds more pending events than the cap; a merge that
// changes nothing journals none and is not refused.
func TestImportMerge_NoOpMergeOverTheCap_IsNotRefusedWhenPendingEventsPassTheCap(t *testing.T) {
	importer, _ := peerPair(t, 3)
	_, err := importer.WriteDB().Exec(`UPDATE sync_events SET sync_status = 'pending'`)
	require.NoError(t, err)
	_, err = importer.WriteDB().Exec(`INSERT OR REPLACE INTO meta (key, value) VALUES ('sync.max_queue_size', '1')`)
	require.NoError(t, err)

	_, _, err = importer.ImportReconcile(context.Background(), exportOf(t, importer), sqlite.ImportReconcileOptions{
		Mode: sqlite.ImportModeMerge, Workflow: sqlite.WorkflowResolution{Prefer: sqlite.WorkflowTheirs}})

	require.NoError(t, err)
}

// TestImportMerge_LabelsClearedToEmpty_EventCarriesAnEmptyList (L2): the event
// payload reads [], never null, and an empty labels column clears the same way.
func TestImportMerge_LabelsClearedToEmpty_EventCarriesAnEmptyList(t *testing.T) {
	for _, held := range []string{`["a"]`, ``} {
		t.Run("held "+held, func(t *testing.T) {
			importer, _ := peerPair(t, 1)
			_, err := importer.WriteDB().Exec(`UPDATE nodes SET labels = ? WHERE id = 'REC-1'`, held)
			require.NoError(t, err)
			data := exportOf(t, importer)
			data.Nodes[0].Labels = ""
			data.Nodes[0].Title = "Retitled"
			data.Nodes[0].ContentHash = plainHash("Retitled")
			require.NoError(t, sqlite.RecomputeExportChecksum(data))

			mergeTheirs(t, importer, data)

			if held == "" {
				return
			}
			var payload string
			require.NoError(t, importer.WriteDB().QueryRow(`SELECT payload FROM sync_events
				WHERE node_id = 'REC-1' AND sync_status = 'pending' AND payload LIKE '%labels%'`).Scan(&payload))
			require.Contains(t, payload, `[]`)
			require.NotContains(t, payload, `null`)
		})
	}
}

// TestImportMerge_PulledTask_KeepsItsUID (M1): a task whose create was pulled
// from the hub keeps the uid the hub gave it.
func TestImportMerge_PulledTask_KeepsItsUID(t *testing.T) {
	hubSide, puller := newTestStore(t), newTestStore(t)
	createPlainTask(t, hubSide, "REC-1", 1, "", sameIDTime)
	replicate(t, hubSide, puller)
	var uid string
	require.NoError(t, puller.WriteDB().QueryRow(`SELECT uid FROM nodes WHERE id = 'REC-1'`).Scan(&uid))
	other := newTestStore(t)
	createSameIDTask(t, other, "REC-1", "", 1, "", "Shared task")

	file := exportOf(t, other)
	var createdAt string // the pull stamped the row at apply time; the file's author made it then too
	require.NoError(t, puller.WriteDB().QueryRow(`SELECT created_at FROM nodes WHERE id = 'REC-1'`).Scan(&createdAt))
	file.Nodes[0].CreatedAt = createdAt
	made, err := time.Parse(time.RFC3339, createdAt)
	require.NoError(t, err)
	file.Nodes[0].UID = uidMintedAt(t, made.Add(30*24*time.Hour)) // minted long after creation: a backfill, so the same task
	require.NoError(t, sqlite.RecomputeExportChecksum(file))

	res := mergeTheirs(t, puller, file)

	var after string
	require.NoError(t, puller.WriteDB().QueryRow(`SELECT uid FROM nodes WHERE id = 'REC-1'`).Scan(&after))
	require.Equal(t, uid, after)
	require.Contains(t, fieldsNamed(res, "REC-1"), "uid")
}
