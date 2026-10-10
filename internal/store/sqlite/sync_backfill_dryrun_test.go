// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests legacy backfill accounting and dry-run purity using owned migrated stores.
package sqlite_test

import (
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

var backfillCreated = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
var backfillUpdated = backfillCreated.Add(24 * time.Hour)
var backfillCommentTime = backfillCreated.Add(2 * time.Hour)

func backfillBehaviorStore(t *testing.T) *sqlite.Store {
	t.Helper()
	t.Setenv(sqlite.AuthorIDEnv, "")
	s := newTestStore(t)
	s.SetClock(func() time.Time { return backfillUpdated })
	require.NoError(t, s.SetDefaultAuthor(t.Context(), "fixture-default"))
	return s
}

func backfillBehaviorNode(seq int) *model.Node {
	return &model.Node{ID: fmt.Sprintf("TEST-%d", seq), Project: "TEST", Seq: seq, Title: fmt.Sprintf("Legacy node %d", seq),
		Status: model.StatusOpen, NodeType: model.NodeTypeForDepth(0), Priority: model.PriorityMedium, Weight: 1,
		Creator: "legacy-owner", CreatedAt: backfillCreated, UpdatedAt: backfillUpdated}
}

func clearBackfillJournal(t *testing.T, s *sqlite.Store) {
	t.Helper()
	// Only this owned fixture models pre-sync data; its canonical rows stay intact.
	_, err := s.WriteDB().ExecContext(t.Context(), "DELETE FROM sync_events")
	require.NoError(t, err)
}

func backfillRawRows(t *testing.T, db *sql.DB, query string) [][]any {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), query)
	require.NoError(t, err)
	defer rows.Close()
	columns, err := rows.Columns()
	require.NoError(t, err)
	result := [][]any{}
	for rows.Next() {
		row := make([]any, len(columns))
		targets := make([]any, len(columns))
		for i := range row {
			targets[i] = &row[i]
		}
		require.NoError(t, rows.Scan(targets...))
		for i, value := range row {
			if b, ok := value.([]byte); ok {
				row[i] = string(b)
			}
		}
		result = append(result, row)
	}
	require.NoError(t, rows.Err())
	return result
}

func backfillSourceState(t *testing.T, s *sqlite.Store) map[string][][]any {
	t.Helper()
	return map[string][][]any{
		"nodes":        backfillRawRows(t, s.WriteDB(), "SELECT * FROM nodes ORDER BY id"),
		"dependencies": backfillRawRows(t, s.WriteDB(), "SELECT * FROM dependencies ORDER BY from_id,to_id,dep_type"),
	}
}

func backfillAllState(t *testing.T, s *sqlite.Store) map[string][][]any {
	t.Helper()
	state := backfillSourceState(t, s)
	state["events"] = backfillRawRows(t, s.WriteDB(), "SELECT * FROM sync_events ORDER BY rowid")
	state["meta"] = backfillRawRows(t, s.WriteDB(), "SELECT * FROM meta ORDER BY key")
	return state
}

func seedBackfillRich(t *testing.T, s *sqlite.Store) {
	t.Helper()
	node := backfillBehaviorNode(1)
	node.Description = "Legacy description"
	node.Prompt = "Legacy prompt"
	node.Acceptance = "Legacy acceptance"
	node.Assignee = "legacy-worker"
	node.Status = model.StatusDone
	require.NoError(t, s.CreateNode(t.Context(), node))
	require.NoError(t, s.CreateNode(t.Context(), backfillBehaviorNode(2)))
	require.NoError(t, s.CreateNode(t.Context(), backfillBehaviorNode(3)))
	require.NoError(t, s.DeleteNode(t.Context(), "TEST-3", false, "legacy-owner"))
	require.NoError(t, s.SetAnnotations(t.Context(), "TEST-1", []model.Annotation{
		{ID: "source-a", Text: "First legacy comment", Author: "source-author", CreatedAt: backfillCommentTime},
		{ID: "source-b", Text: "Second legacy comment", Author: "source-author", CreatedAt: backfillCommentTime},
	}))
	for _, dep := range []*model.Dependency{
		{FromID: "TEST-1", ToID: "TEST-2", DepType: model.DepTypeRelated, CreatedBy: "named-owner"},
		{FromID: "TEST-2", ToID: "TEST-1", DepType: model.DepTypeDiscoveredFrom},
	} {
		require.NoError(t, s.AddDependency(t.Context(), dep))
	}
	clearBackfillJournal(t, s)
}

func backfillEventCounts(t *testing.T, s *sqlite.Store) map[model.OpType]int {
	t.Helper()
	events, err := s.ReadPendingEvents(t.Context(), 1000)
	require.NoError(t, err)
	counts := map[model.OpType]int{}
	for _, event := range events {
		counts[event.OpType]++
	}
	return counts
}

func TestBackfillDryRun_LegacyFixtures_CountsMatchEmissionAndPreserveSource(t *testing.T) {
	tests := []struct {
		name string
		seed func(*testing.T, *sqlite.Store)
		want sqlite.BackfillResult
	}{
		{"empty", func(*testing.T, *sqlite.Store) {}, sqlite.BackfillResult{}},
		{"sparse", func(t *testing.T, s *sqlite.Store) {
			require.NoError(t, s.CreateNode(t.Context(), backfillBehaviorNode(1)))
			clearBackfillJournal(t, s)
		}, sqlite.BackfillResult{NodeCount: 1, CreateEvents: 1, TotalEvents: 1}},
		{"populated", seedBackfillRich, sqlite.BackfillResult{NodeCount: 2, CreateEvents: 2, UpdateFieldEvents: 4, TransitionEvents: 1, AnnotateEvents: 2, LinkDepEvents: 2, TotalEvents: 11}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := backfillBehaviorStore(t)
			tt.seed(t, s)
			before := backfillAllState(t, s)
			dry, err := s.BackfillDryRun(t.Context())
			require.NoError(t, err)
			require.Equal(t, tt.want, dry)
			require.Equal(t, before, backfillAllState(t, s))
			written, err := s.Backfill(t.Context(), false)
			require.NoError(t, err)
			require.Equal(t, dry, written)
			source := backfillSourceState(t, s)
			require.Equal(t, before["nodes"], source["nodes"])
			require.Equal(t, before["dependencies"], source["dependencies"])
			counts := backfillEventCounts(t, s)
			require.Equal(t, dry.CreateEvents, counts[model.OpCreateNode])
			require.Equal(t, dry.UpdateFieldEvents, counts[model.OpUpdateField])
			require.Equal(t, dry.TransitionEvents, counts[model.OpTransitionStatus])
			require.Equal(t, dry.AnnotateEvents, counts[model.OpComment])
			require.Equal(t, dry.LinkDepEvents, counts[model.OpLinkDep])
			total, err := s.CountSyncEvents(t.Context())
			require.NoError(t, err)
			require.Equal(t, dry.TotalEvents, total)
		})
	}
}

func TestBackfillDryRun_LegacyOptionalValues_CountWithoutMutation(t *testing.T) {
	tests := []struct {
		name                  string
		optional, annotations any
		comments              int
	}{
		{"SQL null", nil, nil, 0}, {"empty", "", "", 0}, {"JSON null", "", "null", 0}, {"empty list", "", "[]", 0},
		{"mixed annotation text", "", `[{"text":"Counted"},{"text":""},{"text":7},{"text":null}]`, 1},
		{"malformed annotation", "", "[", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := backfillBehaviorStore(t)
			require.NoError(t, s.CreateNode(t.Context(), backfillBehaviorNode(1)))
			_, err := s.WriteDB().ExecContext(t.Context(), "UPDATE nodes SET description=?,prompt=?,acceptance=?,assignee=?,annotations=? WHERE id=?", tt.optional, tt.optional, tt.optional, tt.optional, tt.annotations, "TEST-1")
			require.NoError(t, err)
			clearBackfillJournal(t, s)
			before := backfillAllState(t, s)
			got, err := s.BackfillDryRun(t.Context())
			require.NoError(t, err)
			require.Equal(t, sqlite.BackfillResult{NodeCount: 1, CreateEvents: 1, AnnotateEvents: tt.comments, TotalEvents: 1 + tt.comments}, got)
			require.Equal(t, before, backfillAllState(t, s))
		})
	}
}
