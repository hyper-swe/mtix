// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests for MTIX-95.31.1 (FR-7.8, FR-15.2): a replace import writes back
// every exported column, annotations included, so an export then replace
// import round trip leaves the store identical. Written red-first.
package sqlite_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// allNodeColumnsSQL selects every column of the nodes table, each under its
// own name, so rows.Columns() can be checked against the live schema. An
// empty JSON array may be stored as NULL, as an empty string, as null or as
// [] (every reader treats them alike), so the JSON-array columns are
// normalised to NULL.
const allNodeColumnsSQL = `SELECT id, parent_id, depth, seq, project,
	title, description, prompt, acceptance,
	node_type, issue_type, priority,
	NULLIF(NULLIF(NULLIF(labels, '[]'), 'null'), '') AS labels,
	status, previous_status, progress, assignee, creator, agent_state,
	created_at, updated_at, closed_at, defer_until,
	estimate_min, actual_min, weight, content_hash,
	NULLIF(NULLIF(NULLIF(code_refs, '[]'), 'null'), '') AS code_refs,
	NULLIF(NULLIF(NULLIF(commit_refs, '[]'), 'null'), '') AS commit_refs,
	NULLIF(NULLIF(NULLIF(annotations, '[]'), 'null'), '') AS annotations,
	invalidated_at, invalidated_by, invalidation_reason,
	NULLIF(NULLIF(NULLIF(activity, '[]'), 'null'), '') AS activity,
	deleted_at, deleted_by, metadata, session_id, uid
	FROM nodes ORDER BY id`

// nodeColumnRows reads every column of every node row. It fails when the
// query no longer names every column of the live schema, so a column added
// later cannot escape the round-trip comparison.
func nodeColumnRows(t *testing.T, s *sqlite.Store) []map[string]any {
	t.Helper()
	rows, err := s.ReadDB().QueryContext(context.Background(), allNodeColumnsSQL)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	cols, err := rows.Columns()
	require.NoError(t, err)
	require.Equal(t, nodesTableColumns(t, s), cols, "allNodeColumnsSQL must select every nodes column")

	var out []map[string]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		require.NoError(t, rows.Scan(ptrs...))
		row := make(map[string]any, len(cols))
		for i, c := range cols {
			row[c] = vals[i]
		}
		out = append(out, row)
	}
	require.NoError(t, rows.Err())
	return out
}

// seedRoundTripStore builds a store exercising every exported column: a
// soft-deleted node with every column set, a live annotated node, a child
// claimed by an agent, a dependency and a session.
func seedRoundTripStore(t *testing.T) *sqlite.Store {
	t.Helper()
	ctx := context.Background()
	s := newTestStore(t)
	createEveryColumnNode(t, s, "COL-1", 1)
	createAnnotatedNode(t, s, "COL-2", 2)
	ts := columnTestTime()
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "COL-2.1", ParentID: "COL-2", Project: "COL", Depth: 1, Seq: 1,
		Title: "Child", NodeType: model.NodeTypeStory, Priority: model.PriorityMedium,
		Status: model.StatusOpen, Weight: 1.0, ContentHash: "hash-child",
		CreatedAt: ts, UpdatedAt: ts,
	}))
	require.NoError(t, s.ClaimNode(ctx, "COL-2.1", "agent-rt"))
	require.NoError(t, s.AddDependency(ctx, &model.Dependency{
		FromID: "COL-2.1", ToID: "COL-2", DepType: model.DepTypeRelated, CreatedAt: ts,
	}))
	_, err := s.WriteDB().ExecContext(ctx,
		`INSERT INTO sessions (id, agent_id, project, started_at, ended_at, status, summary)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"session-rt", "agent-rt", "COL", ts.Format(time.RFC3339),
		ts.Add(time.Hour).Format(time.RFC3339), "ended", "done for the day")
	require.NoError(t, err)
	return s
}

// TestImport_ReplaceRoundTrip_StoreIdenticalForExportedColumns verifies that
// an export followed by a replace import into another store (which held
// other data) leaves every nodes column identical, and that re-exporting
// the imported store reproduces the original export and checksum
// (MTIX-95.31.1).
func TestImport_ReplaceRoundTrip_StoreIdenticalForExportedColumns(t *testing.T) {
	ctx := context.Background()
	src := seedRoundTripStore(t)
	data, err := src.Export(ctx, "", "")
	require.NoError(t, err)
	wantNodes, err := json.Marshal(data.Nodes)
	require.NoError(t, err)

	dst := newTestStore(t)
	createAnnotatedNode(t, dst, "OLD-1", 1)
	_, err = dst.Import(ctx, data, sqlite.ImportModeReplace, false)
	require.NoError(t, err)

	assert.Equal(t, nodeColumnRows(t, src), nodeColumnRows(t, dst))

	again, err := dst.Export(ctx, "", "")
	require.NoError(t, err)
	gotNodes, err := json.Marshal(again.Nodes)
	require.NoError(t, err)
	assert.JSONEq(t, string(wantNodes), string(gotNodes))
	assert.Equal(t, data.Checksum, again.Checksum)
	assert.Equal(t, data.Dependencies, again.Dependencies)
	assert.Equal(t, data.Sessions, again.Sessions)
}

// TestImport_ReplaceRoundTrip_ThroughFile_AnnotationsAndActivityPreserved
// verifies the same round trip through the bytes of a tasks.json file: the
// node read back after import has every annotation and activity entry it
// had (MTIX-95.31.1).
func TestImport_ReplaceRoundTrip_ThroughFile_AnnotationsAndActivityPreserved(t *testing.T) {
	ctx := context.Background()
	src := seedRoundTripStore(t)
	data, err := src.Export(ctx, "", "")
	require.NoError(t, err)
	fromFile := editExport(t, data, func(map[string]any) {})

	dst := newTestStore(t)
	_, err = dst.Import(ctx, fromFile, sqlite.ImportModeReplace, false)
	require.NoError(t, err)

	for _, id := range []string{"COL-2", "COL-2.1"} {
		want, getErr := src.GetNode(ctx, id)
		require.NoError(t, getErr)
		got, getErr := dst.GetNode(ctx, id)
		require.NoError(t, getErr)
		wantJSON, _ := json.Marshal(want)
		gotJSON, _ := json.Marshal(got)
		assert.JSONEq(t, string(wantJSON), string(gotJSON), "node %s differs after the round trip", id)

		wantAct, actErr := src.GetActivity(ctx, id, 0, 0)
		require.NoError(t, actErr)
		gotAct, actErr := dst.GetActivity(ctx, id, 0, 0)
		require.NoError(t, actErr)
		require.NotEmpty(t, wantAct)
		wantActJSON, _ := json.Marshal(wantAct)
		gotActJSON, _ := json.Marshal(gotAct)
		assert.JSONEq(t, string(wantActJSON), string(gotActJSON), "activity of %s differs", id)
	}
	assert.Len(t, annotationSummary(t, dst, "COL-1"), 2, "a soft-deleted node keeps its annotations")
}
