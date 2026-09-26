// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests for MTIX-95.31.6 (FR-7.8): a merge import reports every uid it
// adopts, with the id, both uids and both titles, in its report and in its
// result, so a merge of two tasks the identity rule cannot tell apart (two
// clones created the id in the same second, at least one clone's event log
// lacks the create event, and both upgraded more than an hour later) is
// visible.
// Written red-first against the MTIX-95.31.4 code, which adopted such uids
// without a word; round 2 adds a file node without a uid, and checks that
// every local uid not reported as adopted survives.
package sqlite_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// adoptionLine is the report line of one uid adoption.
func adoptionLine(a sqlite.ImportUIDAdoption) string {
	local := a.LocalUID
	if local == "" {
		local = "(new)" // the merge gives the local task a backfill uid first (MTIX-95.31.9)
	}
	return fmt.Sprintf("    - %s local uid=%s -> file uid=%s (local %q, file %q)\n",
		a.ID, local, a.FileUID, a.LocalTitle, a.FileTitle)
}

// titlesDifferNote starts the report line printed when an adopted task's
// two titles differ.
const titlesDifferNote = "  a task above whose titles differ may be two different tasks"

// TestImportReconcile_MergeAdoptingUIDs_ReportsEachAdoption verifies the
// report and the result of a merge list every uid the merge adopts, exactly
// those, and that the merge wrote each of them; a replace adopts none.
func TestImportReconcile_MergeAdoptingUIDs_ReportsEachAdoption(t *testing.T) {
	type stores struct{ local, teammate *sqlite.Store }
	tests := []struct {
		name    string
		mode    sqlite.ImportMode
		confirm bool
		setup   func(t *testing.T, s stores) []sqlite.ImportUIDAdoption
		wantErr error
	}{
		{"one task under uids both clones assigned at upgrade", sqlite.ImportModeMerge, false,
			func(t *testing.T, s stores) []sqlite.ImportUIDAdoption {
				mine := createSameIDTask(t, s.local, "REC-1", "", 1, backfilledUID(t), "Shared task")
				theirs := createSameIDTask(t, s.teammate, "REC-1", "", 1, backfilledUID(t), "Shared task")
				return []sqlite.ImportUIDAdoption{{ID: "REC-1", LocalUID: mine, FileUID: theirs,
					LocalTitle: "Shared task", FileTitle: "Shared task"}}
			}, nil},
		{"two tasks created in the same second without create events, both upgraded an hour later",
			sqlite.ImportModeMerge, false,
			func(t *testing.T, s stores) []sqlite.ImportUIDAdoption {
				mine := createSameIDTask(t, s.local, "REC-1", "", 1, backfilledUID(t), "A pre-upgrade task")
				theirs := createSameIDTask(t, s.teammate, "REC-1", "", 1, backfilledUID(t), "B pre-upgrade task")
				return []sqlite.ImportUIDAdoption{{ID: "REC-1", LocalUID: mine, FileUID: theirs,
					LocalTitle: "A pre-upgrade task", FileTitle: "B pre-upgrade task"}}
			}, nil},
		{"a local task without a uid", sqlite.ImportModeMerge, false,
			func(t *testing.T, s stores) []sqlite.ImportUIDAdoption {
				createSameIDTask(t, s.local, "REC-1", "", 1, taskUID(t), "Shared task")
				_, err := s.local.WriteDB().ExecContext(context.Background(), `UPDATE nodes SET uid = NULL`)
				require.NoError(t, err)
				theirs := createSameIDTask(t, s.teammate, "REC-1", "", 1, taskUID(t), "Shared task")
				return []sqlite.ImportUIDAdoption{{ID: "REC-1", FileUID: theirs,
					LocalTitle: "Shared task", FileTitle: "Shared task"}}
			}, nil},
		{"a file node without a uid at an id the store holds, one title", sqlite.ImportModeMerge, false,
			func(t *testing.T, s stores) []sqlite.ImportUIDAdoption {
				createSameIDTask(t, s.local, "REC-1", "", 1, backfilledUID(t), "Shared task")
				createSameIDTask(t, s.teammate, "REC-1", "", 1, backfilledUID(t), "Shared task")
				_, err := s.teammate.WriteDB().ExecContext(context.Background(), `UPDATE nodes SET uid = NULL`)
				require.NoError(t, err)
				return nil
			}, nil},
		{"a soft-deleted local task", sqlite.ImportModeMerge, false,
			func(t *testing.T, s stores) []sqlite.ImportUIDAdoption {
				mine := createSameIDTask(t, s.local, "REC-1", "", 1, backfilledUID(t), "Shared task")
				require.NoError(t, s.local.DeleteNode(context.Background(), "REC-1", false, "agent-local"))
				theirs := createSameIDTask(t, s.teammate, "REC-1", "", 1, backfilledUID(t), "Shared task")
				return []sqlite.ImportUIDAdoption{{ID: "REC-1", LocalUID: mine, FileUID: theirs,
					LocalTitle: "Shared task", FileTitle: "Shared task"}}
			}, nil},
		{"the same uids, and a task new to the store", sqlite.ImportModeMerge, false,
			func(t *testing.T, s stores) []sqlite.ImportUIDAdoption {
				uid := taskUID(t)
				createSameIDTask(t, s.local, "REC-1", "", 1, uid, "Shared task")
				createSameIDTask(t, s.teammate, "REC-1", "", 1, uid, "Retitled shared task")
				createSameIDTask(t, s.teammate, "REC-2", "", 2, backfilledUID(t), "Teammate task")
				return nil
			}, nil},
		{"two tasks the file swapped between their ids", sqlite.ImportModeMerge, false,
			func(t *testing.T, s stores) []sqlite.ImportUIDAdoption {
				a, b := backfilledUID(t), backfilledUID(t)
				createSameIDTask(t, s.local, "REC-1", "", 1, a, "Task A")
				createSameIDTask(t, s.local, "REC-2", "", 2, b, "Task B")
				createSameIDTask(t, s.teammate, "REC-1", "", 1, b, "Task B")
				createSameIDTask(t, s.teammate, "REC-2", "", 2, a, "Task A")
				return nil
			}, nil},
		{"a different task, renumbered with confirmation", sqlite.ImportModeMerge, true,
			func(t *testing.T, s stores) []sqlite.ImportUIDAdoption {
				createSameIDTask(t, s.local, "REC-1", "", 1, taskUID(t), "Local task")
				createSameIDTask(t, s.teammate, "REC-1", "", 1, taskUID(t), "Teammate task")
				return nil
			}, nil},
		{"an adoption beside a renumber awaiting confirmation", sqlite.ImportModeMerge, false,
			func(t *testing.T, s stores) []sqlite.ImportUIDAdoption {
				mine := createSameIDTask(t, s.local, "REC-1", "", 1, backfilledUID(t), "Shared task")
				theirs := createSameIDTask(t, s.teammate, "REC-1", "", 1, backfilledUID(t), "Shared task")
				createSameIDTask(t, s.local, "REC-2", "", 2, taskUID(t), "Local two")
				createSameIDTask(t, s.teammate, "REC-2", "", 2, taskUID(t), "Their two")
				return []sqlite.ImportUIDAdoption{{ID: "REC-1", LocalUID: mine, FileUID: theirs,
					LocalTitle: "Shared task", FileTitle: "Shared task"}}
			}, sqlite.ErrImportConfirmationRequired},
		{"a replace adopts nothing", sqlite.ImportModeReplace, false,
			func(t *testing.T, s stores) []sqlite.ImportUIDAdoption {
				createSameIDTask(t, s.local, "REC-1", "", 1, backfilledUID(t), "A pre-upgrade task")
				createSameIDTask(t, s.teammate, "REC-1", "", 1, backfilledUID(t), "B pre-upgrade task")
				return nil
			}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			s := stores{local: newTestStore(t), teammate: newTestStore(t)}
			want := tt.setup(t, s)
			localUIDs := uidsOf(t, s.local)

			report, result, err := s.local.ImportReconcile(ctx, exportOf(t, s.teammate), sqlite.ImportReconcileOptions{
				Mode: tt.mode, Confirm: tt.confirm,
			})
			require.NotNil(t, report)
			assert.Equal(t, want, report.UIDAdoptions)
			for _, a := range want {
				assert.Contains(t, report.String(), adoptionLine(a))
			}
			assert.Equal(t, len(want) > 0, containsLine(report.String(), "  uids adopted from the file"))
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, want, result.UIDAdoptions, "the result lists the same adoptions")
			for _, a := range want {
				var uid string
				// The uid the merge left at the id, soft-deleted rows included.
				require.NoError(t, s.local.WriteDB().QueryRowContext(ctx,
					`SELECT COALESCE(uid, '') FROM nodes WHERE id = ?`, a.ID).Scan(&uid))
				assert.Equal(t, a.FileUID, uid, "the merge wrote the adoption it reported")
			}
			if tt.mode == sqlite.ImportModeMerge {
				assertOnlyAdoptedUIDsGone(t, s.local, localUIDs, want)
			}
		})
	}
}

// uidsOf returns every uid s holds, soft-deleted nodes included.
func uidsOf(t *testing.T, s *sqlite.Store) []string {
	t.Helper()
	// Every non-empty uid in the store.
	rows, err := s.WriteDB().QueryContext(context.Background(), `SELECT uid FROM nodes WHERE uid IS NOT NULL AND uid <> ''`)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	var uids []string
	for rows.Next() {
		var uid string
		require.NoError(t, rows.Scan(&uid))
		uids = append(uids, uid)
	}
	require.NoError(t, rows.Err())
	return uids
}

// assertOnlyAdoptedUIDsGone checks that, of the uids s held before a merge
// (before), exactly the local uids of the reported adoptions are gone and
// every other one is still held, by one node: a merge never drops a local
// uid without reporting it.
func assertOnlyAdoptedUIDsGone(t *testing.T, s *sqlite.Store, before []string, adopted []sqlite.ImportUIDAdoption) {
	t.Helper()
	gone := make(map[string]bool, len(adopted))
	for _, a := range adopted {
		gone[a.LocalUID] = true
	}
	for _, uid := range before {
		var held int
		// How many nodes hold the uid now, soft-deleted ones included.
		require.NoError(t, s.WriteDB().QueryRowContext(context.Background(),
			`SELECT COUNT(*) FROM nodes WHERE uid = ?`, uid).Scan(&held))
		want := 1
		if gone[uid] {
			want = 0
		}
		assert.Equal(t, want, held, "uid %s", uid)
	}
}

// containsLine reports whether text holds a line that starts with prefix.
func containsLine(text, prefix string) bool {
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

// TestImportReconcileReport_String_AdoptedTitlesDiffer_SaysWhereTheLocalTaskIs
// verifies the report adds, only when an adopted task's two titles differ,
// that the two may be different tasks and where the local one is kept.
func TestImportReconcileReport_String_AdoptedTitlesDiffer_SaysWhereTheLocalTaskIs(t *testing.T) {
	tests := []struct {
		name      string
		fileTitle string
		want      bool
	}{
		{"titles differ", "B pre-upgrade task", true},
		{"titles equal", "A pre-upgrade task", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			local, teammate := newTestStore(t), newTestStore(t)
			createSameIDTask(t, local, "REC-1", "", 1, backfilledUID(t), "A pre-upgrade task")
			createSameIDTask(t, teammate, "REC-1", "", 1, backfilledUID(t), tt.fileTitle)

			report := mergeFile(t, local, exportOf(t, teammate))
			require.Len(t, report.UIDAdoptions, 1)
			out := report.String()
			assert.Equal(t, tt.want, containsLine(out, titlesDifferNote), out)
			if tt.want {
				assert.Contains(t, out, "may be two different tasks created in the same second whose create "+
					"event at least one clone's event log lacks (for example, created before 0.2)")
				assert.Contains(t, out, "the backup taken before the merge holds the local one")
			}
		})
	}
}

// TestImportReconcile_ResidualSameSecondPair_MergeKeepsFileTaskAndReportsIt
// is the MTIX-95.31.4 round-4 residual: two different tasks created in the
// same second, whose create event at least one clone's event log lacks,
// both upgraded more than an hour later, count as one task. The merge takes the file's task
// under the id (the identity rule is unchanged) and its report names both
// titles, so the user can recover the local task from the pre-merge backup.
func TestImportReconcile_ResidualSameSecondPair_MergeKeepsFileTaskAndReportsIt(t *testing.T) {
	ctx := context.Background()
	local, teammate := newTestStore(t), newTestStore(t)
	mine := createSameIDTask(t, local, "REC-1", "", 1, backfilledUID(t), "A pre-upgrade task")
	theirs := createSameIDTask(t, teammate, "REC-1", "", 1, backfilledUID(t), "B pre-upgrade task")

	report := mergeFile(t, local, exportOf(t, teammate))
	assert.Empty(t, report.LocalRenumbers, "the rule treats them as one task")
	node, err := local.GetNode(ctx, "REC-1")
	require.NoError(t, err)
	assert.Equal(t, "B pre-upgrade task", node.Title)
	assert.Equal(t, theirs, node.UID)
	assert.Contains(t, report.String(), adoptionLine(sqlite.ImportUIDAdoption{ID: "REC-1", LocalUID: mine,
		FileUID: theirs, LocalTitle: "A pre-upgrade task", FileTitle: "B pre-upgrade task"}))
	_, err = local.ResolveDisplayPathByUID(ctx, mine)
	require.ErrorIs(t, err, model.ErrNotFound, "the local uid is gone: only the report and the backup name it")
}

// TestImportReconcileReport_String_TwoAdoptionsDifferingFirst_CountsBothAndWarns
// verifies the report of a merge that adopts two uids, the one whose titles
// differ listed first, counts both and still prints the titles-differ note
// (MTIX-95.31.9): the note is not decided by the last adoption alone.
func TestImportReconcileReport_String_TwoAdoptionsDifferingFirst_CountsBothAndWarns(t *testing.T) {
	local, teammate := newTestStore(t), newTestStore(t)
	createSameIDTask(t, local, "REC-1", "", 1, backfilledUID(t), "A pre-upgrade task")
	createSameIDTask(t, teammate, "REC-1", "", 1, backfilledUID(t), "B pre-upgrade task")
	createSameIDTask(t, local, "REC-2", "", 2, backfilledUID(t), "Shared task")
	createSameIDTask(t, teammate, "REC-2", "", 2, backfilledUID(t), "Shared task")

	report := mergeFile(t, local, exportOf(t, teammate))
	require.Len(t, report.UIDAdoptions, 2)
	require.Equal(t, "A pre-upgrade task", report.UIDAdoptions[0].LocalTitle, "the differing adoption comes first")
	out := report.String()
	assert.Contains(t, out, "  uids adopted from the file (the same task under another uid): 2\n")
	assert.True(t, containsLine(out, titlesDifferNote), out)
}
