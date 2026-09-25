// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests for MTIX-95.31.2 (FR-15.2i): DiffReplace compares a store's own
// export with a file a replace import would apply, node by node, and names
// every piece of local data the file lacks: whole nodes, annotations by id,
// annotation resolutions, activity entries by their merge key, and field
// values the file leaves empty. Written red-first.
package sqlite_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// diffFixture returns the export of a store holding COL-1 (every column
// set, soft-deleted) and COL-2 (live, two annotations, two activity
// entries).
func diffFixture(t *testing.T) *sqlite.ExportData {
	t.Helper()
	s := newTestStore(t)
	createEveryColumnNode(t, s, "COL-1", 1)
	createAnnotatedNode(t, s, "COL-2", 2)
	local, err := s.Export(context.Background(), "", "")
	require.NoError(t, err)
	return local
}

// localActivityCount returns how many activity entries node id holds in the
// export.
func localActivityCount(t *testing.T, data *sqlite.ExportData, id string) int {
	t.Helper()
	var entries []json.RawMessage
	raw, ok := exportedNode(t, data, id)["activity"]
	require.True(t, ok, "node %s has no activity in the fixture", id)
	require.NoError(t, json.Unmarshal(raw, &entries))
	require.NotEmpty(t, entries)
	return len(entries)
}

// dropNewestActivity removes node id's newest activity entry from a decoded
// export document: the file then no longer descends from the local copy.
func dropNewestActivity(t *testing.T, doc map[string]any, id string) {
	t.Helper()
	n := docNode(t, doc, id)
	entries, ok := n["activity"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, entries)
	n["activity"] = entries[:len(entries)-1]
}

// lossOf returns the loss DiffReplace reported for node id, or nil.
func lossOf(diff *sqlite.ReplaceDiff, id string) *sqlite.NodeLoss {
	for i := range diff.Losses {
		if diff.Losses[i].NodeID == id {
			return &diff.Losses[i]
		}
	}
	return nil
}

// TestDiffReplace_LocalDataTheFileLacks_ReportsLoss verifies each kind of
// loss a replace import would cause is reported for the node that holds it,
// and that the replace is then lossy.
func TestDiffReplace_LocalDataTheFileLacks_ReportsLoss(t *testing.T) {
	local := diffFixture(t)
	col1Activity := localActivityCount(t, local, "COL-1")
	col2Activity := localActivityCount(t, local, "COL-2")

	tests := []struct {
		name string
		edit func(t *testing.T, doc map[string]any)
		want map[string]sqlite.NodeLoss
	}{
		{"file written by an older client (schema 1.0.0)", func(t *testing.T, doc map[string]any) {
			asLegacyV100(t, doc)
		}, map[string]sqlite.NodeLoss{
			"COL-1": {NodeID: "COL-1", SoftDeleted: true, Annotations: []string{annA, annB}, Activity: col1Activity,
				Fields: []string{
					"actual_min", "code_refs", "commit_refs", "deleted_by", "estimate_min", "invalidated_at",
					"invalidated_by", "invalidation_reason", "metadata", "previous_status", "session_id",
				}},
			"COL-2": {NodeID: "COL-2", Annotations: []string{annA, annB}, Activity: col2Activity,
				Fields: []string{"previous_status"}},
		}},
		{"node missing from the file", func(t *testing.T, doc map[string]any) {
			nodes, ok := doc["nodes"].([]any)
			require.True(t, ok)
			doc["nodes"] = nodes[:1] // COL-1 only
		}, map[string]sqlite.NodeLoss{"COL-2": {NodeID: "COL-2", WholeNode: true}}},
		{"soft-deleted node missing from the file", func(t *testing.T, doc map[string]any) {
			nodes, ok := doc["nodes"].([]any)
			require.True(t, ok)
			doc["nodes"] = nodes[1:] // COL-2 only
		}, map[string]sqlite.NodeLoss{"COL-1": {NodeID: "COL-1", WholeNode: true, SoftDeleted: true}}},
		{"one annotation missing", func(t *testing.T, doc map[string]any) {
			anns := docAnnotations(t, doc, "COL-2")
			docNode(t, doc, "COL-2")["annotations"] = anns[:1]
		}, map[string]sqlite.NodeLoss{"COL-2": {NodeID: "COL-2", Annotations: []string{annB}}}},
		{"annotation resolved locally but open in the file", func(t *testing.T, doc map[string]any) {
			ann, ok := docAnnotations(t, doc, "COL-2")[0].(map[string]any)
			require.True(t, ok)
			require.Equal(t, annA, ann["id"])
			ann["resolved"] = false
		}, map[string]sqlite.NodeLoss{"COL-2": {NodeID: "COL-2", Unresolved: []string{annA}}}},
		{"activity entry missing", func(t *testing.T, doc map[string]any) {
			n := docNode(t, doc, "COL-2")
			entries, ok := n["activity"].([]any)
			require.True(t, ok)
			n["activity"] = entries[1:]
		}, map[string]sqlite.NodeLoss{"COL-2": {NodeID: "COL-2", Activity: 1}}},
		{"stale file leaves the assignee empty", func(t *testing.T, doc map[string]any) {
			n := docNode(t, doc, "COL-1")
			require.NotEmpty(t, n["assignee"], "fixture: COL-1 is assigned")
			n["assignee"] = ""
			dropNewestActivity(t, doc, "COL-1")
		}, map[string]sqlite.NodeLoss{"COL-1": {
			NodeID: "COL-1", SoftDeleted: true, Activity: 1, Fields: []string{"assignee"},
		}}},
		{"stale file leaves the labels as an empty list", func(t *testing.T, doc map[string]any) {
			docNode(t, doc, "COL-1")["labels"] = "[]"
			dropNewestActivity(t, doc, "COL-1")
		}, map[string]sqlite.NodeLoss{"COL-1": {
			NodeID: "COL-1", SoftDeleted: true, Activity: 1, Fields: []string{"labels"},
		}}},
		{"stale file leaves the metadata as an empty object", func(t *testing.T, doc map[string]any) {
			docNode(t, doc, "COL-1")["metadata"] = "{}"
			dropNewestActivity(t, doc, "COL-1")
		}, map[string]sqlite.NodeLoss{"COL-1": {
			NodeID: "COL-1", SoftDeleted: true, Activity: 1, Fields: []string{"metadata"},
		}}},
		{"schema 1.0.0 file leaves a field empty, activity kept", func(t *testing.T, doc map[string]any) {
			doc["schema_version"] = "1.0.0"
			docNode(t, doc, "COL-1")["assignee"] = ""
		}, map[string]sqlite.NodeLoss{"COL-1": {NodeID: "COL-1", SoftDeleted: true, Fields: []string{"assignee"}}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file := editExport(t, local, func(doc map[string]any) { tt.edit(t, doc) })
			diff, err := sqlite.DiffReplace(local, file)
			require.NoError(t, err)
			assert.True(t, diff.Lossy(), "the replace would delete local data")
			require.Len(t, diff.Losses, len(tt.want))
			for id, want := range tt.want {
				got := lossOf(diff, id)
				require.NotNil(t, got, "no loss reported for %s", id)
				assert.Equal(t, want, *got)
			}
		})
	}
}

// TestDiffReplace_AdditionsAndUpdates_NotLossy verifies a file that only
// adds to or changes the local data is not lossy, and that the diff counts
// the nodes it adds and updates (MTIX-95.31.2).
func TestDiffReplace_AdditionsAndUpdates_NotLossy(t *testing.T) {
	local := diffFixture(t)

	tests := []struct {
		name        string
		edit        func(t *testing.T, doc map[string]any)
		wantAdded   []string
		wantUpdated []string
	}{
		{"identical file", func(*testing.T, map[string]any) {}, nil, nil},
		{"title changed upstream", func(t *testing.T, doc map[string]any) {
			changeContent(t, doc, "COL-2")
		}, nil, []string{"COL-2"}},
		{"node added upstream", func(t *testing.T, doc map[string]any) {
			nodes, ok := doc["nodes"].([]any)
			require.True(t, ok)
			added := map[string]any{}
			for k, v := range docNode(t, doc, "COL-2") {
				added[k] = v
			}
			added["id"], added["seq"] = "COL-3", json.Number("3")
			doc["nodes"] = append(nodes, added)
		}, []string{"COL-3"}, nil},
		{"annotation added upstream", func(t *testing.T, doc map[string]any) {
			n := docNode(t, doc, "COL-2")
			n["annotations"] = append(docAnnotations(t, doc, "COL-2"), incomingAnnotationC())
		}, nil, []string{"COL-2"}},
		{"annotation resolved upstream", func(t *testing.T, doc map[string]any) {
			ann, ok := docAnnotations(t, doc, "COL-2")[1].(map[string]any)
			require.True(t, ok)
			require.Equal(t, annB, ann["id"])
			ann["resolved"] = true
		}, nil, []string{"COL-2"}},
		{"empty field set upstream", func(t *testing.T, doc map[string]any) {
			n := docNode(t, doc, "COL-2")
			require.Empty(t, n["assignee"], "fixture: COL-2 is unassigned")
			n["assignee"] = "teammate"
		}, nil, []string{"COL-2"}},
		{"field changed to another value upstream", func(t *testing.T, doc map[string]any) {
			docNode(t, doc, "COL-1")["assignee"] = "agent-b"
		}, nil, []string{"COL-1"}},
		{"node_type left empty (import derives it from depth)", func(t *testing.T, doc map[string]any) {
			docNode(t, doc, "COL-2")["node_type"] = ""
		}, nil, []string{"COL-2"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file := editExport(t, local, func(doc map[string]any) { tt.edit(t, doc) })
			diff, err := sqlite.DiffReplace(local, file)
			require.NoError(t, err)
			assert.False(t, diff.Lossy(), "losses: %+v", diff.Losses)
			assert.Empty(t, diff.Losses)
			assert.Equal(t, tt.wantAdded, diff.Added)
			assert.Equal(t, tt.wantUpdated, diff.Updated)
			assert.Empty(t, diff.Removed)
		})
	}
}

// TestDiffReplace_EveryBlankableField_ReportsLoss verifies every node field
// that holds a value locally is reported, by name, when a file that does
// not descend from the local copy (here schema 1.0.0) leaves it out, and is
// not reported when a descending file does (a deliberate clear); the
// numeric fields and node_type (derived from depth) never are
// (MTIX-95.31.2).
func TestDiffReplace_EveryBlankableField_ReportsLoss(t *testing.T) {
	local := diffFixture(t)
	flagged := []string{
		"project", "title", "description", "prompt", "acceptance", "issue_type", "labels",
		"status", "assignee", "creator", "agent_state", "content_hash", "created_at",
		"updated_at", "closed_at", "defer_until", "deleted_at", "uid", "previous_status", "estimate_min",
		"actual_min", "code_refs", "commit_refs", "invalidated_at", "invalidated_by",
		"invalidation_reason", "deleted_by", "metadata", "session_id",
	}
	for _, key := range flagged {
		t.Run("flags "+key, func(t *testing.T) {
			file := editExport(t, local, func(doc map[string]any) {
				doc["schema_version"] = "1.0.0"
				n := docNode(t, doc, "COL-1")
				require.NotEmpty(t, n[key], "fixture: COL-1 holds %s", key)
				delete(n, key)
			})
			diff, err := sqlite.DiffReplace(local, file)
			require.NoError(t, err)
			loss := lossOf(diff, "COL-1")
			require.NotNil(t, loss)
			assert.Equal(t, []string{key}, loss.Fields)
		})
		if key == "updated_at" {
			continue // a copy without updated_at is never known to be current
		}
		t.Run("accepts "+key+" cleared in a descending file", func(t *testing.T) {
			file := editExport(t, local, func(doc map[string]any) {
				delete(docNode(t, doc, "COL-1"), key)
			})
			diff, err := sqlite.DiffReplace(local, file)
			require.NoError(t, err)
			assert.Nil(t, lossOf(diff, "COL-1"))
			assert.Equal(t, []string{"COL-1"}, diff.Updated)
		})
	}
	for _, key := range []string{"node_type", "depth", "seq", "priority", "progress", "weight"} {
		t.Run("never flags "+key, func(t *testing.T) {
			file := editExport(t, local, func(doc map[string]any) {
				doc["schema_version"] = "1.0.0"
				delete(docNode(t, doc, "COL-1"), key)
			})
			diff, err := sqlite.DiffReplace(local, file)
			require.NoError(t, err)
			assert.Nil(t, lossOf(diff, "COL-1"))
		})
	}
}

// TestDiffReplace_AnnotationWithoutID_NamedByAuthorAndTime verifies an
// annotation without an id, keyed by its author, text and time as merge
// import keys it, is named by its author and time when the file lacks it.
func TestDiffReplace_AnnotationWithoutID_NamedByAuthorAndTime(t *testing.T) {
	fixture := diffFixture(t)
	local := editExport(t, fixture, func(doc map[string]any) {
		n := docNode(t, doc, "COL-2")
		n["annotations"] = append(docAnnotations(t, doc, "COL-2"), map[string]any{
			"author": "legacy", "text": "no id", "created_at": "2026-09-01T10:05:00Z", "resolved": false,
		})
	})

	diff, err := sqlite.DiffReplace(local, fixture)
	require.NoError(t, err)
	loss := lossOf(diff, "COL-2")
	require.NotNil(t, loss)
	assert.Equal(t, []string{"by legacy at 2026-09-01T10:05:00Z"}, loss.Annotations)

	same, err := sqlite.DiffReplace(local, local)
	require.NoError(t, err)
	assert.False(t, same.Lossy(), "the same id-less annotation on both sides is not a loss")
}

// TestDiffReplace_DeliberateClearByTeammate_NotALoss verifies the fields a
// teammate clears on purpose apply: the file's copy holds every local
// activity entry of the node, and the new entry the clearing command
// appended, so it descends from the local copy (MTIX-95.31.2).
func TestDiffReplace_DeliberateClearByTeammate_NotALoss(t *testing.T) {
	local := diffFixture(t)
	teammateEntry := map[string]any{
		"id": "act-teammate", "type": "status_change", "author": "teammate",
		"text": "cleared upstream", "created_at": "2026-09-01T12:00:00Z",
	}
	tests := []struct {
		name   string
		fields []string
	}{
		{"unclaim clears assignee and agent_state", []string{"assignee", "agent_state"}},
		{"reopen clears closed_at", []string{"closed_at"}},
		{"undefer clears defer_until", []string{"defer_until"}},
		{"undelete clears deleted_at and deleted_by", []string{"deleted_at", "deleted_by"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file := editExport(t, local, func(doc map[string]any) {
				n := docNode(t, doc, "COL-1")
				for _, f := range tt.fields {
					require.NotEmpty(t, n[f], "fixture: COL-1 holds %s", f)
					n[f] = ""
				}
				entries, ok := n["activity"].([]any)
				require.True(t, ok)
				n["activity"] = append(entries, teammateEntry)
			})
			diff, err := sqlite.DiffReplace(local, file)
			require.NoError(t, err)
			assert.False(t, diff.Lossy(), "losses: %+v", diff.Losses)
			assert.Equal(t, []string{"COL-1"}, diff.Updated)
		})
	}
}

// TestDiffReplace_OlderCopy_DoesNotDescend verifies a copy that holds every
// local activity entry still does not descend when its updated_at is older
// than the local one: mtix update and mtix delete write no activity entry,
// so only the time shows the copy predates them (MTIX-95.31.2, round 3).
func TestDiffReplace_OlderCopy_DoesNotDescend(t *testing.T) {
	local := diffFixture(t)
	tests := []struct {
		name      string
		updatedAt string
		wantLoss  bool
	}{
		{"older copy", "2020-01-01T00:00:00Z", true},
		{"unreadable time", "not a time", true},
		{"same time", "", false},
		{"newer copy", "2099-01-01T00:00:00Z", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file := editExport(t, local, func(doc map[string]any) {
				n := docNode(t, doc, "COL-1")
				n["assignee"] = ""
				if tt.updatedAt != "" {
					n["updated_at"] = tt.updatedAt
				}
			})
			diff, err := sqlite.DiffReplace(local, file)
			require.NoError(t, err)
			if !tt.wantLoss {
				assert.Nil(t, lossOf(diff, "COL-1"), "a current copy's cleared field applies")
				return
			}
			loss := lossOf(diff, "COL-1")
			require.NotNil(t, loss)
			assert.Equal(t, []string{"assignee"}, loss.Fields)
		})
	}
}

// depFixture returns the export of a store holding DEP-1 and DEP-2, live
// and annotated, and the dependency DEP-1 related DEP-2.
func depFixture(t *testing.T) *sqlite.ExportData {
	t.Helper()
	s := newTestStore(t)
	createAnnotatedNode(t, s, "DEP-1", 1)
	createAnnotatedNode(t, s, "DEP-2", 2)
	require.NoError(t, s.AddDependency(context.Background(), &model.Dependency{
		FromID: "DEP-1", ToID: "DEP-2", DepType: model.DepTypeRelated,
	}))
	local, err := s.Export(context.Background(), "", "")
	require.NoError(t, err)
	require.Len(t, local.Dependencies, 1)
	return local
}

// TestDiffReplace_Dependencies_LossAndAdditions verifies a local dependency
// the file lacks is a loss of the node it starts from, and a dependency
// only the file holds is an addition (MTIX-95.31.2).
func TestDiffReplace_Dependencies_LossAndAdditions(t *testing.T) {
	local := depFixture(t)
	related := map[string]any{"from_id": "DEP-2", "to_id": "DEP-1", "dep_type": "related", "created_at": "2026-09-01T10:00:00Z"}
	tests := []struct {
		name        string
		edit        func(doc map[string]any)
		wantLoss    []string
		wantRemoved []string
		wantAdded   []string
	}{
		{"file lacks the dependency", func(doc map[string]any) { doc["dependencies"] = []any{} },
			[]string{"related DEP-2"}, []string{"DEP-1 related DEP-2"}, nil},
		{"file adds a dependency", func(doc map[string]any) {
			deps, _ := doc["dependencies"].([]any)
			doc["dependencies"] = append(deps, related)
		}, nil, nil, []string{"DEP-2 related DEP-1"}},
		{"file changes the dependency type", func(doc map[string]any) {
			deps, _ := doc["dependencies"].([]any)
			dep, _ := deps[0].(map[string]any)
			dep["dep_type"] = "duplicates"
		}, []string{"related DEP-2"}, []string{"DEP-1 related DEP-2"}, []string{"DEP-1 duplicates DEP-2"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file := editExport(t, local, tt.edit)
			diff, err := sqlite.DiffReplace(local, file)
			require.NoError(t, err)
			assert.Equal(t, tt.wantRemoved, diff.DepsRemoved)
			assert.Equal(t, tt.wantAdded, diff.DepsAdded)
			if tt.wantLoss == nil {
				assert.False(t, diff.Lossy(), "losses: %+v", diff.Losses)
				return
			}
			loss := lossOf(diff, "DEP-1")
			require.NotNil(t, loss)
			assert.Equal(t, sqlite.NodeLoss{NodeID: "DEP-1", Dependencies: tt.wantLoss}, *loss)
		})
	}
}

// TestDiffReplace_NilExport_ReturnsError verifies a missing side is an
// error, never an empty (and so harmless-looking) diff.
func TestDiffReplace_NilExport_ReturnsError(t *testing.T) {
	local := diffFixture(t)
	for _, tt := range []struct {
		name        string
		local, file *sqlite.ExportData
	}{
		{"no local export", nil, local},
		{"no file", local, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := sqlite.DiffReplace(tt.local, tt.file)
			require.Error(t, err)
		})
	}
}
