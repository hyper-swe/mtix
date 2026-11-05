// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func boardState(uid, id, fp string, deleted bool, events ...string) sqlite.BoardNodeState {
	return sqlite.BoardNodeState{UID: uid, ID: id, Fingerprint: fp, Deleted: deleted, EventIDs: events}
}

// TestBoardNodesHubLacks pins the node proof of the enrolment: a local node
// passes only when the hub holds its uid and either the contents match or the
// hub has moved past it by an event the local journal lacks.
func TestBoardNodesHubLacks(t *testing.T) {
	tests := []struct {
		name       string
		local, hub []sqlite.BoardNodeState
		want       []string
	}{
		{"identical", []sqlite.BoardNodeState{boardState("u1", "T-1", "a", false, "e1")},
			[]sqlite.BoardNodeState{boardState("u1", "T-1", "a", false, "e1")}, nil},
		{"hub lacks the uid", []sqlite.BoardNodeState{boardState("u1", "T-1", "a", false, "e1")},
			[]sqlite.BoardNodeState{boardState("u2", "T-1", "a", false, "e1")}, []string{"T-1"}},
		{"same display id under another uid is another task", []sqlite.BoardNodeState{boardState("u1", "T-1", "a", false)},
			[]sqlite.BoardNodeState{boardState("u2", "T-1", "a", false)}, []string{"T-1"}},
		{"journal-less node the hub holds with equal content", []sqlite.BoardNodeState{boardState("u1", "T-1", "a", false)},
			[]sqlite.BoardNodeState{boardState("u1", "T-1", "a", false, "e1")}, nil},
		{"journal-less node whose content differs is never explained by the hub being ahead", []sqlite.BoardNodeState{boardState("u1", "T-1", "a", false)},
			[]sqlite.BoardNodeState{boardState("u1", "T-1", "b", false, "e1")}, []string{"T-1"}},
		{"content differs and the hub has no newer event: a local change nobody journalled",
			[]sqlite.BoardNodeState{boardState("u1", "T-1", "a", false, "e1")},
			[]sqlite.BoardNodeState{boardState("u1", "T-1", "b", false, "e1")}, []string{"T-1"}},
		{"hub holds a newer event: its copy wins, whatever differs",
			[]sqlite.BoardNodeState{boardState("u1", "T-1", "a", false, "e1")},
			[]sqlite.BoardNodeState{boardState("u1", "T-1", "b", false, "e1", "e2")}, nil},
		{"deleted on the hub with a newer event is fine",
			[]sqlite.BoardNodeState{boardState("u1", "T-1", "a", false, "e1")},
			[]sqlite.BoardNodeState{boardState("u1", "T-1", "a", true, "e1", "e2")}, nil},
		{"deleted locally only: the deletion is a local change the hub lacks",
			[]sqlite.BoardNodeState{boardState("u1", "T-1", "a", true, "e1")},
			[]sqlite.BoardNodeState{boardState("u1", "T-1", "a", false, "e1")}, []string{"T-1"}},
		{"node without a uid matches nothing on the hub", []sqlite.BoardNodeState{boardState("", "T-1", "a", false)},
			[]sqlite.BoardNodeState{boardState("u1", "T-1", "a", false)}, []string{"T-1"}},
		{"ids come back sorted", []sqlite.BoardNodeState{boardState("u2", "T-2", "a", false), boardState("u1", "T-1", "a", false)}, nil, []string{"T-1", "T-2"}},
		{"empty store", nil, []sqlite.BoardNodeState{boardState("u1", "T-1", "a", false)}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, service.BoardNodesHubLacks(tt.local, tt.hub))
		})
	}
}

func boardStore(t *testing.T) *sqlite.Store {
	t.Helper()
	st, err := sqlite.New(filepath.Join(t.TempDir(), "data"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	for i, id := range []string{"TEST-1", "TEST-2"} {
		n := &model.Node{ID: id, Project: "TEST", Seq: i + 1, Title: "task " + id, NodeType: model.NodeTypeForDepth(0),
			Priority: model.PriorityMedium, Status: model.StatusOpen, Weight: 1.0, Creator: "c", CreatedAt: now, UpdatedAt: now}
		n.ContentHash = n.ComputeHash()
		require.NoError(t, st.CreateNode(context.Background(), n))
	}
	return st
}

// exportRoundTrip is a board file as the store would have written it.
func exportRoundTrip(t *testing.T, st *sqlite.Store) *sqlite.ExportData {
	t.Helper()
	exp, err := st.Export(context.Background(), "", "")
	require.NoError(t, err)
	body, err := json.Marshal(exp)
	require.NoError(t, err)
	var out sqlite.ExportData
	require.NoError(t, json.Unmarshal(body, &out))
	return &out
}

// TestBoardFileDifferences compares by content: the store's own export has no
// difference, and each edit a person or tool could make to the file shows.
func TestBoardFileDifferences(t *testing.T) {
	ctx := context.Background()
	edits := []struct {
		name string
		edit func(t *testing.T, st *sqlite.Store, file *sqlite.ExportData)
		want []string
	}{
		{"identical", func(*testing.T, *sqlite.Store, *sqlite.ExportData) {}, nil},
		{"exported_at and checksum are not board content", func(_ *testing.T, _ *sqlite.Store, f *sqlite.ExportData) {
			f.ExportedAt, f.Checksum = "2001-01-01T00:00:00Z", "stale"
		}, nil},
		{"file lacks a node the store holds", func(_ *testing.T, _ *sqlite.Store, f *sqlite.ExportData) { f.Nodes = f.Nodes[:1] }, []string{"TEST-2"}},
		{"file holds a node the store lacks", func(_ *testing.T, st *sqlite.Store, _ *sqlite.ExportData) {
			_, err := st.WriteDB().ExecContext(ctx, `DELETE FROM nodes WHERE id = 'TEST-2'`)
			require.NoError(t, err)
		}, []string{"TEST-2"}},
		{"title edited in the file", func(_ *testing.T, _ *sqlite.Store, f *sqlite.ExportData) { f.Nodes[0].Title = "edited" }, []string{"TEST-1"}},
		{"status edited in the file", func(_ *testing.T, _ *sqlite.Store, f *sqlite.ExportData) { f.Nodes[1].Status = "done" }, []string{"TEST-2"}},
		{"priority edited in the file", func(_ *testing.T, _ *sqlite.Store, f *sqlite.ExportData) { f.Nodes[0].Priority = 1 }, []string{"TEST-1"}},
		{"assignee edited in the file", func(_ *testing.T, _ *sqlite.Store, f *sqlite.ExportData) { f.Nodes[0].Assignee = "someone" }, []string{"TEST-1"}},
		{"updated_at only", func(_ *testing.T, _ *sqlite.Store, f *sqlite.ExportData) {
			f.Nodes[0].UpdatedAt = "2030-01-01T00:00:00Z"
		}, []string{"TEST-1"}},
		{"uid replaced: another task under the same id", func(_ *testing.T, _ *sqlite.Store, f *sqlite.ExportData) { f.Nodes[0].UID = "other-uid" }, []string{"TEST-1"}},
		{"dependency only in the store", func(t *testing.T, st *sqlite.Store, _ *sqlite.ExportData) {
			require.NoError(t, st.AddDependency(ctx, &model.Dependency{FromID: "TEST-1", ToID: "TEST-2", DepType: model.DepTypeBlocks, CreatedAt: time.Now().UTC()}))
		}, []string{"TEST-1", "TEST-2"}}, // the blocked node's own status changes with it
	}
	for _, tt := range edits {
		t.Run(tt.name, func(t *testing.T) {
			st := boardStore(t)
			file := exportRoundTrip(t, st)
			tt.edit(t, st, file)
			local, err := st.Export(ctx, "", "")
			require.NoError(t, err)

			got, err := service.BoardFileDifferences(file, local)

			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
