// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests for MTIX-95.31.4 (FR-15.2i, FR-7.8), rounds 2 to 4: a merge keeps a
// local value that a stale copy of the node leaves empty; a local uid the
// file holds under another id is the same task moved there, not a different
// task; two nodes under one id with different uids are the same task only
// when their creation times are equal and one uid is a UUIDv7 minted more
// than an hour after that time (a backfill when a clone upgraded from before
// uids were shared), and different tasks otherwise; and the numbering of
// renumbered local tasks. Written red-first against the round before each.
package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// exportOf returns s's export.
func exportOf(t *testing.T, s *sqlite.Store) *sqlite.ExportData {
	t.Helper()
	data, err := s.Export(context.Background(), "", "")
	require.NoError(t, err)
	return data
}

// asSchemaV1 turns an export into what mtix 0.5.3 wrote: schema 1.0.0,
// without annotations and activity, with a valid checksum.
func asSchemaV1(t *testing.T, data *sqlite.ExportData) *sqlite.ExportData {
	t.Helper()
	data.SchemaVersion = "1.0.0"
	for i := range data.Nodes {
		data.Nodes[i].Annotations, data.Nodes[i].Activity = nil, nil
	}
	require.NoError(t, sqlite.RecomputeExportChecksum(data))
	return data
}

// mergeFile runs the merge mtix import --mode merge runs, without
// confirmation, and requires it to apply.
func mergeFile(t *testing.T, s *sqlite.Store, data *sqlite.ExportData) *sqlite.ImportReconcileReport {
	t.Helper()
	report, _, err := s.ImportReconcile(context.Background(), data, sqlite.ImportReconcileOptions{Mode: sqlite.ImportModeMerge})
	require.NoError(t, err)
	require.True(t, report.Applied)
	return report
}

// createPlainTask creates root task id with uid, an empty description, a
// real content hash and the given update time, in s.
func createPlainTask(t *testing.T, s *sqlite.Store, id string, seq int, uid string, updated time.Time) {
	t.Helper()
	require.NoError(t, s.CreateNode(context.Background(), &model.Node{
		ID: id, Project: "REC", Depth: 0, Seq: seq, Title: "Shared task",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeForDepth(0), ContentHash: model.ComputeContentHash("Shared task", "", "", "", nil),
		UID: uid, CreatedAt: sameIDTime, UpdatedAt: updated,
	}))
}

// setUpdatedAt sets node id's updated_at in s, as a write at that time
// would.
func setUpdatedAt(t *testing.T, s *sqlite.Store, id string, at time.Time) {
	t.Helper()
	_, err := s.WriteDB().ExecContext(context.Background(),
		`UPDATE nodes SET updated_at = ? WHERE id = ?`, at.UTC().Format(time.RFC3339), id)
	require.NoError(t, err)
}

// TestImportReconcile_MergeOfStaleCopy_KeepsFieldTheCopyBlanks verifies a
// merge keeps a local non-empty value that the file's copy of the node
// leaves empty when that copy is not current (it lacks a local activity
// entry, is older, or carries no activity at all), with the local status
// when the value belongs to it; the stale copy's other changes apply and
// the content hash matches the merged content.
func TestImportReconcile_MergeOfStaleCopy_KeepsFieldTheCopyBlanks(t *testing.T) {
	later := sameIDTime.Add(time.Hour)
	retitle := func(t *testing.T, s *sqlite.Store) {
		title := "Retitled by the teammate"
		require.NoError(t, s.UpdateNode(context.Background(), "REC-1", &store.NodeUpdate{Title: &title}))
		setUpdatedAt(t, s, "REC-1", sameIDTime.Add(30*time.Minute))
	}
	describe := func(t *testing.T, s *sqlite.Store) {
		d := "Local description"
		require.NoError(t, s.UpdateNode(context.Background(), "REC-1", &store.NodeUpdate{Description: &d}))
		setUpdatedAt(t, s, "REC-1", later)
	}
	tests := []struct {
		name     string
		local    func(t *testing.T, s *sqlite.Store) // after the teammate's copy is taken
		teammate func(t *testing.T, s *sqlite.Store)
		v1       bool
		check    func(t *testing.T, n *model.Node)
	}{
		{"older 2.0.0 copy leaves the description empty", describe, nil, false, func(t *testing.T, n *model.Node) {
			assert.Equal(t, "Local description", n.Description)
		}},
		{"0.5.3 copy leaves the description empty", describe, nil, true, func(t *testing.T, n *model.Node) {
			assert.Equal(t, "Local description", n.Description)
		}},
		{"newer copy without the local close reopens the task", func(t *testing.T, s *sqlite.Store) {
			require.NoError(t, s.ClaimNode(context.Background(), "REC-1", "agent-a"))
			require.NoError(t, s.TransitionStatus(context.Background(), "REC-1", model.StatusDone, "done", "agent-a"))
			setUpdatedAt(t, s, "REC-1", later) // older than the copy: only the missing activity makes it stale
		}, func(t *testing.T, s *sqlite.Store) {
			title := "Retitled by the teammate"
			require.NoError(t, s.UpdateNode(context.Background(), "REC-1", &store.NodeUpdate{Title: &title}))
			setUpdatedAt(t, s, "REC-1", later.Add(time.Hour))
		}, false, func(t *testing.T, n *model.Node) {
			assert.Equal(t, "Retitled by the teammate", n.Title, "the stale copy's change applies")
			assert.Equal(t, model.StatusDone, n.Status, "the status the kept closed time belongs to stays")
			assert.NotNil(t, n.ClosedAt)
		}},
		{"older copy leaves only the closed time empty", func(t *testing.T, s *sqlite.Store) {
			_, err := s.WriteDB().ExecContext(context.Background(),
				`UPDATE nodes SET status = 'done', closed_at = ?, progress = 1 WHERE id = 'REC-1'`,
				later.Format(time.RFC3339))
			require.NoError(t, err)
			setUpdatedAt(t, s, "REC-1", later)
		}, retitle, false, func(t *testing.T, n *model.Node) {
			assert.Equal(t, "Retitled by the teammate", n.Title)
			assert.Equal(t, model.StatusDone, n.Status, "the status stays with the kept closed time")
			assert.NotNil(t, n.ClosedAt)
		}},
		{"stale copy blanks a status field and a content field", func(t *testing.T, s *sqlite.Store) {
			require.NoError(t, s.ClaimNode(context.Background(), "REC-1", "agent-a"))
			describe(t, s)
		}, retitle, false, func(t *testing.T, n *model.Node) {
			assert.Equal(t, "Retitled by the teammate", n.Title)
			assert.Equal(t, "Local description", n.Description)
			assert.Equal(t, model.StatusInProgress, n.Status, "the whole local status stays")
			assert.Equal(t, "agent-a", n.Assignee)
		}},
		{"stale copy defers a task claimed locally", func(t *testing.T, s *sqlite.Store) {
			require.NoError(t, s.ClaimNode(context.Background(), "REC-1", "agent-a"))
		}, func(t *testing.T, s *sqlite.Store) {
			until := later.Add(48 * time.Hour)
			require.NoError(t, s.DeferNode(context.Background(), "REC-1", &until, "later", "agent-b"))
			retitle(t, s)
		}, false, func(t *testing.T, n *model.Node) {
			assert.Equal(t, "Retitled by the teammate", n.Title)
			assert.Equal(t, model.StatusInProgress, n.Status, "the local status stays with the kept claim")
			assert.Equal(t, "agent-a", n.Assignee)
			assert.Nil(t, n.DeferUntil, "the copy's wake time does not join the local status")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			local, teammate := newTestStore(t), newTestStore(t)
			uid := taskUID(t)
			createPlainTask(t, local, "REC-1", 1, uid, sameIDTime)
			createPlainTask(t, teammate, "REC-1", 1, uid, sameIDTime)
			if tt.teammate != nil {
				tt.teammate(t, teammate)
			}
			file := exportOf(t, teammate)
			if tt.v1 {
				file = asSchemaV1(t, file)
			}
			tt.local(t, local)

			mergeFile(t, local, file)
			node, err := local.GetNode(ctx, "REC-1")
			require.NoError(t, err)
			tt.check(t, node)
			assert.Equal(t, node.ComputeHash(), node.ContentHash, "the content hash matches the merged content")
		})
	}
}

// TestImportReconcile_MergeOfCurrentCopy_AppliesItsClear verifies a field a
// teammate cleared in a copy that has seen the local changes (it holds all
// of the node's activity and is not older) is cleared by the merge.
func TestImportReconcile_MergeOfCurrentCopy_AppliesItsClear(t *testing.T) {
	ctx := context.Background()
	local, teammate := newTestStore(t), newTestStore(t)
	uid := taskUID(t)
	createSameIDTask(t, local, "REC-1", "", 1, uid, "Shared task")
	createSameIDTask(t, teammate, "REC-1", "", 1, uid, "Shared task")
	cleared := ""
	require.NoError(t, teammate.UpdateNode(ctx, "REC-1", &store.NodeUpdate{Description: &cleared}))
	setUpdatedAt(t, teammate, "REC-1", sameIDTime.Add(48*time.Hour)) // later than the local copy

	mergeFile(t, local, exportOf(t, teammate))
	node, err := local.GetNode(ctx, "REC-1")
	require.NoError(t, err)
	assert.Empty(t, node.Description, "a current copy's clear applies")
}

// TestImportReconcile_LocalUIDUnderAnotherID_MovesWithoutConfirm verifies a
// local task that the file holds under another id (another clone
// renumbered it) moves there with its subtree, without confirmation, while
// the file's task takes its old id; nothing is lost, the moves are
// reported, and a dependency follows the moved task.
func TestImportReconcile_LocalUIDUnderAnotherID_MovesWithoutConfirm(t *testing.T) {
	ctx := context.Background()
	local, teammate := newTestStore(t), newTestStore(t)
	shared, moved, child := taskUID(t), taskUID(t), taskUID(t)
	for _, s := range []*sqlite.Store{local, teammate} {
		createSameIDTask(t, s, "REC-1", "", 1, shared, "Shared task")
	}
	createSameIDTask(t, local, "REC-2", "", 2, moved, "Moved task")
	createSameIDTask(t, local, "REC-2.1", "REC-2", 1, child, "Its subtask")
	require.NoError(t, local.AddDependency(ctx, &model.Dependency{
		FromID: "REC-1", ToID: "REC-2", DepType: model.DepTypeBlocks, CreatedAt: sameIDTime,
	}))
	theirs := createSameIDTask(t, teammate, "REC-2", "", 2, taskUID(t), "Teammate task")
	createSameIDTask(t, teammate, "REC-3", "", 3, moved, "Moved task")
	createSameIDTask(t, teammate, "REC-3.1", "REC-3", 1, child, "Its subtask")
	require.NoError(t, teammate.AddDependency(ctx, &model.Dependency{
		FromID: "REC-1", ToID: "REC-3", DepType: model.DepTypeBlocks, CreatedAt: sameIDTime,
	}))
	file := exportOf(t, teammate)

	diff, err := sqlite.DiffReplace(exportOf(t, local), file)
	require.NoError(t, err)
	assert.False(t, diff.Lossy(), "a moved task is not lost: %+v", diff.Losses)

	report := mergeFile(t, local, file)
	assert.Empty(t, report.LocalRenumbers)
	assert.Equal(t, []sqlite.ImportRemapEntry{
		{UID: moved, OldPath: "REC-2", NewPath: "REC-3"},
		{UID: child, OldPath: "REC-2.1", NewPath: "REC-3.1"},
	}, report.Moved)
	assert.Contains(t, report.String(), "uid="+moved+" REC-2 -> REC-3")
	for uid, want := range map[string]string{moved: "REC-3", child: "REC-3.1", theirs: "REC-2"} {
		path, err := local.ResolveDisplayPathByUID(ctx, uid)
		require.NoError(t, err)
		assert.Equal(t, want, path)
	}
	deps, err := local.GetBlockers(ctx, "REC-3")
	require.NoError(t, err)
	assert.Len(t, deps, 1)
}

// TestImportReconcile_UIDsSwappedBetweenIDs_MovesBoth verifies two local
// tasks the file holds under each other's id both move (a swap), with no
// renumbering and no confirmation.
func TestImportReconcile_UIDsSwappedBetweenIDs_MovesBoth(t *testing.T) {
	ctx := context.Background()
	local, teammate := newTestStore(t), newTestStore(t)
	a, b := taskUID(t), taskUID(t)
	createSameIDTask(t, local, "REC-1", "", 1, a, "Task A")
	createSameIDTask(t, local, "REC-2", "", 2, b, "Task B")
	createSameIDTask(t, teammate, "REC-1", "", 1, b, "Task B")
	createSameIDTask(t, teammate, "REC-2", "", 2, a, "Task A")

	report := mergeFile(t, local, exportOf(t, teammate))
	assert.Len(t, report.Moved, 2)
	for uid, want := range map[string]string{a: "REC-2", b: "REC-1"} {
		path, err := local.ResolveDisplayPathByUID(ctx, uid)
		require.NoError(t, err)
		assert.Equal(t, want, path)
	}
}

// TestDiffReplace_SameIDOtherUID_ClassifiesByIdentity verifies the rule
// that decides whether two nodes under one id with different uids are one
// task (MTIX-95.31.4, round 4). Calling two tasks different is safe (a
// merge renumbers one, with confirmation); calling them the same loses one.
// So they are the same task only when their creation times are equal and
// one uid is a UUIDv7 minted more than an hour after that time: a uid a
// clone assigned when it upgraded from before uids were shared. A uid
// minted before the creation time (a node a hub applied, created_at being
// the apply time), within the hour after it (a create that waited for the
// write lock), or that is not a UUIDv7, leaves them different tasks.
func TestDiffReplace_SameIDOtherUID_ClassifiesByIdentity(t *testing.T) {
	minted := func(offset time.Duration) func(t *testing.T) string {
		return func(t *testing.T) string { return uidMintedAt(t, sameIDTime.Add(offset)) }
	}
	notUUID := func(*testing.T) string { return "01J9NOTAUUIDV70000000000001" }
	v4Late := func(t *testing.T) string { // minted a month late, but version 4
		u := uuid.MustParse(backfilledUID(t))
		u[6] = u[6]&0x0f | 0x40
		return u.String()
	}
	tests := []struct {
		name          string
		localUID      func(t *testing.T) string
		fileUID       func(t *testing.T) string
		fileCreatedAt string // "" keeps the local creation time
		different     bool
	}{
		{"both minted at creation in the same second", taskUID, taskUID, "", true},
		{"a create that waited 3 s for the write lock", taskUID, minted(3 * time.Second), "", true},
		{"minted 59 minutes after creation", taskUID, minted(59 * time.Minute), "", true},
		{"minted exactly one hour after creation", taskUID, minted(time.Hour), "", true},
		{"a node a hub applied: minted before its creation time", taskUID, minted(-10 * time.Minute), "", true},
		{"file uid not a UUID", taskUID, notUUID, "", true},
		{"file uid a UUIDv4 with a late time in its first bits", taskUID, v4Late, "", true},
		{"file uid backfilled", taskUID, backfilledUID, "", false},
		{"local uid backfilled", backfilledUID, taskUID, "", false},
		{"both backfilled", backfilledUID, backfilledUID, "", false},
		{"minted one hour and one second after creation", taskUID, minted(time.Hour + time.Second), "", false},
		{"backfilled uid, other creation time", taskUID, backfilledUID, "2026-09-24T09:00:00Z", true},
		{"file without a creation time", taskUID, backfilledUID, "none", true},
		{"creation times that cannot be read", taskUID, backfilledUID, "unreadable", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			local := newTestStore(t)
			createSameIDTask(t, local, "REC-1", "", 1, tt.localUID(t), "Local title")
			localData := exportOf(t, local)
			file := exportOf(t, local)
			file.Nodes[0].UID, file.Nodes[0].Title = tt.fileUID(t), "File title"
			switch tt.fileCreatedAt {
			case "":
			case "none":
				file.Nodes[0].CreatedAt = ""
			case "unreadable": // the same text on both sides, but not a time
				file.Nodes[0].CreatedAt, localData.Nodes[0].CreatedAt = "not-a-time", "not-a-time"
			default:
				file.Nodes[0].CreatedAt = tt.fileCreatedAt
			}

			diff, err := sqlite.DiffReplace(localData, file)
			require.NoError(t, err)
			loss := lossOf(diff, "REC-1")
			assert.Equal(t, tt.different, loss != nil && loss.DifferentTask, "%+v", diff.Losses)
		})
	}
}

// TestImportReconcile_SameSecondCreates_RenumberedOnlyWithConfirm verifies
// the S0 case: two clones create REC-1 in the same second (both uids minted
// then); the merge treats them as different tasks and renumbers the local
// one only with confirmation.
func TestImportReconcile_SameSecondCreates_RenumberedOnlyWithConfirm(t *testing.T) {
	ctx := context.Background()
	local, teammate := newTestStore(t), newTestStore(t)
	mine := createSameIDTask(t, local, "REC-1", "", 1, taskUID(t), "Local task")
	theirs := createSameIDTask(t, teammate, "REC-1", "", 1, taskUID(t), "Teammate task")
	before := storeSnapshotJSON(t, local)

	report, _, err := local.ImportReconcile(ctx, exportOf(t, teammate), sqlite.ImportReconcileOptions{Mode: sqlite.ImportModeMerge})
	require.ErrorIs(t, err, sqlite.ErrImportConfirmationRequired)
	assert.Equal(t, []sqlite.ImportRemapEntry{{UID: mine, OldPath: "REC-1", NewPath: "REC-2"}}, report.LocalRenumbers)
	assert.Equal(t, before, storeSnapshotJSON(t, local))

	_, _, err = local.ImportReconcile(ctx, exportOf(t, teammate), sqlite.ImportReconcileOptions{
		Mode: sqlite.ImportModeMerge, Confirm: true,
	})
	require.NoError(t, err)
	for uid, want := range map[string]string{mine: "REC-2", theirs: "REC-1"} {
		path, err := local.ResolveDisplayPathByUID(ctx, uid)
		require.NoError(t, err)
		assert.Equal(t, want, path)
	}
}

// TestImportReconcile_BackfilledUIDs_MergeAdoptsFileUID verifies the merge
// of a board whose shared tasks carry other uids with the same creation
// times renumbers nothing and adopts the file's uids, content unchanged.
func TestImportReconcile_BackfilledUIDs_MergeAdoptsFileUID(t *testing.T) {
	ctx := context.Background()
	local, teammate := newTestStore(t), newTestStore(t)
	createSameIDTask(t, local, "REC-1", "", 1, backfilledUID(t), "Shared task")
	fileUID := createSameIDTask(t, teammate, "REC-1", "", 1, backfilledUID(t), "Shared task")

	report := mergeFile(t, local, exportOf(t, teammate))
	assert.Empty(t, report.LocalRenumbers)
	assert.Empty(t, report.Moved)
	node, err := local.GetNode(ctx, "REC-1")
	require.NoError(t, err)
	assert.Equal(t, fileUID, node.UID, "the local node adopts the file's uid")
}

// TestImportReconcile_TwoLocalTasksRenumbered_GetDistinctNumbers verifies
// two local tasks renumbered under one parent get different numbers (both
// clones created REC-2 and REC-3).
func TestImportReconcile_TwoLocalTasksRenumbered_GetDistinctNumbers(t *testing.T) {
	ctx := context.Background()
	local, teammate := newTestStore(t), newTestStore(t)
	shared := taskUID(t)
	for _, s := range []*sqlite.Store{local, teammate} {
		createSameIDTask(t, s, "REC-1", "", 1, shared, "Shared task")
	}
	two := createSameIDTask(t, local, "REC-2", "", 2, taskUID(t), "Local two")
	three := createSameIDTask(t, local, "REC-3", "", 3, taskUID(t), "Local three")
	createSameIDTask(t, teammate, "REC-2", "", 2, taskUID(t), "Their two")
	createSameIDTask(t, teammate, "REC-3", "", 3, taskUID(t), "Their three")

	report, _, err := local.ImportReconcile(ctx, exportOf(t, teammate), sqlite.ImportReconcileOptions{
		Mode: sqlite.ImportModeMerge, Confirm: true,
	})
	require.NoError(t, err)
	assert.Equal(t, []sqlite.ImportRemapEntry{
		{UID: two, OldPath: "REC-2", NewPath: "REC-4"},
		{UID: three, OldPath: "REC-3", NewPath: "REC-5"},
	}, report.LocalRenumbers)
	assert.NotContains(t, report.String(), "not applied", "an applied report has no not-applied line")
}

// TestImportReconcile_RenumberNumbering_PrefixAndSoftDeleted verifies the
// numbering and the id boundaries of a renumber: siblings REC-2 and REC-20
// are separate tasks (REC-20 is not under REC-2), and a soft-deleted
// sibling with the highest number still counts.
func TestImportReconcile_RenumberNumbering_PrefixAndSoftDeleted(t *testing.T) {
	tests := []struct {
		name  string
		local func(t *testing.T, s *sqlite.Store) // besides a different REC-2
		file  []string                            // ids the teammate holds besides REC-2
		want  []string                            // old -> new of the renumbered local tasks
	}{
		{"siblings REC-2 and REC-20", func(t *testing.T, s *sqlite.Store) {
			createSameIDTask(t, s, "REC-20", "", 20, taskUID(t), "Local twenty")
		}, []string{"REC-20"}, []string{"REC-2 -> REC-21", "REC-20 -> REC-22"}},
		{"soft-deleted highest sibling", func(t *testing.T, s *sqlite.Store) {
			createSameIDTask(t, s, "REC-5", "", 5, taskUID(t), "Deleted five")
			require.NoError(t, s.DeleteNode(context.Background(), "REC-5", false, "agent-local"))
		}, nil, []string{"REC-2 -> REC-6"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			local, teammate := newTestStore(t), newTestStore(t)
			createSameIDTask(t, local, "REC-2", "", 2, taskUID(t), "Local two")
			tt.local(t, local)
			createSameIDTask(t, teammate, "REC-2", "", 2, taskUID(t), "Their two")
			for _, id := range tt.file {
				createSameIDTask(t, teammate, id, "", 20, taskUID(t), "Their "+id)
			}

			report, _, err := local.ImportReconcile(context.Background(), exportOf(t, teammate),
				sqlite.ImportReconcileOptions{Mode: sqlite.ImportModeMerge, Confirm: true})
			require.NoError(t, err)
			var got []string
			for _, m := range report.LocalRenumbers {
				got = append(got, m.OldPath+" -> "+m.NewPath)
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestImport_MergeOverDifferentTaskSameContentHash_Refused verifies merge
// import refuses a different task under a local id even when both share a
// content hash, the case merge would otherwise skip as unchanged.
func TestImport_MergeOverDifferentTaskSameContentHash_Refused(t *testing.T) {
	ctx := context.Background()
	local := newTestStore(t)
	createSameIDTask(t, local, "REC-1", "", 1, taskUID(t), "Same title")
	file := exportOf(t, local)
	file.Nodes[0].UID, file.Nodes[0].CreatedAt = taskUID(t), "2026-09-24T09:00:00Z"
	require.NoError(t, sqlite.RecomputeExportChecksum(file))

	_, err := local.Import(ctx, file, sqlite.ImportModeMerge, false)
	require.ErrorIs(t, err, model.ErrConflict)
}

// TestImportReconcile_BeforeWrite_RunsOnlyBeforeAWrite verifies the step a
// caller runs before the import writes (mtix import --mode merge backs up
// there) runs once when the import writes, never when it writes nothing
// (confirmation awaited, a file that fails its checks, a merge that would
// change nothing), and that its error stops the import.
func TestImportReconcile_BeforeWrite_RunsOnlyBeforeAWrite(t *testing.T) {
	tests := []struct {
		name      string
		edit      func(d *sqlite.ExportData)
		hookErr   error
		wantCalls int
		wantErr   error
	}{
		{"the import writes", nil, nil, 1, nil},
		{"the merge would change nothing", func(d *sqlite.ExportData) {
			d.Nodes[0].Title, d.Nodes[0].ContentHash = "Local title", "h-Local title"
			require.NoError(t, sqlite.RecomputeExportChecksum(d))
		}, nil, 0, nil},
		{"confirmation awaited", func(d *sqlite.ExportData) {
			d.Nodes[0].UID, d.Nodes[0].CreatedAt = "01a0d56f-0000-7000-8000-00000000f001", "2026-09-23T08:00:00Z"
			require.NoError(t, sqlite.RecomputeExportChecksum(d))
		}, nil, 0, sqlite.ErrImportConfirmationRequired},
		{"the file fails its checks", func(d *sqlite.ExportData) { d.Checksum = "stale" }, nil, 0, model.ErrInvalidInput},
		{"the step fails", nil, model.ErrConflict, 1, model.ErrConflict},
		{"a new node whose type the file does not derive from depth", func(d *sqlite.ExportData) {
			added := d.Nodes[0]
			added.ID, added.Seq, added.UID = "REC-2", 2, taskUID(t)
			added.NodeType = "not-derived" // the checksum covers it: the dry run must not change it
			d.Nodes = append(d.Nodes, added)
			d.NodeCount = len(d.Nodes)
			require.NoError(t, sqlite.RecomputeExportChecksum(d))
		}, nil, 1, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			local := newTestStore(t)
			createSameIDTask(t, local, "REC-1", "", 1, taskUID(t), "Local title")
			file := exportOf(t, local)
			file.Nodes[0].Title, file.Nodes[0].ContentHash = "Retitled", "h-Retitled"
			require.NoError(t, sqlite.RecomputeExportChecksum(file))
			if tt.edit != nil {
				tt.edit(file)
			}
			calls := 0
			_, _, err := local.ImportReconcile(context.Background(), file, sqlite.ImportReconcileOptions{
				Mode: sqlite.ImportModeMerge, BeforeWrite: func() error { calls++; return tt.hookErr },
			})
			assert.Equal(t, tt.wantCalls, calls)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				node, getErr := local.GetNode(context.Background(), "REC-1")
				require.NoError(t, getErr)
				assert.Equal(t, "Local title", node.Title, "nothing was written")
				return
			}
			require.NoError(t, err)
		})
	}
}

// TestImportReconcile_RenumberSkipsANumberWithNodesUnderIt verifies a
// renumbered local task never takes a number that a local or a file node
// sits under although no node holds the number itself (its parent is
// gone): the plan skips it.
func TestImportReconcile_RenumberSkipsANumberWithNodesUnderIt(t *testing.T) {
	tests := []struct {
		name  string
		local func(t *testing.T, s *sqlite.Store)
		file  []sqlite.TestExportNode
	}{
		{"a local node under the number", func(t *testing.T, s *sqlite.Store) {
			createSameIDTask(t, s, "REC-5", "", 5, taskUID(t), "Parent gone")
			createSameIDTask(t, s, "REC-5.1", "REC-5", 1, taskUID(t), "Left behind")
			_, err := s.WriteDB().ExecContext(context.Background(), `DELETE FROM nodes WHERE id = 'REC-5'`)
			require.NoError(t, err)
		}, nil},
		{"a file node under the number", nil, []sqlite.TestExportNode{{
			ID: "REC-5.1", ParentID: "REC-5", Project: "REC", Depth: 1, Seq: 1, Title: "Orphan",
			ContentHash: "h-o", UID: "01a0d56f-0000-7000-8000-00000000f101", CreatedAt: sameIDTime, UpdatedAt: sameIDTime,
		}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			local := newTestStore(t)
			createSameIDTask(t, local, "REC-2", "", 2, taskUID(t), "Local two")
			createSameIDTask(t, local, "REC-4", "", 4, taskUID(t), "Local four")
			if tt.local != nil {
				tt.local(t, local)
			}
			nodes := append([]sqlite.TestExportNode{{ID: "REC-2", Project: "REC", Seq: 2, Title: "Their two",
				ContentHash: "h-t", UID: taskUID(t), CreatedAt: sameIDTime, UpdatedAt: sameIDTime}}, tt.file...)
			report, _, err := local.ImportReconcile(context.Background(), reconcileExport(t, "REC", nodes...),
				sqlite.ImportReconcileOptions{Mode: sqlite.ImportModeMerge})
			require.ErrorIs(t, err, sqlite.ErrImportConfirmationRequired)
			require.Len(t, report.LocalRenumbers, 1)
			assert.Equal(t, "REC-6", report.LocalRenumbers[0].NewPath, "REC-5 has a node under it")
		})
	}
}

// TestImportReconcile_MoveOntoAKeptTask_RejectedWritesNothing verifies a
// task the file moves onto an id a local task the merge keeps also holds
// (the same task under a backfilled uid) is rejected, writing nothing.
func TestImportReconcile_MoveOntoAKeptTask_RejectedWritesNothing(t *testing.T) {
	ctx := context.Background()
	local := newTestStore(t)
	kept, moved := backfilledUID(t), taskUID(t)
	createPlainTask(t, local, "REC-1", 1, kept, sameIDTime)
	createPlainTask(t, local, "REC-2", 2, moved, sameIDTime)
	before := storeSnapshotJSON(t, local)
	file := reconcileExport(t, "REC", sqlite.TestExportNode{
		ID: "REC-1", Project: "REC", Seq: 1, Title: "Shared task", ContentHash: "h",
		UID: moved, CreatedAt: sameIDTime, UpdatedAt: sameIDTime,
	})

	_, _, err := local.ImportReconcile(ctx, file, sqlite.ImportReconcileOptions{Mode: sqlite.ImportModeMerge, Confirm: true})
	require.ErrorIs(t, err, model.ErrConflict)
	assert.Equal(t, before, storeSnapshotJSON(t, local))
}

// TestImportReconcile_NodeWhoseParentIsGone_KeepsItsID verifies a local
// node whose parent was removed keeps its id through a merge that moves
// nothing, and is not reported as moved.
func TestImportReconcile_NodeWhoseParentIsGone_KeepsItsID(t *testing.T) {
	ctx := context.Background()
	local := newTestStore(t)
	shared := taskUID(t)
	createSameIDTask(t, local, "REC-1", "", 1, shared, "Shared task")
	createSameIDTask(t, local, "REC-5", "", 5, taskUID(t), "Parent gone")
	left := createSameIDTask(t, local, "REC-5.1", "REC-5", 1, taskUID(t), "Left behind")
	_, err := local.WriteDB().ExecContext(ctx, `DELETE FROM nodes WHERE id = 'REC-5'`)
	require.NoError(t, err)
	file := reconcileExport(t, "REC", sqlite.TestExportNode{ID: "REC-1", Project: "REC", Seq: 1,
		Title: "Shared task", ContentHash: "h", UID: shared, CreatedAt: sameIDTime,
		UpdatedAt: sameIDTime})

	report := mergeFile(t, local, file)
	assert.Empty(t, report.Moved)
	assert.Empty(t, report.LocalRenumbers)
	path, err := local.ResolveDisplayPathByUID(ctx, left)
	require.NoError(t, err)
	assert.Equal(t, "REC-5.1", path)
}

// TestImportReconcile_MergeOfUIDlessBoard_KeepsLocalUIDs verifies a merge of
// a board without uids (written by mtix 0.3.x) whose content is unchanged
// never writes an empty uid over the local ones.
func TestImportReconcile_MergeOfUIDlessBoard_KeepsLocalUIDs(t *testing.T) {
	ctx := context.Background()
	local := newTestStore(t)
	uid := createSameIDTask(t, local, "REC-1", "", 1, taskUID(t), "Shared task")
	file := exportOf(t, local)
	file.Nodes[0].UID = ""
	require.NoError(t, sqlite.RecomputeExportChecksum(file))

	mergeFile(t, local, file)
	node, err := local.GetNode(ctx, "REC-1")
	require.NoError(t, err)
	assert.Equal(t, uid, node.UID, "an empty uid never replaces a local one")
}

// TestImportReconcile_ProvisionalAndMoveUnderOneParent_TakeDistinctNumbers
// verifies an incoming provisional node never takes the number a local task
// moves to under the same parent.
func TestImportReconcile_ProvisionalAndMoveUnderOneParent_TakeDistinctNumbers(t *testing.T) {
	ctx := context.Background()
	local := newTestStore(t)
	shared, moved, prov := taskUID(t), taskUID(t), taskUID(t)
	createSameIDTask(t, local, "REC-1", "", 1, shared, "Shared task")
	createSameIDTask(t, local, "REC-1.1", "REC-1", 1, moved, "Moved child")
	data := reconcileExport(t, "REC",
		sqlite.TestExportNode{ID: "REC-1", Project: "REC", Seq: 1, Title: "Shared task",
			ContentHash: "h-Shared task", UID: shared, CreatedAt: sameIDTime, UpdatedAt: sameIDTime},
		sqlite.TestExportNode{ID: "REC-1.2", ParentID: "REC-1", Project: "REC", Depth: 1, Seq: 2,
			Title: "Moved child", ContentHash: "h-Moved child", UID: moved, CreatedAt: sameIDTime, UpdatedAt: sameIDTime},
		sqlite.TestExportNode{ID: provisionalPath(t, "REC-1", prov), ParentID: "REC-1", Project: "REC",
			Depth: 1, Seq: 1, Title: "Provisional child", ContentHash: "h-pc", UID: prov,
			CreatedAt: sameIDTime, UpdatedAt: sameIDTime},
	)

	report, _, err := local.ImportReconcile(ctx, data, sqlite.ImportReconcileOptions{
		Mode: sqlite.ImportModeMerge, Confirm: true,
	})
	require.NoError(t, err)
	assert.Equal(t, []sqlite.ImportRemapEntry{{UID: moved, OldPath: "REC-1.1", NewPath: "REC-1.2"}}, report.Moved)
	for uid, want := range map[string]string{moved: "REC-1.2", prov: "REC-1.3"} {
		path, err := local.ResolveDisplayPathByUID(ctx, uid)
		require.NoError(t, err)
		assert.Equal(t, want, path)
	}
}

// TestImportReconcile_MovedParentsChildCollides_RenumberedAfterSiblings
// verifies a local child of a moved task, which collides with a different
// task under the parent's new id, is renumbered after every sibling the
// moved parent brings (REC-3.3, not its sibling's REC-3.2).
func TestImportReconcile_MovedParentsChildCollides_RenumberedAfterSiblings(t *testing.T) {
	ctx := context.Background()
	local, teammate := newTestStore(t), newTestStore(t)
	parent, first, second := taskUID(t), taskUID(t), taskUID(t)
	createSameIDTask(t, local, "REC-2", "", 2, parent, "Moved parent")
	createSameIDTask(t, local, "REC-2.1", "REC-2", 1, first, "Local first child")
	createSameIDTask(t, local, "REC-2.2", "REC-2", 2, second, "Local second child")
	createSameIDTask(t, teammate, "REC-2", "", 2, taskUID(t), "Teammate task")
	createSameIDTask(t, teammate, "REC-3", "", 3, parent, "Moved parent")
	createSameIDTask(t, teammate, "REC-3.1", "REC-3", 1, taskUID(t), "Teammate child")

	report, _, err := local.ImportReconcile(ctx, exportOf(t, teammate), sqlite.ImportReconcileOptions{
		Mode: sqlite.ImportModeMerge, Confirm: true,
	})
	require.NoError(t, err)
	assert.Equal(t, []sqlite.ImportRemapEntry{{UID: first, OldPath: "REC-2.1", NewPath: "REC-3.3"}}, report.LocalRenumbers)
	assert.Equal(t, []sqlite.ImportRemapEntry{
		{UID: parent, OldPath: "REC-2", NewPath: "REC-3"},
		{UID: second, OldPath: "REC-2.2", NewPath: "REC-3.2"},
	}, report.Moved)
}

// TestDiffReplace_FileGivesIDToAnotherLocalTask_ListedAsDifferentTask
// verifies a local task whose id the file gives to another local task (a
// clone moved it there), and which the file lacks, is listed as a different
// task under its id, so the refusal says the merge renumbers it; the moved
// task is not a loss. Both carry backfilled uids with one creation time, so
// only the move tells them apart.
func TestDiffReplace_FileGivesIDToAnotherLocalTask_ListedAsDifferentTask(t *testing.T) {
	local := newTestStore(t)
	createSameIDTask(t, local, "REC-1", "", 1, backfilledUID(t), "Dropped task")
	kept := createSameIDTask(t, local, "REC-2", "", 2, backfilledUID(t), "Kept task")
	file := exportOf(t, local)
	file.Nodes = file.Nodes[1:]
	file.Nodes[0].ID, file.Nodes[0].Seq = "REC-1", 1
	require.Equal(t, kept, file.Nodes[0].UID)

	diff, err := sqlite.DiffReplace(exportOf(t, local), file)
	require.NoError(t, err)
	loss := lossOf(diff, "REC-1")
	require.NotNil(t, loss)
	assert.True(t, loss.DifferentTask)
	assert.Equal(t, "Dropped task", loss.LocalTitle)
	assert.Equal(t, "Kept task", loss.FileTitle)
	assert.Nil(t, lossOf(diff, "REC-2"), "the kept task only moved")
}

// TestImportReconcile_WriteAfterTheDryRun_WritesNothing verifies a write
// committed between the merge's dry run and its write (here from the step
// run before it writes) makes the merge write nothing: the dry run's
// checksum of the store is re-checked inside the write's transaction, so
// the write survives and the error says the store changed.
func TestImportReconcile_WriteAfterTheDryRun_WritesNothing(t *testing.T) {
	ctx := context.Background()
	local := newTestStore(t)
	createSameIDTask(t, local, "REC-1", "", 1, taskUID(t), "Local title")
	file := exportOf(t, local)
	file.Nodes[0].Title, file.Nodes[0].ContentHash = "File title", "h-File title"
	require.NoError(t, sqlite.RecomputeExportChecksum(file))
	concurrent := "Written after the dry run"

	_, _, err := local.ImportReconcile(ctx, file, sqlite.ImportReconcileOptions{
		Mode: sqlite.ImportModeMerge,
		BeforeWrite: func() error {
			return local.UpdateNode(ctx, "REC-1", &store.NodeUpdate{Title: &concurrent})
		},
	})
	require.ErrorIs(t, err, sqlite.ErrStoreChangedSinceCheck)
	node, err := local.GetNode(ctx, "REC-1")
	require.NoError(t, err)
	assert.Equal(t, concurrent, node.Title, "the concurrent write survives; nothing was imported")
}
