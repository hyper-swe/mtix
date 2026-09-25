// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Shared fixtures for the MTIX-95.31.1 export and import tests: a node with
// every nodes column set, and helpers that read and edit an export through
// its JSON form, the way a tasks.json file reaches import.
package sqlite_test

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// columnTestTime is the fixed instant the column fixtures stamp.
func columnTestTime() time.Time {
	return time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
}

// columnTestAnnotations returns the two annotations the fixtures store: a
// resolved review verdict and an unresolved comment addressed to an agent.
// The text holds characters JSON escapes (<, >, &).
func columnTestAnnotations() []model.Annotation {
	ts := columnTestTime()
	return []model.Annotation{
		{
			ID: "01J9ANNOTATION0000000000AA", Author: "reviewer",
			Text:      "PASS: <evidence> & receipt",
			CreatedAt: ts.Add(time.Minute), Resolved: true,
		},
		{
			ID: "01J9ANNOTATION0000000000BB", Author: "lead",
			Text:      "please re-run the bar",
			CreatedAt: ts.Add(2 * time.Minute), Addressee: "agent-a",
		},
	}
}

// createAnnotatedNode creates a live root node with the fixture annotations
// and two activity entries (created, then a status change to blocked, which
// also stamps previous_status).
func createAnnotatedNode(t *testing.T, s *sqlite.Store, id string, seq int) {
	t.Helper()
	ctx := context.Background()
	ts := columnTestTime()
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: id, Project: "COL", Depth: 0, Seq: seq, Title: "Annotated " + id,
		Description: "description", Prompt: "prompt", Acceptance: "acceptance",
		NodeType: model.NodeTypeEpic, Priority: model.PriorityHigh,
		Status: model.StatusInProgress, Weight: 1.0, Creator: "creator-a",
		ContentHash: "hash-" + id, CreatedAt: ts, UpdatedAt: ts,
	}))
	require.NoError(t, s.SetAnnotations(ctx, id, columnTestAnnotations()))
	require.NoError(t, s.TransitionStatus(ctx, id, model.StatusBlocked, "waiting", "agent-a"))
}

// createEveryColumnNode creates root node id with every nodes column set
// (MTIX-95.31.1): CreateNode writes the model fields, SetAnnotations the
// annotations, a transition adds an activity entry with metadata, and
// DeleteNode stamps deleted_at and deleted_by.
func createEveryColumnNode(t *testing.T, s *sqlite.Store, id string, seq int) {
	t.Helper()
	ctx := context.Background()
	ts := columnTestTime()
	s.SetClock(func() time.Time { return ts.Add(3 * time.Hour) })
	closed, deferUntil, invalidated := ts.Add(time.Hour), ts.Add(48*time.Hour), ts.Add(2*time.Hour)
	est, act := 30, 45
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: id, Project: "COL", Depth: 0, Seq: seq, Title: "Every column " + id,
		Description: "description", Prompt: "prompt", Acceptance: "acceptance",
		NodeType: model.NodeTypeEpic, IssueType: model.IssueTypeBug,
		Priority: model.PriorityHigh, Labels: []string{"alpha", "beta"},
		Status: model.StatusInProgress, PreviousStatus: model.StatusOpen,
		Progress: 0.25, Assignee: "agent-a", Creator: "creator-a",
		AgentState: model.AgentStateWorking, CreatedAt: ts, UpdatedAt: ts,
		ClosedAt: &closed, DeferUntil: &deferUntil, EstimateMin: &est, ActualMin: &act,
		Weight: 2.5, ContentHash: "hash-" + id,
		CodeRefs:      []model.CodeRef{{File: "a.go", Line: 3, Function: "F", Snippet: "x := 1"}},
		CommitRefs:    []string{"abc123", "def456"},
		InvalidatedAt: &invalidated, InvalidatedBy: "reviewer", InvalidationReason: "stale plan",
		Metadata: json.RawMessage(`{"origin":"test","n":1}`), SessionID: "session-" + id,
	}))
	require.NoError(t, s.SetAnnotations(ctx, id, columnTestAnnotations()))
	require.NoError(t, s.TransitionStatus(ctx, id, model.StatusBlocked, "waiting", "agent-a"))
	require.NoError(t, s.DeleteNode(ctx, id, false, "deleter-a"))
}

// exportedNode returns the JSON object of node id as the export writes it.
func exportedNode(t *testing.T, data *sqlite.ExportData, id string) map[string]json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(data.Nodes)
	require.NoError(t, err)
	var nodes []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &nodes))
	for _, n := range nodes {
		var nodeID string
		require.NoError(t, json.Unmarshal(n["id"], &nodeID))
		if nodeID == id {
			return n
		}
	}
	require.Failf(t, "node not exported", "node %s is not in the export", id)
	return nil
}

// editExport writes data as the auto-export does (indented JSON), lets
// edit change the decoded document, and decodes the result the way import
// reads a file. The checksum is left as written, so a content edit fails
// verification unless the caller recomputes it.
func editExport(t *testing.T, data *sqlite.ExportData, edit func(doc map[string]any)) *sqlite.ExportData {
	t.Helper()
	raw, err := json.MarshalIndent(data, "", "  ")
	require.NoError(t, err)
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // keep every number literal exactly as written
	var doc map[string]any
	require.NoError(t, dec.Decode(&doc))
	edit(doc)
	out, err := json.MarshalIndent(doc, "", "  ")
	require.NoError(t, err)
	edited, err := sqlite.DecodeExportData(bytes.NewReader(out))
	require.NoError(t, err)
	return edited
}

// editExportResealed is editExport followed by a fresh checksum, so only
// the edited content (not its integrity) is under test.
func editExportResealed(t *testing.T, data *sqlite.ExportData, edit func(doc map[string]any)) *sqlite.ExportData {
	t.Helper()
	edited := editExport(t, data, edit)
	edited.Checksum = sqlite.RecomputeChecksumForTest(t, edited)
	return edited
}

// docNode returns node id's object from a decoded export document.
func docNode(t *testing.T, doc map[string]any, id string) map[string]any {
	t.Helper()
	nodes, ok := doc["nodes"].([]any)
	require.True(t, ok, "export document has no nodes array")
	for _, raw := range nodes {
		n, isObj := raw.(map[string]any)
		require.True(t, isObj)
		if n["id"] == id {
			return n
		}
	}
	require.Failf(t, "node not in document", "node %s is not in the export document", id)
	return nil
}

// docAnnotations returns node id's annotations array from a decoded export
// document; the test fails when the node has none.
func docAnnotations(t *testing.T, doc map[string]any, id string) []any {
	t.Helper()
	anns, ok := docNode(t, doc, id)["annotations"].([]any)
	require.True(t, ok, "node %s carries no annotations in the export", id)
	return anns
}

// legacyColumnKeys are the node keys schema 2.0.0 added (MTIX-95.31.1). A
// schema 1.0.0 file carries none of them.
func legacyColumnKeys() []string {
	return []string{
		"previous_status", "estimate_min", "actual_min", "code_refs", "commit_refs",
		"annotations", "invalidated_at", "invalidated_by", "invalidation_reason",
		"activity", "deleted_by", "metadata", "session_id",
	}
}

// asLegacyV100 turns a decoded export document into what mtix 0.5.3 wrote:
// schema_version 1.0.0 and none of the 2.0.0 node keys.
func asLegacyV100(t *testing.T, doc map[string]any) {
	t.Helper()
	doc["schema_version"] = "1.0.0"
	nodes, ok := doc["nodes"].([]any)
	require.True(t, ok)
	for _, raw := range nodes {
		n, isObj := raw.(map[string]any)
		require.True(t, isObj)
		for _, key := range legacyColumnKeys() {
			delete(n, key)
		}
	}
}

// annotationSummary renders a node's annotations as "id:resolved:text"
// strings, in stored order, for compact assertions.
func annotationSummary(t *testing.T, s *sqlite.Store, id string) []string {
	t.Helper()
	var raw *string
	require.NoError(t, s.QueryRow(context.Background(),
		`SELECT annotations FROM nodes WHERE id = ?`, id).Scan(&raw))
	if raw == nil || *raw == "" {
		return nil
	}
	var anns []model.Annotation
	require.NoError(t, json.Unmarshal([]byte(*raw), &anns))
	out := make([]string, 0, len(anns))
	for _, a := range anns {
		resolved := "open"
		if a.Resolved {
			resolved = "resolved"
		}
		out = append(out, a.ID+":"+resolved+":"+a.Text)
	}
	return out
}
