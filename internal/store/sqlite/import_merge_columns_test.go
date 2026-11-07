// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// columnEdit changes one column of a file's node. skip says why a column has no
// edit that this test can judge (it is the task's identity or derived from
// other columns); a column with neither an edit nor a skip fails the test.
type columnEdit struct {
	edit func(d *sqlite.ExportData, i int)
	skip string
	// view reads the value to compare on the importer and the peer; the
	// column itself when nil.
	view func(t *testing.T, s *sqlite.Store) string
}

func annotationsView(t *testing.T, s *sqlite.Store) string {
	t.Helper()
	var raw string
	// A task that never had an annotation stores NULL, which reads as none.
	require.NoError(t, s.WriteDB().QueryRow(`SELECT COALESCE(annotations, '') FROM nodes WHERE id = 'REC-1'`).Scan(&raw))
	var list []model.Annotation
	if raw != "" {
		require.NoError(t, json.Unmarshal([]byte(raw), &list))
	}
	out := ""
	for _, a := range list {
		out += a.Author + ":" + a.Text + ";"
	}
	return out
}

// columnEdits has one entry per column of the nodes table. TestMergeColumns_*
// reads pragma_table_info, so a column the schema gains fails there until an
// entry is added, and the journaled/local verdict is then checked on a peer.
func columnEdits() map[string]columnEdit {
	est, act := 30, 40
	return map[string]columnEdit{
		"id":           {skip: "identity: another id is another task"},
		"content_hash": {skip: "derived from title, description, prompt, acceptance and labels"},
		"project":      {edit: func(d *sqlite.ExportData, i int) { d.Nodes[i].Project = "OTHER" }},
		"parent_id":    {edit: func(d *sqlite.ExportData, i int) { d.Nodes[i].ParentID = "REC-2" }},
		"depth":        {edit: func(d *sqlite.ExportData, i int) { d.Nodes[i].Depth = 3 }},
		"seq":          {edit: func(d *sqlite.ExportData, i int) { d.Nodes[i].Seq = 7 }},
		"node_type":    {edit: func(d *sqlite.ExportData, i int) { d.Nodes[i].NodeType = "micro" }},
		"uid":          {edit: func(d *sqlite.ExportData, i int) { d.Nodes[i].UID = "01ARZ3NDEKTSV4RRFFQ69G5FAV" }},
		"title":        {edit: func(d *sqlite.ExportData, i int) { d.Nodes[i].Title = "Edited title" }},
		"description":  {edit: func(d *sqlite.ExportData, i int) { d.Nodes[i].Description = "Edited description" }},
		"prompt":       {edit: func(d *sqlite.ExportData, i int) { d.Nodes[i].Prompt = "Edited prompt" }},
		"acceptance":   {edit: func(d *sqlite.ExportData, i int) { d.Nodes[i].Acceptance = "Edited acceptance" }},
		"issue_type":   {edit: func(d *sqlite.ExportData, i int) { d.Nodes[i].IssueType = "bug" }},
		"priority":     {edit: func(d *sqlite.ExportData, i int) { d.Nodes[i].Priority = 1 }},
		"labels":       {edit: func(d *sqlite.ExportData, i int) { d.Nodes[i].Labels = `["edited"]` }},
		"assignee":     {edit: func(d *sqlite.ExportData, i int) { d.Nodes[i].Assignee = "bob" }},
		"agent_state":  {edit: func(d *sqlite.ExportData, i int) { d.Nodes[i].AgentState = string(model.AgentStateStuck) }},
		"status":       {edit: func(d *sqlite.ExportData, i int) { d.Nodes[i].Status = string(model.StatusInProgress) }},
		"defer_until":  {edit: func(d *sqlite.ExportData, i int) { d.Nodes[i].DeferUntil = "2027-05-05T00:00:00Z" }},
		"deleted_at": {edit: func(d *sqlite.ExportData, i int) {
			d.Nodes[i].DeletedAt, d.Nodes[i].DeletedBy = "2026-01-02T00:00:00Z", "bob"
		}},
		"annotations": {edit: func(d *sqlite.ExportData, i int) {
			d.Nodes[i].Annotations = []model.Annotation{{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", Author: "bob", Text: "note", CreatedAt: sameIDTime}}
		}, view: annotationsView},
		"creator":             {edit: func(d *sqlite.ExportData, i int) { d.Nodes[i].Creator = "zed" }},
		"weight":              {edit: func(d *sqlite.ExportData, i int) { d.Nodes[i].Weight = 2.5 }},
		"estimate_min":        {edit: func(d *sqlite.ExportData, i int) { d.Nodes[i].EstimateMin = &est }},
		"actual_min":          {edit: func(d *sqlite.ExportData, i int) { d.Nodes[i].ActualMin = &act }},
		"code_refs":           {edit: func(d *sqlite.ExportData, i int) { d.Nodes[i].CodeRefs = []model.CodeRef{{File: "a.go"}} }},
		"commit_refs":         {edit: func(d *sqlite.ExportData, i int) { d.Nodes[i].CommitRefs = []string{"abc123"} }},
		"metadata":            {edit: func(d *sqlite.ExportData, i int) { d.Nodes[i].Metadata = `{"k":1}` }},
		"session_id":          {edit: func(d *sqlite.ExportData, i int) { d.Nodes[i].SessionID = "sess-1" }},
		"invalidated_at":      {edit: func(d *sqlite.ExportData, i int) { d.Nodes[i].InvalidatedAt = "2026-01-01T00:00:00Z" }},
		"invalidated_by":      {edit: func(d *sqlite.ExportData, i int) { d.Nodes[i].InvalidatedBy = "bob" }},
		"invalidation_reason": {edit: func(d *sqlite.ExportData, i int) { d.Nodes[i].InvalidationReason = "stale" }},
		"previous_status":     {edit: func(d *sqlite.ExportData, i int) { d.Nodes[i].PreviousStatus = "in_progress" }},
		"deleted_by":          {edit: func(d *sqlite.ExportData, i int) { d.Nodes[i].DeletedBy = "carol" }},
		"activity": {edit: func(d *sqlite.ExportData, i int) {
			d.Nodes[i].Activity = []model.ActivityEntry{{ID: "act-1", Type: model.ActivityTypeComment, Author: "carol", Text: "x"}}
		}},
		"created_at": {edit: func(d *sqlite.ExportData, i int) { d.Nodes[i].CreatedAt = "2025-01-01T00:00:00Z" }},
		"updated_at": {edit: func(d *sqlite.ExportData, i int) { d.Nodes[i].UpdatedAt = "2026-05-06T07:08:09Z" }},
		"closed_at":  {edit: func(d *sqlite.ExportData, i int) { d.Nodes[i].ClosedAt = "2026-01-02T00:00:00Z" }},
		"progress":   {edit: func(d *sqlite.ExportData, i int) { d.Nodes[i].Progress = 0.5 }},
	}
}

func schemaColumns(t *testing.T) []string {
	t.Helper()
	s := newTestStore(t)
	rows, err := s.WriteDB().Query(`SELECT name FROM pragma_table_info('nodes')`)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	var out []string
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		out = append(out, name)
	}
	require.NoError(t, rows.Err())
	return out
}

func mergedColumn(t *testing.T, s *sqlite.Store, col string, e columnEdit) string {
	t.Helper()
	if e.view != nil {
		return e.view(t, s)
	}
	return columnOf(t, s, col)
}

// TestMergeColumns_EveryColumnOfTheSchema_JournaledArrivesOnAPeerOrIsNamed
// (principle F): for every column of the real nodes table, edit that column in
// a file (with a title change, so the row is written), merge into a held task,
// replicate to a fresh peer and require that the write landed here; a column
// marked journaled must then arrive on the peer, and one marked local only
// must agree or be named. A column marked journaled that no op carries fails.
func TestMergeColumns_EveryColumnOfTheSchema_JournaledArrivesOnAPeerOrIsNamed(t *testing.T) {
	edits := columnEdits()
	coverage := sqlite.MergeColumnCoverage()
	for _, col := range schemaColumns(t) {
		e, ok := edits[col]
		require.True(t, ok, "nodes column %s has no entry in columnEdits: add the edit that changes it", col)
		if e.skip != "" {
			continue
		}
		t.Run(col, func(t *testing.T) {
			importer, peer := peerPair(t, 2)
			beforeHere, beforePeer := mergedColumn(t, importer, col, e), mergedColumn(t, peer, col, e)
			data := exportOf(t, importer)
			i := fileIdx(t, data, "REC-1")
			if col != "title" {
				data.Nodes[i].Title = "Retitled so the row is written"
			}
			e.edit(data, i)
			n := &data.Nodes[i]
			var labels []string
			require.NoError(t, json.Unmarshal([]byte(orEmptyList(n.Labels)), &labels))
			n.ContentHash = model.ComputeContentHash(n.Title, n.Description, n.Prompt, n.Acceptance, labels)
			require.NoError(t, sqlite.RecomputeExportChecksum(data))

			res := mergeTheirs(t, importer, data)
			replicate(t, importer, peer)

			here, there := mergedColumn(t, importer, col, e), mergedColumn(t, peer, col, e)
			named := false
			for _, f := range fieldsNamed(res, "REC-1") {
				named = named || f == col
			}
			if coverage[col] != "local only" {
				require.NotEqual(t, beforeHere, here, "column %s: the file's edit did not land here", col)
				require.Equal(t, here, there, "column %s is marked journaled (%s) and did not arrive on the peer", col, coverage[col])
				require.NotEqual(t, beforePeer, there, "column %s: nothing changed on the peer", col)
				return
			}
			require.True(t, here == there || named, "column %s differs on the peer (%s against %s) and the Local only line does not name it", col, here, there)
		})
	}
}

func orEmptyList(s string) string {
	if s == "" {
		return "[]"
	}
	return s
}
