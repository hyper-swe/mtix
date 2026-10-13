// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests for MTIX-95.31.15.1: when the last-exchanged board is in an older
// form, the import compares the store and the pulled board with it per task
// and per field. Written red-first.
package sqlite_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// boardNode returns a node document with every 1.0.0 field set.
func boardNode(id, uid, status, assignee string) map[string]any {
	return map[string]any{
		"id": id, "parent_id": "", "depth": 0, "seq": 1, "project": "PROJ", "title": "Task " + id,
		"description": "d", "prompt": "", "acceptance": "", "node_type": "epic", "issue_type": "",
		"priority": 3, "labels": "[]", "status": status, "progress": 0.0, "assignee": assignee,
		"creator": "cli", "agent_state": "", "weight": 1.0, "content_hash": "h-" + id,
		"created_at": "2026-10-01T08:00:00Z", "updated_at": "2026-10-01T08:00:00Z", "uid": uid,
	}
}

// board builds a decoded export of the schema with a valid checksum.
func board(t *testing.T, schema string, nodes ...map[string]any) *sqlite.ExportData {
	t.Helper()
	return boardWithDeps(t, schema, nil, nodes...)
}

// boardWithDeps is board with the dependencies deps.
func boardWithDeps(t *testing.T, schema string, deps []any, nodes ...map[string]any) *sqlite.ExportData {
	t.Helper()
	if deps == nil {
		deps = []any{}
	}
	doc := map[string]any{"version": 1, "schema_version": schema, "project": "PROJ", "nodes": nodes,
		"dependencies": deps, "agents": []any{}, "sessions": []any{}}
	raw, err := json.Marshal(doc)
	require.NoError(t, err)
	data, err := sqlite.DecodeExportData(bytes.NewReader(raw))
	require.NoError(t, err)
	require.NoError(t, sqlite.RecomputeExportChecksum(data))
	return data
}

func withKeys(n map[string]any, kv ...any) map[string]any {
	c := map[string]any{}
	for k, v := range n {
		c[k] = v
	}
	for i := 0; i+1 < len(kv); i += 2 {
		c[kv[i].(string)] = kv[i+1]
	}
	return c
}

const (
	uidOne = "01995a1e-8c00-7a11-8b22-000000000001"
	uidTwo = "01995a1e-8c00-7a11-8b22-000000000002"
)

var (
	localActivity = []any{map[string]any{"id": "01J9ACT0000000000000000001", "type": "claim", "author": "alice",
		"text": "claimed", "created_at": "2026-10-02T09:00:00Z"}}
	localNote = []any{map[string]any{"id": "01J9ANN0000000000000000001", "author": "alice", "text": "note",
		"created_at": "2026-10-02T09:05:00Z"}}
)

func resolveFor(t *testing.T, base, local, file *sqlite.ExportData) *sqlite.BaseResolution {
	t.Helper()
	b, err := sqlite.ImportBaseFromBoard(base, "git-merge-base")
	require.NoError(t, err)
	require.True(t, b.Older)
	res, err := sqlite.ResolveWithBase(local, file, b)
	require.NoError(t, err)
	return res
}

func resolvedNode(t *testing.T, res *sqlite.BaseResolution, id string) map[string]any {
	t.Helper()
	require.NotNil(t, res.Resolved)
	return exportedNodeDoc(t, res.Resolved, id)
}

func exportedNodeDoc(t *testing.T, data *sqlite.ExportData, id string) map[string]any {
	t.Helper()
	raw, err := json.Marshal(data)
	require.NoError(t, err)
	var doc struct {
		Nodes []map[string]any `json:"nodes"`
	}
	require.NoError(t, json.Unmarshal(raw, &doc))
	for _, n := range doc.Nodes {
		if n["id"] == id {
			return n
		}
	}
	require.Failf(t, "node missing", "%s", id)
	return nil
}

func TestResolveWithBase_LocalUnchanged_TakesTeammateChangeKeepsHistory(t *testing.T) {
	p1 := boardNode("PROJ-1", uidOne, "in_progress", "alice")
	p2 := boardNode("PROJ-2", uidTwo, "open", "")
	base := board(t, "1.0.0", p1, p2)
	local := board(t, "2.0.0", withKeys(p1, "activity", localActivity, "annotations", localNote, "previous_status", "open"), p2)
	file := board(t, "2.0.0", p1, withKeys(p2, "status", "in_progress", "assignee", "bob", "updated_at", "2026-10-02T10:00:00Z"))
	res := resolveFor(t, base, local, file)
	assert.False(t, res.Diff.Lossy(), "%+v", res.Diff.Losses)
	n2 := resolvedNode(t, res, "PROJ-2")
	assert.Equal(t, "bob", n2["assignee"])
	assert.Equal(t, "in_progress", n2["status"])
	n1 := resolvedNode(t, res, "PROJ-1")
	assert.Len(t, n1["activity"], 1)
	assert.Len(t, n1["annotations"], 1)
	assert.Equal(t, "open", n1["previous_status"])
	assert.Equal(t, 1, res.Kept.Annotations)
	assert.Equal(t, 1, res.Kept.Activity)
}

func TestResolveWithBase_FileUnchanged_KeepsLocalChange(t *testing.T) {
	p1 := boardNode("PROJ-1", uidOne, "open", "")
	p2 := boardNode("PROJ-2", uidTwo, "open", "")
	base := board(t, "1.0.0", p1, p2)
	local := board(t, "2.0.0", withKeys(p1, "title", "Renamed", "updated_at", "2026-10-02T09:00:00Z"), p2)
	file := board(t, "2.0.0", p1, withKeys(p2, "status", "in_progress", "assignee", "bob"))
	res := resolveFor(t, base, local, file)
	assert.False(t, res.Diff.Lossy(), "%+v", res.Diff.Losses)
	assert.Equal(t, "Renamed", resolvedNode(t, res, "PROJ-1")["title"])
	assert.Equal(t, "2026-10-02T09:00:00Z", resolvedNode(t, res, "PROJ-1")["updated_at"], "the later copy's time")
	assert.Equal(t, "bob", resolvedNode(t, res, "PROJ-2")["assignee"])
	assert.Equal(t, 1, res.Kept.LocalChanges)
}

func TestResolveWithBase_BothChanged_ListsConflict(t *testing.T) {
	p1 := boardNode("PROJ-1", uidOne, "open", "")
	base := board(t, "1.0.0", p1)
	local := board(t, "2.0.0", withKeys(p1, "status", "in_progress", "assignee", "alice"))
	file := board(t, "2.0.0", withKeys(p1, "status", "in_progress", "assignee", "bob"))
	res := resolveFor(t, base, local, file)
	require.True(t, res.Diff.Lossy())
	require.Len(t, res.Diff.Losses, 1)
	assert.Equal(t, []string{"assignee"}, res.Diff.Losses[0].BothChanged)
}

func TestResolveWithBase_LocalOnlyTask_IsKept(t *testing.T) {
	p1 := boardNode("PROJ-1", uidOne, "open", "")
	p3 := boardNode("PROJ-3", "01995a1e-8c00-7a11-8b22-000000000003", "open", "")
	base := board(t, "1.0.0", p1)
	local := board(t, "2.0.0", p1, p3)
	file := board(t, "2.0.0", p1)
	res := resolveFor(t, base, local, file)
	assert.False(t, res.Diff.Lossy(), "%+v", res.Diff.Losses)
	assert.Equal(t, "PROJ-3", resolvedNode(t, res, "PROJ-3")["id"])
	assert.Equal(t, 1, res.Kept.LocalTasks)
}

func TestResolveWithBase_TaskTheBaseLacksAndFilesDiffer_IsConflict(t *testing.T) {
	p1 := boardNode("PROJ-1", uidOne, "open", "")
	p2 := boardNode("PROJ-2", uidTwo, "open", "")
	base := board(t, "1.0.0", p1)
	local := board(t, "2.0.0", p1, p2)
	file := board(t, "2.0.0", p1, withKeys(p2, "status", "in_progress"))
	res := resolveFor(t, base, local, file)
	assert.True(t, res.Diff.Lossy())
}

func TestResolveWithBase_BothSetCarriedField_IsConflict(t *testing.T) {
	p1 := boardNode("PROJ-1", uidOne, "open", "")
	base := board(t, "1.0.0", p1)
	local := board(t, "2.0.0", withKeys(p1, "estimate_min", 30))
	file := board(t, "2.0.0", withKeys(p1, "estimate_min", 60))
	res := resolveFor(t, base, local, file)
	require.True(t, res.Diff.Lossy())
	assert.Equal(t, []string{"estimate_min"}, res.Diff.Losses[0].BothChanged)
}

func TestResolveWithBase_LocalDependencyTheFileLacks_StaysALoss(t *testing.T) {
	p1 := boardNode("PROJ-1", uidOne, "open", "")
	p2 := boardNode("PROJ-2", uidTwo, "open", "")
	base := board(t, "1.0.0", p1, p2)
	dep := map[string]any{"from_id": "PROJ-1", "to_id": "PROJ-2", "dep_type": "blocks", "created_at": "2026-10-02T09:00:00Z"}
	local := boardWithDeps(t, "2.0.0", []any{dep}, p1, p2)
	res := resolveFor(t, base, local, board(t, "2.0.0", p1, p2))
	require.True(t, res.Diff.Lossy())
	assert.NotEmpty(t, res.Diff.Losses[0].Dependencies)
}

func TestResolveWithBase_NodeTheFileLacks_StaysALoss(t *testing.T) {
	p1 := boardNode("PROJ-1", uidOne, "open", "")
	p2 := boardNode("PROJ-2", uidTwo, "open", "")
	base := board(t, "1.0.0", p1, p2)
	res := resolveFor(t, base, board(t, "2.0.0", p1, p2), board(t, "2.0.0", p1))
	require.True(t, res.Diff.Lossy())
	assert.True(t, res.Diff.Losses[0].WholeNode)
}

func TestResolveWithBase_NotOlderBase_ReturnsInvalidInput(t *testing.T) {
	p1 := boardNode("PROJ-1", uidOne, "open", "")
	b, err := sqlite.ImportBaseFromBoard(board(t, "2.0.0", p1), "git-merge-base")
	require.NoError(t, err)
	_, err = sqlite.ResolveWithBase(board(t, "2.0.0", p1), board(t, "2.0.0", p1), b)
	require.ErrorIs(t, err, model.ErrInvalidInput)
}

func TestResolveWithBase_CarriedFieldTheFileLacks_IsKept(t *testing.T) {
	p1 := boardNode("PROJ-1", uidOne, "open", "")
	base := board(t, "1.0.0", p1)
	local := board(t, "2.0.0", withKeys(p1, "estimate_min", 30, "metadata", `{"k":"v"}`))
	res := resolveFor(t, base, local, board(t, "2.0.0", p1))
	assert.False(t, res.Diff.Lossy(), "%+v", res.Diff.Losses)
	n := resolvedNode(t, res, "PROJ-1")
	assert.EqualValues(t, 30, n["estimate_min"])
	assert.Equal(t, `{"k":"v"}`, n["metadata"])
	assert.Equal(t, 2, res.Kept.Fields)
}

func TestResolveWithBase_TeammateUndelete_DropsDeletedBy(t *testing.T) {
	base := board(t, "1.0.0", boardNode("PROJ-1", uidOne, "open", ""))
	local := board(t, "2.0.0", withKeys(boardNode("PROJ-1", uidOne, "open", ""),
		"deleted_at", "2026-10-02T09:00:00Z", "deleted_by", "alice"))
	// The file still holds the task deleted: deleted_by is kept (the delete is the local change, file unchanged).
	res := resolveFor(t, base, local, board(t, "2.0.0", boardNode("PROJ-1", uidOne, "open", "")))
	assert.False(t, res.Diff.Lossy(), "%+v", res.Diff.Losses)
	assert.Equal(t, "2026-10-02T09:00:00Z", resolvedNode(t, res, "PROJ-1")["deleted_at"])
	assert.Equal(t, "alice", resolvedNode(t, res, "PROJ-1")["deleted_by"])
	// The file holds it live although the local copy is unchanged from the base: the teammate undeleted it.
	liveLocal := board(t, "2.0.0", withKeys(boardNode("PROJ-1", uidOne, "open", ""), "deleted_by", "alice"))
	res = resolveFor(t, base, liveLocal, board(t, "2.0.0", boardNode("PROJ-1", uidOne, "open", "")))
	assert.NotContains(t, resolvedNode(t, res, "PROJ-1"), "deleted_by")
}

func TestResolveWithBase_PreviousStatus_FollowsTheFilesStatus(t *testing.T) {
	p1 := boardNode("PROJ-1", uidOne, "open", "")
	base := board(t, "1.0.0", p1)
	local := board(t, "2.0.0", withKeys(p1, "previous_status", "blocked"))
	file := board(t, "2.0.0", withKeys(p1, "status", "in_progress", "assignee", "bob", "previous_status", "open"))
	res := resolveFor(t, base, local, file)
	assert.False(t, res.Diff.Lossy(), "%+v", res.Diff.Losses)
	assert.Equal(t, "open", resolvedNode(t, res, "PROJ-1")["previous_status"])
}

func TestResolveWithBase_BaseWithoutUIDs_MatchesTasksById(t *testing.T) {
	old1 := boardNode("PROJ-1", "", "open", "")
	old2 := boardNode("PROJ-2", "", "open", "")
	base := board(t, "1.0.0", old1, old2)
	local := board(t, "2.0.0", withKeys(old1, "uid", uidOne, "activity", localActivity), withKeys(old2, "uid", uidTwo))
	file := board(t, "2.0.0", withKeys(old1, "uid", uidOne), withKeys(old2, "uid", uidTwo, "status", "in_progress", "assignee", "bob"))
	res := resolveFor(t, base, local, file)
	assert.False(t, res.Diff.Lossy(), "%+v", res.Diff.Losses)
	assert.Equal(t, "bob", resolvedNode(t, res, "PROJ-2")["assignee"])
	assert.Len(t, resolvedNode(t, res, "PROJ-1")["activity"], 1)
}

func TestResolveWithBase_KeptLocalContent_RecomputesContentHash(t *testing.T) {
	p1 := boardNode("PROJ-1", uidOne, "open", "")
	base := board(t, "1.0.0", p1)
	local := board(t, "2.0.0", withKeys(p1, "description", "edited here", "content_hash", "local-hash"))
	file := board(t, "2.0.0", withKeys(p1, "status", "in_progress", "assignee", "bob", "content_hash", "h-PROJ-1"))
	res := resolveFor(t, base, local, file)
	n := resolvedNode(t, res, "PROJ-1")
	assert.Equal(t, "edited here", n["description"])
	assert.Equal(t, "bob", n["assignee"])
	assert.Equal(t, model.ComputeContentHash("Task PROJ-1", "edited here", "", "", []string{}), n["content_hash"])
}

func TestResolveWithBase_ResolvedBoard_ChecksumVerifies(t *testing.T) {
	p1 := boardNode("PROJ-1", uidOne, "open", "")
	base := board(t, "1.0.0", p1)
	res := resolveFor(t, base, board(t, "2.0.0", withKeys(p1, "activity", localActivity)), board(t, "2.0.0", p1))
	require.NoError(t, sqlite.ValidateExport(res.Resolved))
}

func TestResolveWithBase_KeptLocalStatus_KeepsLocalProgress(t *testing.T) {
	p1 := boardNode("PROJ-1", uidOne, "open", "")
	base := board(t, "1.0.0", p1)
	local := board(t, "2.0.0", withKeys(p1, "status", "done", "progress", 1.0))
	file := board(t, "2.0.0", withKeys(p1, "title", "Retitled by the teammate"))
	res := resolveFor(t, base, local, file)
	assert.False(t, res.Diff.Lossy(), "%+v", res.Diff.Losses)
	n := resolvedNode(t, res, "PROJ-1")
	assert.Equal(t, "done", n["status"])
	assert.EqualValues(t, 1.0, n["progress"])
	assert.Equal(t, "Retitled by the teammate", n["title"])
}

func TestResolveWithBase_LocalClear_IsKeptWhenFileEqualsBase(t *testing.T) {
	p1 := boardNode("PROJ-1", uidOne, "in_progress", "alice")
	base := board(t, "1.0.0", p1)
	local := board(t, "2.0.0", withKeys(p1, "status", "open", "assignee", ""))
	file := board(t, "2.0.0", withKeys(p1, "description", "edited by the teammate"))
	res := resolveFor(t, base, local, file)
	assert.False(t, res.Diff.Lossy(), "%+v", res.Diff.Losses)
	n := resolvedNode(t, res, "PROJ-1")
	assert.Equal(t, "", n["assignee"], "the local unclaim is kept")
	assert.Equal(t, "edited by the teammate", n["description"])
}

// previous_status is a field of its own: it is resolved against the base, and a kept
// local status never silently takes the file's previous_status.
func TestResolveWithBase_PreviousStatus_ResolvedAgainstTheBase(t *testing.T) {
	p1 := boardNode("PROJ-1", uidOne, "open", "")
	base := board(t, "1.0.0", p1)
	deferredHere := withKeys(p1, "status", "deferred", "previous_status", "open")
	tests := []struct {
		name         string
		local, file  map[string]any
		wantConflict bool
		wantPrevious string
		wantStatus   string
	}{
		{"kept local status keeps its previous_status when the file holds none", deferredHere, p1, false, "open", "deferred"},
		{"kept local status and a different previous_status in the file: the local one goes with its status", deferredHere,
			withKeys(p1, "previous_status", "in_progress"), false, "open", "deferred"},
		{"status taken from the file takes the file's previous_status (the teammate's transition)",
			withKeys(p1, "previous_status", "open"),
			withKeys(p1, "status", "done", "previous_status", "in_progress"), false, "in_progress", "done"},
		{"unchanged status and two different previous_status values is a conflict",
			withKeys(p1, "previous_status", "blocked"), withKeys(p1, "previous_status", "deferred"), true, "", ""},
		{"file holds none, local value kept", withKeys(p1, "previous_status", "blocked"), p1, false, "blocked", "open"},
		{"local holds none, file value taken", p1, withKeys(p1, "previous_status", "deferred"), false, "deferred", "open"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := resolveFor(t, base, board(t, "2.0.0", tt.local), board(t, "2.0.0", tt.file))
			if tt.wantConflict {
				require.True(t, res.Diff.Lossy())
				assert.Equal(t, []string{"previous_status"}, res.Diff.Losses[0].BothChanged)
				return
			}
			assert.False(t, res.Diff.Lossy(), "%+v", res.Diff.Losses)
			n := resolvedNode(t, res, "PROJ-1")
			assert.Equal(t, tt.wantStatus, n["status"])
			assert.Equal(t, tt.wantPrevious, n["previous_status"])
		})
	}
}

// The three invalidation fields are one fact: they are taken or kept together.
func TestResolveWithBase_InvalidationFields_AreResolvedTogether(t *testing.T) {
	p1 := boardNode("PROJ-1", uidOne, "open", "")
	base := board(t, "1.0.0", p1)
	invalid := withKeys(p1, "invalidated_at", "2026-10-02T09:00:00Z", "invalidated_by", "alice", "invalidation_reason", "stale")
	res := resolveFor(t, base, board(t, "2.0.0", invalid), board(t, "2.0.0", p1))
	assert.False(t, res.Diff.Lossy(), "%+v", res.Diff.Losses)
	n := resolvedNode(t, res, "PROJ-1")
	assert.Equal(t, "alice", n["invalidated_by"])
	assert.Equal(t, "stale", n["invalidation_reason"])
	// The file holds a different invalidation: a mix of the two would be a third fact.
	other := withKeys(p1, "invalidated_at", "2026-10-03T09:00:00Z", "invalidated_by", "bob")
	res = resolveFor(t, base, board(t, "2.0.0", invalid), board(t, "2.0.0", other))
	require.True(t, res.Diff.Lossy())
	assert.Equal(t, []string{"invalidated_at"}, res.Diff.Losses[0].BothChanged)
}

// The base holds no dependencies, so a dependency that differs between the store and
// the file cannot be told from the teammate's change: it is refused, never resolved.
func TestResolveWithBase_DependencyDifference_IsRefusedNotResolved(t *testing.T) {
	p1 := boardNode("PROJ-1", uidOne, "open", "")
	p2 := boardNode("PROJ-2", uidTwo, "open", "")
	dep := map[string]any{"from_id": "PROJ-1", "to_id": "PROJ-2", "dep_type": "blocks", "created_at": "2026-10-02T09:00:00Z"}
	base := board(t, "1.0.0", p1, p2)
	tests := []struct {
		name        string
		local, file *sqlite.ExportData
	}{
		{"a dependency only the store holds", boardWithDeps(t, "2.0.0", []any{dep}, p1, p2), board(t, "2.0.0", p1, p2)},
		{"a dependency only the file holds, which the store may have removed",
			board(t, "2.0.0", p1, p2), boardWithDeps(t, "2.0.0", []any{dep}, p1, p2)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := resolveFor(t, base, tt.local, tt.file)
			require.True(t, res.Diff.Lossy(), "a dependency difference must be refused")
			raw, err := json.Marshal(res.Resolved)
			require.NoError(t, err)
			if len(tt.file.Dependencies) == 0 {
				assert.NotContains(t, string(raw), `"from_id":"PROJ-1"`, "the local dependency is not written")
			}
		})
	}
}

// depDoc builds a dependency document.
func depDoc(from, to string) map[string]any {
	return map[string]any{"from_id": from, "to_id": to, "dep_type": "blocks", "created_at": "2026-10-02T09:00:00Z"}
}

func resolvedDeps(t *testing.T, res *sqlite.BaseResolution) []string {
	t.Helper()
	raw, err := json.Marshal(res.Resolved)
	require.NoError(t, err)
	var doc struct {
		Dependencies []struct {
			FromID string `json:"from_id"`
			ToID   string `json:"to_id"`
		} `json:"dependencies"`
	}
	require.NoError(t, json.Unmarshal(raw, &doc))
	var out []string
	for _, d := range doc.Dependencies {
		out = append(out, d.FromID+">"+d.ToID)
	}
	return out
}

// A dependency whose author is certain is resolved: one touching a task made locally since the
// exchange is the store's, one touching a task new in the file is the teammate's.
func TestResolveWithBase_DependencyOfANewTask_IsResolvedByItsAuthor(t *testing.T) {
	p1 := boardNode("PROJ-1", uidOne, "open", "")
	p3 := boardNode("PROJ-3", "01995a1e-8c00-7a11-8b22-000000000003", "open", "")
	p4 := boardNode("PROJ-4", "01995a1e-8c00-7a11-8b22-000000000004", "open", "")
	p5 := boardNode("PROJ-5", "01995a1e-8c00-7a11-8b22-000000000005", "open", "")
	base := board(t, "1.0.0", p1)
	tests := []struct {
		name        string
		local, file *sqlite.ExportData
		want        []string
	}{
		{"both ends made locally: kept from the store",
			boardWithDeps(t, "2.0.0", []any{depDoc("PROJ-3", "PROJ-4")}, p1, p3, p4), board(t, "2.0.0", p1), []string{"PROJ-3>PROJ-4"}},
		{"a local task and a pre-existing one: kept from the store (only the store can have made it)",
			boardWithDeps(t, "2.0.0", []any{depDoc("PROJ-3", "PROJ-1")}, p1, p3), board(t, "2.0.0", p1), []string{"PROJ-3>PROJ-1"}},
		{"a task new in the file: the dependency is taken from the file",
			board(t, "2.0.0", p1), boardWithDeps(t, "2.0.0", []any{depDoc("PROJ-5", "PROJ-1")}, p1, p5), []string{"PROJ-5>PROJ-1"}},
		{"an existing task blocks a local task: kept from the store",
			boardWithDeps(t, "2.0.0", []any{depDoc("PROJ-1", "PROJ-3")}, p1, p3), board(t, "2.0.0", p1), []string{"PROJ-1>PROJ-3"}},
		{"an existing task blocks a task new in the file: taken from the file",
			board(t, "2.0.0", p1), boardWithDeps(t, "2.0.0", []any{depDoc("PROJ-1", "PROJ-5")}, p1, p5), []string{"PROJ-1>PROJ-5"}},
		{"a local task's dependency the file also holds is written once",
			boardWithDeps(t, "2.0.0", []any{depDoc("PROJ-3", "PROJ-1")}, p1, p3),
			boardWithDeps(t, "2.0.0", []any{depDoc("PROJ-3", "PROJ-1")}, p1), []string{"PROJ-3>PROJ-1"}},
		{"both kinds at once",
			boardWithDeps(t, "2.0.0", []any{depDoc("PROJ-3", "PROJ-4")}, p1, p3, p4),
			boardWithDeps(t, "2.0.0", []any{depDoc("PROJ-5", "PROJ-1")}, p1, p5), []string{"PROJ-5>PROJ-1", "PROJ-3>PROJ-4"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := resolveFor(t, base, tt.local, tt.file)
			assert.False(t, res.Diff.Lossy(), "%+v", res.Diff.Losses)
			assert.ElementsMatch(t, tt.want, resolvedDeps(t, res))
		})
	}
}

// Everything else that differs stays refused: an ambiguous author is never guessed, and a
// resolvable dependency does not excuse an ambiguous one beside it.
func TestResolveWithBase_DependencyBetweenExistingTasks_StaysRefused(t *testing.T) {
	p1 := boardNode("PROJ-1", uidOne, "open", "")
	p2 := boardNode("PROJ-2", uidTwo, "open", "")
	p5 := boardNode("PROJ-5", "01995a1e-8c00-7a11-8b22-000000000005", "open", "")
	base := board(t, "1.0.0", p1, p2)
	tests := []struct {
		name        string
		local, file *sqlite.ExportData
	}{
		{"a file-only dependency between pre-existing tasks (the store may have removed it)",
			board(t, "2.0.0", p1, p2), boardWithDeps(t, "2.0.0", []any{depDoc("PROJ-1", "PROJ-2")}, p1, p2)},
		{"a store-only dependency between pre-existing tasks",
			boardWithDeps(t, "2.0.0", []any{depDoc("PROJ-1", "PROJ-2")}, p1, p2), board(t, "2.0.0", p1, p2)},
		{"a new file task's dependency does not excuse a file-only one between existing tasks",
			board(t, "2.0.0", p1, p2),
			boardWithDeps(t, "2.0.0", []any{depDoc("PROJ-5", "PROJ-1"), depDoc("PROJ-1", "PROJ-2")}, p1, p2, p5)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := resolveFor(t, base, tt.local, tt.file)
			require.True(t, res.Diff.Lossy(), "an ambiguous dependency difference must be refused")
			if len(tt.file.Dependencies) == 0 {
				assert.Empty(t, resolvedDeps(t, res), "the local dependency is not written")
			}
		})
	}
}

// With the status kept from the store the previous_status goes with it, even when the
// file holds a stale one.
func TestResolveWithBase_KeptLocalStatus_DropsAStaleFilePreviousStatus(t *testing.T) {
	p1 := boardNode("PROJ-1", uidOne, "in_progress", "")
	base := board(t, "1.0.0", p1)
	local := board(t, "2.0.0", withKeys(p1, "status", "open"))
	file := board(t, "2.0.0", withKeys(p1, "previous_status", "blocked"))
	res := resolveFor(t, base, local, file)
	assert.False(t, res.Diff.Lossy(), "%+v", res.Diff.Losses)
	n := resolvedNode(t, res, "PROJ-1")
	assert.Equal(t, "open", n["status"])
	assert.NotContains(t, n, "previous_status")
}

// A local undelete, reopen or undefer clears an omitempty field. While the file equals the
// base the clear is kept: the field must be absent in the resolved task, not restored.
func TestResolveWithBase_LocalClearOfOptionalField_IsKept(t *testing.T) {
	tests := []struct {
		name, field string
		base        map[string]any
		local       map[string]any
	}{
		{"undelete", "deleted_at", withKeys(boardNode("PROJ-1", uidOne, "open", ""), "deleted_at", "2026-10-02T09:00:00Z"),
			boardNode("PROJ-1", uidOne, "open", "")},
		{"reopen", "closed_at", withKeys(boardNode("PROJ-1", uidOne, "done", ""), "closed_at", "2026-10-02T09:00:00Z"),
			boardNode("PROJ-1", uidOne, "open", "")},
		{"undefer", "defer_until", withKeys(boardNode("PROJ-1", uidOne, "deferred", ""), "defer_until", "2026-11-02T09:00:00Z"),
			boardNode("PROJ-1", uidOne, "open", "")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := resolveFor(t, board(t, "1.0.0", tt.base), board(t, "2.0.0", tt.local), board(t, "2.0.0", tt.base))
			assert.False(t, res.Diff.Lossy(), "%+v", res.Diff.Losses)
			assert.NotContains(t, resolvedNode(t, res, "PROJ-1"), tt.field, "the local %s is kept", tt.name)
		})
	}
}

func TestResolveWithBase_StatusFromFile_ReplacesAStalePreviousStatus(t *testing.T) {
	p1 := boardNode("PROJ-1", uidOne, "open", "")
	base := board(t, "1.0.0", p1)
	local := board(t, "2.0.0", withKeys(p1, "previous_status", "in_progress"))
	file := board(t, "2.0.0", withKeys(p1, "status", "blocked")) // a 0.5.3 teammate's board carries no previous_status
	res := resolveFor(t, base, local, file)
	assert.False(t, res.Diff.Lossy(), "%+v", res.Diff.Losses)
	n := resolvedNode(t, res, "PROJ-1")
	assert.Equal(t, "blocked", n["status"])
	assert.NotContains(t, n, "previous_status", "the stale value pointed into the replaced status: a later unblock restores open")
	assert.Zero(t, res.Kept.Fields)
}

func TestResolveWithBase_InvalidationUnitDiffersInOneKey_NamesThatKey(t *testing.T) {
	p1 := boardNode("PROJ-1", uidOne, "open", "")
	base := board(t, "1.0.0", p1)
	mine := withKeys(p1, "invalidated_at", "2026-10-02T09:00:00Z", "invalidated_by", "alice", "invalidation_reason", "stale")
	theirs := withKeys(p1, "invalidated_at", "2026-10-02T09:00:00Z", "invalidated_by", "bob", "invalidation_reason", "stale")
	res := resolveFor(t, base, board(t, "2.0.0", mine), board(t, "2.0.0", theirs))
	require.True(t, res.Diff.Lossy())
	assert.Equal(t, []string{"invalidated_by"}, res.Diff.Losses[0].BothChanged)
}

// Without uids the base matches a task by id only while the title still agrees:
// another task under the same id must not read as the same task.
func TestResolveWithBase_BaseWithoutUIDs_DifferentTaskUnderTheId_IsNotTakenAsTheSame(t *testing.T) {
	baseTask := withKeys(boardNode("PROJ-1", "", "open", ""), "title", "X")
	base := board(t, "1.0.0", baseTask)
	localTask := withKeys(boardNode("PROJ-1", uidOne, "open", ""), "title", "Y") // description as in the base task
	fileTask := withKeys(boardNode("PROJ-1", uidOne, "open", ""), "title", "Y", "description", "theirs")
	res := resolveFor(t, base, board(t, "2.0.0", localTask), board(t, "2.0.0", fileTask))
	require.True(t, res.Diff.Lossy())
	assert.Contains(t, res.Diff.Losses[0].BothChanged, "description")
	assert.Equal(t, "theirs", resolvedNode(t, res, "PROJ-1")["description"], "nothing taken from the local copy on a guess")
}

// A base without uids (a 0.3.x board) holds its tasks by id. A task the base and the file hold
// but the store lacks (deleted and purged) is not new in the file: no panic, and its
// dependencies stay ambiguous and refused.
func TestResolveWithBase_UIDlessBase_TaskTheStoreLacks_IsNotNewInTheFile(t *testing.T) {
	p1 := boardNode("PROJ-1", "", "open", "")
	p2 := boardNode("PROJ-2", "", "open", "")
	base := board(t, "1.0.0", p1, p2)
	local := board(t, "2.0.0", p1)
	tests := []struct {
		name string
		file *sqlite.ExportData
	}{
		{"1.x file", boardWithDeps(t, "1.0.0", []any{depDoc("PROJ-2", "PROJ-1")}, p1, p2)},
		{"2.0.0 file", boardWithDeps(t, "2.0.0", []any{depDoc("PROJ-2", "PROJ-1")}, p1, p2)},
		{"retitled by the teammate", boardWithDeps(t, "2.0.0", []any{depDoc("PROJ-2", "PROJ-1")}, p1,
			withKeys(p2, "title", "Retitled"))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var res *sqlite.BaseResolution
			require.NotPanics(t, func() { res = resolveFor(t, base, local, tt.file) })
			require.True(t, res.Diff.Lossy(), "the dependency of a task the base held is ambiguous")
		})
	}
}

// A different task under the id is not new in the file either: its dependency stays listed.
func TestResolveWithBase_DifferentTaskUnderTheId_KeepsItsDependencyListed(t *testing.T) {
	p1 := boardNode("PROJ-1", uidOne, "open", "")
	p2 := boardNode("PROJ-2", uidTwo, "open", "")
	other := withKeys(boardNode("PROJ-1", "01995a1e-8c00-7a11-8b22-000000000009", "open", ""), "title", "Another task")
	base := board(t, "1.0.0", p1, p2)
	res := resolveFor(t, base, board(t, "2.0.0", p1, p2), boardWithDeps(t, "2.0.0", []any{depDoc("PROJ-1", "PROJ-2")}, other, p2))
	require.True(t, res.Diff.Lossy())
	var listed []string
	for _, l := range res.Diff.Losses {
		listed = append(listed, l.BothChanged...)
	}
	assert.Contains(t, listed, "dependency blocks PROJ-2 (only in the file)")
}

// A local task's dependency on a task the file renumbered cannot be written under the store's id:
// the import is refused, not resolved.
func TestResolveWithBase_DependencyOnARenumberedTask_IsRefused(t *testing.T) {
	p1 := boardNode("PROJ-1", uidOne, "open", "")
	p3 := boardNode("PROJ-3", "01995a1e-8c00-7a11-8b22-000000000003", "open", "")
	moved := withKeys(p1, "id", "PROJ-7")
	res := resolveFor(t, board(t, "1.0.0", p1), boardWithDeps(t, "2.0.0", []any{depDoc("PROJ-3", "PROJ-1")}, p1, p3),
		board(t, "2.0.0", moved))
	assert.True(t, res.Diff.Lossy(), "%+v", res.Diff.Losses)
}
