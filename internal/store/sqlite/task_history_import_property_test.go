// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"fmt"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// TestTaskHistory_RandomImports_NeverMoveHistoryOntoAnUnpairedTask runs many
// random imports and checks the rule of task_history_import_nomove_test.go on
// each: no earlier entry changes key or is deleted, every entry the importer
// writes matches what its own key's task held (structured Old and New values,
// and a real text change for the value-free fields), and every task new to the
// store gets its creation entries. The generator
// moves tasks between ids, adds unrelated tasks, uses titles that collide, makes
// local tasks uid-less, and puts one uid into two file slots.
func TestTaskHistory_RandomImports_NeverMoveHistoryOntoAnUnpairedTask(t *testing.T) {
	succeeded := 0
	for seed := int64(1); seed <= 80; seed++ {
		if runRandomImport(t, seed) {
			succeeded++
		}
	}
	assert.GreaterOrEqual(t, succeeded, 25, "most random imports must apply, or the property proves nothing")
}

type fileSlot struct {
	id, title, uid, owner, kind string
}

type propLocal struct {
	uid, tag string
	backfill bool
}

var propTitles = []string{"T1", "T2"}

func runRandomImport(t *testing.T, seed int64) (applied bool) {
	t.Helper()
	rng := rand.New(rand.NewSource(seed)) //nolint:gosec // a seeded test generator
	local, mate := newTestStore(t), newTestStore(t)
	locals := seedPropLocals(t, rng, local)
	slots := planPropFile(t, rng, locals)
	for _, s := range slots {
		createSameIDTask(t, mate, s.id, "", int(s.id[len(s.id)-1]-'0'), s.uid, s.title)
		if s.kind == "other" {
			_, err := mate.WriteDB().Exec(`UPDATE nodes SET created_at = '2027-01-01T00:00:00Z' WHERE id = ?`, s.id)
			require.NoError(t, err)
		}
	}
	file := exportOf(t, mate)
	if len(file.Nodes) > 1 && rng.Intn(4) == 0 { // one uid in two file slots
		file.Nodes[1].UID = file.Nodes[0].UID
		require.NoError(t, sqlite.RecomputeExportChecksum(file))
	}
	before := snapImport(t, local.WriteDB())
	t.Setenv("MTIX_AUTHOR_ID", "importer")
	if !importRandom(t, rng, local, file) {
		return false
	}
	assertImportMovedNothing(t, local.WriteDB(), before)
	return true
}

// seedPropLocals creates three local tasks, each with history tagged for its
// owner; sometimes one becomes a legacy uid-less task keyed by its id.
func seedPropLocals(t *testing.T, rng *rand.Rand, local *sqlite.Store) []propLocal {
	t.Helper()
	locals := make([]propLocal, 3)
	for i := range locals {
		id := fmt.Sprintf("REC-%d", i+1)
		locals[i] = propLocal{tag: fmt.Sprintf("own-%d", i+1), backfill: rng.Intn(2) == 0}
		if locals[i].backfill {
			locals[i].uid = backfilledUID(t)
		} else {
			locals[i].uid = taskUID(t)
		}
		t.Setenv("MTIX_AUTHOR_ID", locals[i].tag) // the creation entries carry the owner's tag too
		createSameIDTask(t, local, id, "", i+1, locals[i].uid, propTitles[rng.Intn(len(propTitles))])
		who := "who-" + locals[i].tag
		require.NoError(t, local.UpdateNode(context.Background(), id, &store.NodeUpdate{Assignee: &who}))
	}
	if rng.Intn(4) == 0 {
		k := rng.Intn(len(locals))
		id := fmt.Sprintf("REC-%d", k+1)
		_, err := local.WriteDB().Exec(`UPDATE task_history SET task_uid = ? WHERE task_uid = ?`, id, locals[k].uid)
		require.NoError(t, err)
		_, err = local.WriteDB().Exec(`UPDATE nodes SET uid = NULL WHERE id = ?`, id)
		require.NoError(t, err)
		locals[k].uid = ""
	}
	return locals
}

// planPropFile lays out the file: the same tasks (possibly moved), adoptable
// tasks and unrelated new ones, with the owner the generator knows for each.
func planPropFile(t *testing.T, rng *rand.Rand, locals []propLocal) []fileSlot {
	t.Helper()
	var slots []fileSlot
	used := map[int]bool{}
	for n, idn := range rng.Perm(4)[:1+rng.Intn(4)] {
		s := fileSlot{id: fmt.Sprintf("REC-%d", idn+1), title: propTitles[rng.Intn(len(propTitles))]}
		i := rng.Intn(len(locals))
		switch {
		case n%3 == 0 && locals[i].uid != "" && !used[i]:
			s.kind, s.uid, s.owner, used[i] = "same", locals[i].uid, locals[i].tag, true
		case n%3 == 1 && locals[i].backfill && locals[i].uid != "" && !used[i] && idn == i:
			s.kind, s.uid, s.owner, used[i] = "adopt", taskUID(t), locals[i].tag, true
		default:
			s.kind, s.uid = "other", taskUID(t)
		}
		slots = append(slots, s)
	}
	return slots
}

// importRandom runs a replace or a reconciling merge; it reports whether the import applied.
func importRandom(t *testing.T, rng *rand.Rand, local *sqlite.Store, file *sqlite.ExportData) bool {
	t.Helper()
	var err error
	if rng.Intn(2) == 0 {
		_, err = local.Import(context.Background(), file, sqlite.ImportModeReplace, true)
	} else {
		_, _, err = local.ImportReconcile(context.Background(), file, sqlite.ImportReconcileOptions{
			Mode: sqlite.ImportModeMerge, Confirm: true, Workflow: sqlite.WorkflowResolution{Prefer: sqlite.WorkflowTheirs}})
	}
	return err == nil
}
