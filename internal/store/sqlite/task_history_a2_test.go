// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
)

// blockPair seeds a blocker (ccRoot) and a dependent (ccOutside) and links
// them with a blocks edge, then clears the history of the setup.
func (f *historyFixture) blockPair(t *testing.T) {
	t.Helper()
	seedCascadeNodes(t, f.s, ccNode(ccRoot, model.StatusOpen), ccNode(ccOutside, model.StatusOpen))
	f.clearHistory(t)
}

func (f *historyFixture) link(t *testing.T) {
	t.Helper()
	require.NoError(t, f.s.AddDependency(context.Background(), &model.Dependency{
		FromID: ccRoot, ToID: ccOutside, DepType: model.DepTypeBlocks, CreatedBy: "alice"}))
}

// onlyRow returns the single history row of the node for the field.
func (f *historyFixture) onlyRow(t *testing.T, id, field string) historyRow {
	t.Helper()
	var found []historyRow
	for _, r := range f.rows(t, id) {
		if r.field == field {
			found = append(found, r)
		}
	}
	require.Len(t, found, 1, "exactly one %s entry for %s", field, id)
	return found[0]
}

func TestTaskHistory_DependencyAutoBlockAndUnblock_RecordStatusWithOriginSystem(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name  string
		act   func(t *testing.T, f *historyFixture)
		old   string
		new   string
		actor string
	}{
		{"add blocks edge auto-blocks the target", func(t *testing.T, f *historyFixture) { f.link(t) }, `"open"`, `"blocked"`, "alice"},
		{"remove edge auto-unblocks the target", func(t *testing.T, f *historyFixture) {
			f.link(t)
			f.clearHistory(t)
			require.NoError(t, f.s.RemoveDependency(ctx, ccRoot, ccOutside, model.DepTypeBlocks))
		}, `"blocked"`, `"open"`, historyEnvAuthor},
		{"finishing the blocker unblocks the dependent", func(t *testing.T, f *historyFixture) {
			f.link(t)
			require.NoError(t, f.s.TransitionStatus(ctx, ccRoot, model.StatusInProgress, "go", "bob"))
			f.clearHistory(t)
			require.NoError(t, f.s.TransitionStatus(ctx, ccRoot, model.StatusDone, "done", "bob"))
		}, `"blocked"`, `"open"`, "bob"},
		{"cancelling the blocker unblocks the dependent", func(t *testing.T, f *historyFixture) {
			f.link(t)
			f.clearHistory(t)
			require.NoError(t, f.s.CancelNode(ctx, ccRoot, "obsolete", "bob", false))
		}, `"blocked"`, `"open"`, "bob"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newHistoryFixture(t)
			f.blockPair(t)
			tt.act(t, f)
			row := f.onlyRow(t, ccOutside, "status")
			assert.Equal(t, tt.old, row.old.String)
			assert.Equal(t, tt.new, row.new.String)
			assert.Equal(t, "system", row.origin, "an automatic status change has origin system")
			assert.Equal(t, tt.actor, row.actor)
			assert.Equal(t, []string{"status"}, f.fields(t, ccOutside), "previous_status is bookkeeping, not a field")
		})
	}
}

func TestTaskHistory_Annotations_RecordValueFreeEntry(t *testing.T) {
	secret := "annotation-secret-text"
	ctx := context.Background()
	ann := model.Annotation{ID: "01A", Author: "carol", Text: secret}
	tests := []struct {
		name  string
		act   func(t *testing.T, f *historyFixture)
		actor string
	}{
		{"append", func(t *testing.T, f *historyFixture) { require.NoError(t, f.s.AppendAnnotation(ctx, ccRoot, ann)) }, historyEnvAuthor},
		{"set", func(t *testing.T, f *historyFixture) {
			require.NoError(t, f.s.SetAnnotations(ctx, ccRoot, []model.Annotation{ann}))
		}, historyEnvAuthor},
		{"resolve", func(t *testing.T, f *historyFixture) {
			require.NoError(t, f.s.AppendAnnotation(ctx, ccRoot, ann))
			f.clearHistory(t)
			require.NoError(t, f.s.ResolveAnnotation(ctx, ccRoot, "01A", true))
		}, historyEnvAuthor},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newHistoryFixture(t)
			f.seed(t)
			tt.act(t, f)
			row := f.onlyRow(t, ccRoot, "annotations")
			assert.False(t, row.old.Valid, "no annotation text")
			assert.False(t, row.new.Valid, "no annotation text or hash")
			assert.Equal(t, "local", row.origin)
			assert.Equal(t, tt.actor, row.actor)
			var dump string
			require.NoError(t, f.db.QueryRow(`SELECT group_concat(entry_id||task_uid||actor||field||COALESCE(old_value,'')||COALESCE(new_value,''), '|') FROM task_history`).Scan(&dump))
			assert.NotContains(t, dump, secret)
		})
	}
}

func TestTaskHistory_ProgressRollup_RecordsNothing(t *testing.T) {
	f := newHistoryFixture(t)
	f.seed(t)
	ctx := context.Background()
	require.NoError(t, f.s.UpdateProgress(ctx, ccRoot, 0.5))
	require.NoError(t, f.s.TransitionStatus(ctx, ccChild, model.StatusInProgress, "go", "bob"))
	require.NoError(t, f.s.TransitionStatus(ctx, ccChild, model.StatusDone, "done", "bob"))
	for _, r := range f.rows(t, ccRoot) {
		assert.NotEqual(t, "progress", r.field)
	}
	assert.Empty(t, f.fields(t, ccRoot), "progress is derived and excluded from the history")
}

func TestTaskHistory_NullAndEmpty_AreTheSameEmptyValue(t *testing.T) {
	f := newHistoryFixture(t)
	f.seed(t)
	ctx := context.Background()
	_, err := f.db.Exec(`UPDATE nodes SET assignee = NULL WHERE id = ?`, ccRoot)
	require.NoError(t, err)
	// An update that stores '' over NULL changes nothing observable: no entry.
	empty := ""
	require.NoError(t, f.s.UpdateNode(ctx, ccRoot, &store.NodeUpdate{Assignee: &empty}))
	assert.Empty(t, f.fields(t, ccRoot))
}

// TestTaskHistory_EmptyValues_RenderAsOneCanonicalNull pins the encoding: an
// assignee cleared to NULL (unclaim) and one cleared to ” (an update) both
// record the JSON text null.
func TestTaskHistory_EmptyValues_RenderAsOneCanonicalNull(t *testing.T) {
	ctx := context.Background()
	clears := map[string]func(f *historyFixture) error{
		"cleared to NULL": func(f *historyFixture) error { return f.s.UnclaimNode(ctx, ccRoot, "stop", "agent-7") },
		"cleared to empty string": func(f *historyFixture) error {
			return f.s.UpdateNode(ctx, ccRoot, &store.NodeUpdate{Assignee: strPtr("")})
		},
	}
	for name, clearIt := range clears {
		t.Run(name, func(t *testing.T) {
			f := newHistoryFixture(t)
			f.seed(t)
			require.NoError(t, f.s.ClaimNode(ctx, ccRoot, "agent-7"))
			f.clearHistory(t)
			require.NoError(t, clearIt(f))
			row := f.onlyRow(t, ccRoot, "assignee")
			assert.Equal(t, `"agent-7"`, row.old.String)
			assert.True(t, row.new.Valid)
			assert.Equal(t, "null", row.new.String, "an emptied value is the one canonical null")
		})
	}
}
