// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"database/sql"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

var historyTestTime = time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)

// historyFixture is a store with a fixed clock and a raw handle for reading task_history.
type historyFixture struct {
	s    *sqlite.Store
	db   *sql.DB
	path string
}

func newHistoryFixture(t *testing.T) *historyFixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "h.db")
	s, err := sqlite.New(path, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	s.SetClock(func() time.Time { return historyTestTime })
	return &historyFixture{s: s, db: newTestDB(t, path), path: path}
}

// seed creates a root PROJ-1 with children PROJ-1.1 and PROJ-1.2, then clears the history.
func (f *historyFixture) seed(t *testing.T) {
	t.Helper()
	seedCascadeNodes(t, f.s, ccNode(ccRoot, model.StatusOpen), ccNode(ccChild, model.StatusOpen), ccNode(ccChild2, model.StatusOpen))
	_, err := f.db.Exec("DELETE FROM task_history")
	require.NoError(t, err)
}

type historyRow struct {
	uid, actor, origin, kind, field string
	old, new                        sql.NullString
	ts                              string
}

// rows returns the entries for the node id, oldest first.
func (f *historyFixture) rows(t *testing.T, id string) []historyRow {
	t.Helper()
	q, err := f.db.Query(`SELECT h.task_uid, h.actor, h.origin, h.kind, h.field, h.old_value, h.new_value, h.ts
		FROM task_history h JOIN nodes n ON COALESCE(NULLIF(n.uid,''), n.id) = h.task_uid
		WHERE n.id = ? ORDER BY h.entry_id`, id)
	require.NoError(t, err)
	defer func() { require.NoError(t, q.Close()) }()
	var out []historyRow
	for q.Next() {
		var r historyRow
		require.NoError(t, q.Scan(&r.uid, &r.actor, &r.origin, &r.kind, &r.field, &r.old, &r.new, &r.ts))
		out = append(out, r)
	}
	require.NoError(t, q.Err())
	return out
}

func (f *historyFixture) fields(t *testing.T, id string) []string {
	t.Helper()
	var out []string
	for _, r := range f.rows(t, id) {
		out = append(out, r.field)
	}
	sort.Strings(out)
	return out
}

func TestTaskHistory_WritePaths_AppendOneEntryPerChangedField(t *testing.T) {
	ctx := context.Background()
	prio := model.Priority(1)
	tests := []struct {
		name   string
		node   string
		act    func(t *testing.T, f *historyFixture)
		fields []string
	}{
		{"update title and priority", ccRoot, func(t *testing.T, f *historyFixture) {
			require.NoError(t, f.s.UpdateNode(ctx, ccRoot, &store.NodeUpdate{Title: strPtr("renamed"), Priority: &prio}))
		}, []string{"priority", "title"}},
		{"update with identical values", ccRoot, func(t *testing.T, f *historyFixture) {
			require.NoError(t, f.s.UpdateNode(ctx, ccRoot, &store.NodeUpdate{Title: strPtr("node " + ccRoot)}))
		}, nil},
		{"claim", ccRoot, func(t *testing.T, f *historyFixture) {
			require.NoError(t, f.s.ClaimNode(ctx, ccRoot, "agent-7"))
		}, []string{"agent_state", "assignee", "status"}},
		{"unclaim", ccRoot, func(t *testing.T, f *historyFixture) {
			require.NoError(t, f.s.ClaimNode(ctx, ccRoot, "agent-7"))
			_, err := f.db.Exec("DELETE FROM task_history")
			require.NoError(t, err)
			require.NoError(t, f.s.UnclaimNode(ctx, ccRoot, "stop", "agent-7"))
		}, []string{"agent_state", "assignee", "status"}},
		{"transition", ccRoot, func(t *testing.T, f *historyFixture) {
			require.NoError(t, f.s.TransitionStatus(ctx, ccRoot, model.StatusInProgress, "go", "alice"))
		}, []string{"status"}},
		{"idempotent transition", ccRoot, func(t *testing.T, f *historyFixture) {
			require.NoError(t, f.s.TransitionStatus(ctx, ccRoot, model.StatusOpen, "noop", "alice"))
		}, nil},
		{"defer", ccRoot, func(t *testing.T, f *historyFixture) {
			until := historyTestTime.Add(48 * time.Hour)
			require.NoError(t, f.s.DeferNode(ctx, ccRoot, &until, "later", "alice"))
		}, []string{"defer_until", "status"}},
		{"cancel without cascade", ccChild, func(t *testing.T, f *historyFixture) {
			require.NoError(t, f.s.CancelNode(ctx, ccChild, "obsolete", "alice", false))
		}, []string{"status"}},
		{"cancel cascade reaches child", ccChild2, func(t *testing.T, f *historyFixture) {
			require.NoError(t, f.s.CancelNode(ctx, ccRoot, "obsolete", "alice", true))
		}, []string{"status"}},
		{"delete cascade reaches child", ccChild, func(t *testing.T, f *historyFixture) {
			require.NoError(t, f.s.DeleteNode(ctx, ccRoot, true, "alice"))
		}, []string{"deleted_at"}},
		{"undelete cascade restores child", ccChild, func(t *testing.T, f *historyFixture) {
			require.NoError(t, f.s.DeleteNode(ctx, ccRoot, true, "alice"))
			_, err := f.db.Exec("DELETE FROM task_history")
			require.NoError(t, err)
			require.NoError(t, f.s.UndeleteNode(ctx, ccRoot))
		}, []string{"deleted_at"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newHistoryFixture(t)
			f.seed(t)
			tt.act(t, f)
			assert.Equal(t, tt.fields, f.fields(t, tt.node))
			for _, r := range f.rows(t, tt.node) {
				assert.Equal(t, "change", r.kind)
				assert.Equal(t, "local", r.origin)
				assert.Equal(t, "2026-10-13T09:00:00.000000000Z", r.ts)
				assert.NotEmpty(t, r.actor)
			}
		})
	}
}

func TestTaskHistory_Transition_RecordsStructuredValuesAndActor(t *testing.T) {
	f := newHistoryFixture(t)
	f.seed(t)
	require.NoError(t, f.s.TransitionStatus(context.Background(), ccRoot, model.StatusInProgress, "go", "alice"))
	rows := f.rows(t, ccRoot)
	require.Len(t, rows, 1)
	assert.Equal(t, "alice", rows[0].actor)
	assert.Equal(t, `"open"`, rows[0].old.String)
	assert.Equal(t, `"in_progress"`, rows[0].new.String)
}

func TestTaskHistory_CreateNode_RecordsInitialFieldsOncePerField(t *testing.T) {
	f := newHistoryFixture(t)
	require.NoError(t, f.s.CreateNode(context.Background(), ccNode(ccRoot, model.StatusOpen)))
	got := f.fields(t, ccRoot)
	assert.Contains(t, got, "status")
	assert.Contains(t, got, "title")
	seen := map[string]bool{}
	for _, field := range got {
		assert.False(t, seen[field], "field %s recorded twice", field)
		seen[field] = true
	}
	for _, r := range f.rows(t, ccRoot) {
		assert.False(t, r.old.Valid, "create has no previous value: %s", r.field)
	}
}

func TestTaskHistory_FreeTextFields_StoreNoTextOrHash(t *testing.T) {
	secret := "hunter2-secret-text"
	for _, field := range []string{"title", "description", "prompt", "acceptance"} {
		t.Run(field, func(t *testing.T) {
			f := newHistoryFixture(t)
			f.seed(t)
			u := &store.NodeUpdate{}
			switch field {
			case "title":
				u.Title = &secret
			case "description":
				u.Description = &secret
			case "prompt":
				u.Prompt = &secret
			case "acceptance":
				u.Acceptance = &secret
			}
			require.NoError(t, f.s.UpdateNode(context.Background(), ccRoot, u))
			rows := f.rows(t, ccRoot)
			require.Len(t, rows, 1)
			assert.Equal(t, field, rows[0].field)
			assert.False(t, rows[0].old.Valid, "no old text")
			assert.False(t, rows[0].new.Valid, "no new text or hash")
			var dump string
			require.NoError(t, f.db.QueryRow(`SELECT group_concat(entry_id||task_uid||actor||field||COALESCE(old_value,'')||COALESCE(new_value,'')||COALESCE(event_id,''), '|') FROM task_history`).Scan(&dump))
			assert.False(t, strings.Contains(dump, secret))
		})
	}
}

func TestHistoryFieldClassOf_Registry(t *testing.T) {
	tests := []struct {
		field string
		want  model.HistoryFieldClass
	}{
		{"status", model.HistoryFieldStructured}, {"assignee", model.HistoryFieldStructured},
		{"priority", model.HistoryFieldStructured}, {"labels", model.HistoryFieldStructured},
		{"node_type", model.HistoryFieldStructured}, {"issue_type", model.HistoryFieldStructured},
		{"parent", model.HistoryFieldStructured}, {"defer_until", model.HistoryFieldStructured},
		{"title", model.HistoryFieldValueFree}, {"description", model.HistoryFieldValueFree},
		{"prompt", model.HistoryFieldValueFree}, {"acceptance", model.HistoryFieldValueFree},
		{"notes", model.HistoryFieldValueFree}, {"annotations", model.HistoryFieldValueFree},
		{"comments", model.HistoryFieldValueFree}, {"some_future_field", model.HistoryFieldValueFree},
	}
	for _, tt := range tests {
		t.Run(tt.field, func(t *testing.T) {
			assert.Equal(t, tt.want, model.HistoryFieldClassOf(tt.field))
		})
	}
}

func TestTaskHistory_FailedWrite_LeavesNoEntryAndNoChange(t *testing.T) {
	ctx := context.Background()
	t.Run("write on a missing node", func(t *testing.T) {
		f := newHistoryFixture(t)
		f.seed(t)
		require.Error(t, f.s.UpdateNode(ctx, "PROJ-99", &store.NodeUpdate{Title: strPtr("x")}))
		var n int
		require.NoError(t, f.db.QueryRow("SELECT count(*) FROM task_history").Scan(&n))
		assert.Zero(t, n)
	})
	t.Run("history append failure rolls the write back", func(t *testing.T) {
		f := newHistoryFixture(t)
		f.seed(t)
		_, err := f.db.Exec(`CREATE TRIGGER fail_history BEFORE INSERT ON task_history BEGIN SELECT RAISE(ABORT, 'history refused'); END`)
		require.NoError(t, err)
		require.Error(t, f.s.UpdateNode(ctx, ccRoot, &store.NodeUpdate{Title: strPtr("changed")}))
		n, err := f.s.GetNode(ctx, ccRoot)
		require.NoError(t, err)
		assert.Equal(t, "node "+ccRoot, n.Title, "the write must roll back with its history")
	})
}

func TestTaskHistory_Migration_IsAdditiveAndIdempotent(t *testing.T) {
	f := newHistoryFixture(t)
	f.seed(t)
	require.NoError(t, f.s.UpdateNode(context.Background(), ccRoot, &store.NodeUpdate{Title: strPtr("kept")}))
	require.NoError(t, f.s.Close())

	// Simulate a pre-migration store: the table is absent.
	raw := newTestDB(t, f.path)
	_, err := raw.Exec("DROP TABLE task_history")
	require.NoError(t, err)
	s2, err := sqlite.New(f.path, slog.Default())
	require.NoError(t, err, "a pre-migration store must open")
	var n int
	require.NoError(t, raw.QueryRow("SELECT count(*) FROM task_history").Scan(&n))
	assert.Zero(t, n)
	require.NoError(t, s2.UpdateNode(context.Background(), ccRoot, &store.NodeUpdate{Title: strPtr("again")}))
	require.NoError(t, s2.Close())

	s3, err := sqlite.New(f.path, slog.Default())
	require.NoError(t, err, "reopening must be idempotent")
	require.NoError(t, s3.Close())
	require.NoError(t, raw.QueryRow("SELECT count(*) FROM task_history").Scan(&n))
	assert.Equal(t, 1, n, "reopen keeps existing history")
	f.s = s3 // the cleanup closes the first store; Close is idempotent
}
