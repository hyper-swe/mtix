// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// The conflict baseline mtix 0.3.0 wrote (MTIX-95.31.11, FR-15.2h), rebuilt
// the way 0.3.0 computed it, independently of the code under test: the node
// type and query below are copied from v0.3.0-beta
// (internal/store/sqlite/export.go), and v030BaselineHash is its
// computeDBHash (internal/service/sync_service.go). 0.3.0 exported the 1.0.0
// form without any uid key: nodes had no uid column yet. Its dependency,
// agent and session types, its other queries and its checksum are those of
// 0.5.3 (sync_conflict_baseline_v053_test.go), unchanged since 0.3.0.
package service_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// v030ExportData is ExportData of mtix 0.3.0, field for field.
type v030ExportData struct {
	Version       int                 `json:"version"`
	SchemaVersion string              `json:"schema_version"`
	ExportedAt    string              `json:"exported_at"`
	MtixVersion   string              `json:"mtix_version"`
	Project       string              `json:"project"`
	Nodes         []v030ExportNode    `json:"nodes"`
	Dependencies  []v053ExportDep     `json:"dependencies"`
	Agents        []v053ExportAgent   `json:"agents"`
	Sessions      []v053ExportSession `json:"sessions"`
	NodeCount     int                 `json:"node_count"`
	Checksum      string              `json:"checksum"`
}

// v030ExportNode is exportNode of mtix 0.3.0 (schema 1.0.0, no uid), field
// for field.
type v030ExportNode struct {
	ID          string  `json:"id"`
	ParentID    string  `json:"parent_id"`
	Depth       int     `json:"depth"`
	Seq         int     `json:"seq"`
	Project     string  `json:"project"`
	Title       string  `json:"title"`
	Description string  `json:"description"`
	Prompt      string  `json:"prompt"`
	Acceptance  string  `json:"acceptance"`
	NodeType    string  `json:"node_type"`
	IssueType   string  `json:"issue_type"`
	Priority    int     `json:"priority"`
	Labels      string  `json:"labels"`
	Status      string  `json:"status"`
	Progress    float64 `json:"progress"`
	Assignee    string  `json:"assignee"`
	Creator     string  `json:"creator"`
	AgentState  string  `json:"agent_state"`
	Weight      float64 `json:"weight"`
	ContentHash string  `json:"content_hash"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
	ClosedAt    string  `json:"closed_at,omitempty"`
	DeferUntil  string  `json:"defer_until,omitempty"`
	DeletedAt   string  `json:"deleted_at,omitempty"`
}

// v030NodeSelectSQL is exportNodeSelectSQL of mtix 0.3.0: the 1.0.0
// columns, uid not yet among them.
const v030NodeSelectSQL = `SELECT id, COALESCE(parent_id,''), depth, seq, project,
		        title, COALESCE(description,''), COALESCE(prompt,''),
		        COALESCE(acceptance,''), COALESCE(node_type,'auto'),
		        COALESCE(issue_type,''), priority, COALESCE(labels,'[]'),
		        status, progress, COALESCE(assignee,''), COALESCE(creator,''),
		        COALESCE(agent_state,''), weight, COALESCE(content_hash,''),
		        created_at, updated_at, COALESCE(closed_at,''),
		        COALESCE(defer_until,''), COALESCE(deleted_at,'')
		 FROM nodes ORDER BY id`

// The conflict baselines 0.3.0 wrote for the fixture (v030BaselineHash),
// pinned: the fixture as newUpgradeFixture builds it, and with PROJ-2's
// description holding invalid UTF-8.
const (
	v030FixtureBaseline            = "05d35fa3a6ae8185f8e917829f9667f20073f3f2aa8a3e96acfc39fc82342ba5"
	v030FixtureBaselineInvalidUTF8 = "6252b03b965c152c715b43cbee1589b33fadcfe84c51c1e5963245385759aecf"
)

// v030BaselineHash returns the conflict baseline mtix 0.3.0 wrote for st:
// computeDBHash of v0.3.0-beta, over the store read with 0.3.0's queries.
func v030BaselineHash(t *testing.T, st *sqlite.Store) string {
	t.Helper()
	data := v030Export(t, st.ReadDB())
	data.ExportedAt = "" // 0.3.0 zeroed the export time before hashing
	raw, err := json.Marshal(data)
	require.NoError(t, err)
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

// v030Export is Store.Export of mtix 0.3.0 with project and mtix version
// empty, as computeDBHash called it.
func v030Export(t *testing.T, db *sql.DB) *v030ExportData {
	t.Helper()
	nodes := v030Nodes(t, db)
	deps := v053Deps(t, db)
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	sort.Slice(deps, func(i, j int) bool {
		if deps[i].FromID != deps[j].FromID {
			return deps[i].FromID < deps[j].FromID
		}
		return deps[i].ToID < deps[j].ToID
	})
	// computeExportChecksum of 0.3.0: the SHA-256 of the first JSON encoding
	// of the sorted nodes and dependencies.
	canonical, err := json.Marshal(struct {
		Nodes []v030ExportNode `json:"nodes"`
		Deps  []v053ExportDep  `json:"deps"`
	}{Nodes: nodes, Deps: deps})
	require.NoError(t, err)
	return &v030ExportData{
		Version: 1, SchemaVersion: "1.0.0", ExportedAt: "2026-06-01T08:00:00Z",
		Nodes: nodes, Dependencies: deps, Agents: v053Agents(t, db), Sessions: v053Sessions(t, db),
		NodeCount: len(nodes), Checksum: fmt.Sprintf("%x", sha256.Sum256(canonical)),
	}
}

// v030Nodes reads the nodes as mtix 0.3.0 exported them, node_type
// normalized to the depth's type.
func v030Nodes(t *testing.T, db *sql.DB) []v030ExportNode {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), v030NodeSelectSQL)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	var nodes []v030ExportNode
	for rows.Next() {
		var n v030ExportNode
		require.NoError(t, rows.Scan(
			&n.ID, &n.ParentID, &n.Depth, &n.Seq, &n.Project,
			&n.Title, &n.Description, &n.Prompt, &n.Acceptance, &n.NodeType,
			&n.IssueType, &n.Priority, &n.Labels, &n.Status, &n.Progress,
			&n.Assignee, &n.Creator, &n.AgentState, &n.Weight, &n.ContentHash,
			&n.CreatedAt, &n.UpdatedAt, &n.ClosedAt, &n.DeferUntil, &n.DeletedAt,
		))
		n.NodeType = string(model.NodeTypeForDepth(n.Depth))
		nodes = append(nodes, n)
	}
	require.NoError(t, rows.Err())
	return nodes
}

// TestV030BaselineHash_UpgradeFixture_MatchesPinnedHash pins the baselines
// 0.3.0 wrote for the fixture, so a change to the fixture or to the
// reconstruction of 0.3.0's hash cannot pass unnoticed.
func TestV030BaselineHash_UpgradeFixture_MatchesPinnedHash(t *testing.T) {
	tests := []struct {
		name  string
		setup upgradeSetup
		want  string
	}{
		{"valid UTF-8", upgradeSetup{}, v030FixtureBaseline},
		{"description holding invalid UTF-8", upgradeSetup{invalidUTF8: true}, v030FixtureBaselineInvalidUTF8},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newUpgradeFixture(t, tt.setup)
			assert.Equal(t, tt.want, v030BaselineHash(t, f.store))
		})
	}
}
