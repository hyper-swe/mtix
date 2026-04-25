// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

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

// insertLegacyNode bypasses the service layer to write a node with a
// non-canonical node_type — simulating data created by pre-v0.1.1-beta
// versions where the depth-to-type mapping was inverted.
func insertLegacyNode(t *testing.T, s *sqlite.Store, id, parentID, project string, depth, seq int, legacyType string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	parentVal := any(nil)
	if parentID != "" {
		parentVal = parentID
	}
	_, err := s.WriteDB().ExecContext(context.Background(),
		`INSERT INTO nodes (id, parent_id, depth, seq, project,
		  title, status, priority, node_type, weight, content_hash,
		  created_at, updated_at)
		 VALUES (?,?,?,?,?, ?,?,?,?,?,?, ?,?)`,
		id, parentVal, depth, seq, project,
		"Legacy "+id, "open", 3, legacyType, 1.0, "h-"+id,
		now, now,
	)
	require.NoError(t, err, "failed to insert legacy node %s", id)
}

// TestExport_NodeTypeDerivedFromDepth verifies that exportNodes overrides
// any stored node_type with the depth-derived value per MTIX-12. This
// brings export into symmetry with import (which already depth-derives
// per MTIX-7), making round-trip idempotent.
//
// Setup: insert nodes with INTENTIONALLY WRONG stored node_type
// (depth 0 with stored "story", depth 1 with stored "epic" — the
// pre-v0.1.1-beta convention). Export must coerce these to the canonical
// depth-derived values.
func TestExport_NodeTypeDerivedFromDepth(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Legacy DB state: depth 0 stored as "story" (old convention).
	insertLegacyNode(t, s, "ACH-1", "", "ACH", 0, 1, "story")
	// Legacy DB state: depth 1 stored as "epic" (old convention).
	insertLegacyNode(t, s, "ACH-1.1", "ACH-1", "ACH", 1, 1, "epic")
	// depth 2: should always be "issue".
	insertLegacyNode(t, s, "ACH-1.1.1", "ACH-1.1", "ACH", 2, 1, "micro")

	data, err := s.Export(ctx, "ACH", "0.1.4")
	require.NoError(t, err)
	require.Len(t, data.Nodes, 3)

	// Re-marshal to access the unexported exportNode fields by name.
	jsonBytes, err := json.Marshal(data)
	require.NoError(t, err)
	var out struct {
		Nodes []struct {
			ID       string `json:"id"`
			Depth    int    `json:"depth"`
			NodeType string `json:"node_type"`
		} `json:"nodes"`
	}
	require.NoError(t, json.Unmarshal(jsonBytes, &out))

	byID := make(map[string]string, len(out.Nodes))
	for _, n := range out.Nodes {
		byID[n.ID] = n.NodeType
	}

	assert.Equal(t, "epic", byID["ACH-1"],
		"depth-0 node MUST export as epic regardless of stored value (was: story)")
	assert.Equal(t, "story", byID["ACH-1.1"],
		"depth-1 node MUST export as story regardless of stored value (was: epic)")
	assert.Equal(t, "issue", byID["ACH-1.1.1"],
		"depth-2 node MUST export as issue regardless of stored value (was: micro)")
}

// TestExport_DeepNodes_NodeTypeMicro verifies depth >= 3 exports as micro.
func TestExport_DeepNodes_NodeTypeMicro(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	insertLegacyNode(t, s, "DPN-1", "", "DPN", 0, 1, "epic")
	insertLegacyNode(t, s, "DPN-1.1", "DPN-1", "DPN", 1, 1, "story")
	insertLegacyNode(t, s, "DPN-1.1.1", "DPN-1.1", "DPN", 2, 1, "issue")
	// depth 3+ stored as something else, should export as micro.
	insertLegacyNode(t, s, "DPN-1.1.1.1", "DPN-1.1.1", "DPN", 3, 1, "epic")
	insertLegacyNode(t, s, "DPN-1.1.1.1.1", "DPN-1.1.1.1", "DPN", 4, 1, "story")

	data, err := s.Export(ctx, "DPN", "0.1.4")
	require.NoError(t, err)

	jsonBytes, err := json.Marshal(data)
	require.NoError(t, err)
	var out struct {
		Nodes []struct {
			ID       string `json:"id"`
			NodeType string `json:"node_type"`
		} `json:"nodes"`
	}
	require.NoError(t, json.Unmarshal(jsonBytes, &out))

	byID := make(map[string]string, len(out.Nodes))
	for _, n := range out.Nodes {
		byID[n.ID] = n.NodeType
	}

	assert.Equal(t, "micro", byID["DPN-1.1.1.1"], "depth-3 must export as micro")
	assert.Equal(t, "micro", byID["DPN-1.1.1.1.1"], "depth-4 must export as micro")
}

// TestExportImport_RoundTrip_NodeTypeIdempotent is the core integration test
// for MTIX-12. The fix from MTIX-7 made import normalize node_type, but
// export still wrote raw DB values, making the round-trip non-idempotent
// for legacy DBs. After MTIX-12, both sides depth-derive, so:
//   1. Export legacy DB -> tasks.json with normalized values
//   2. Import that tasks.json -> DB with normalized values
//   3. Export again -> identical to step 1 (modulo exported_at)
func TestExportImport_RoundTrip_NodeTypeIdempotent(t *testing.T) {
	s1 := newTestStore(t)
	ctx := context.Background()

	// Legacy state with intentionally wrong node_types.
	insertLegacyNode(t, s1, "RT-1", "", "RT", 0, 1, "story")
	insertLegacyNode(t, s1, "RT-1.1", "RT-1", "RT", 1, 1, "epic")
	insertLegacyNode(t, s1, "RT-1.2", "RT-1", "RT", 1, 2, "issue")

	// First export — snapshot the node_type values BEFORE Import runs,
	// because Import mutates data.Nodes[i].NodeType in place via the
	// depth-derivation in insertExportNode (a separate quirk we don't
	// fix here — see import.go:232).
	data1, err := s1.Export(ctx, "RT", "0.1.4")
	require.NoError(t, err)
	t1 := snapshotNodeTypes(t, data1)

	// Import into a fresh store. (After this, data1.Nodes is mutated.)
	s2 := newTestStore(t)
	_, err = s2.Import(ctx, data1, sqlite.ImportModeReplace, false)
	require.NoError(t, err)

	// Second export from the imported store.
	data2, err := s2.Export(ctx, "RT", "0.1.4")
	require.NoError(t, err)
	t2 := snapshotNodeTypes(t, data2)

	require.Len(t, t1, 3)
	require.Len(t, t2, 3)
	assert.Equal(t, t1, t2,
		"export -> import -> export must produce the same node_type for every node (idempotent)")

	// Specifically verify the canonical values.
	assert.Equal(t, "epic", t2["RT-1"])
	assert.Equal(t, "story", t2["RT-1.1"])
	assert.Equal(t, "story", t2["RT-1.2"])
}

// snapshotNodeTypes captures node_type per id from the export, immediately,
// so subsequent Import calls (which mutate the input) don't poison the
// snapshot.
func snapshotNodeTypes(t *testing.T, d *sqlite.ExportData) map[string]string {
	t.Helper()
	jb, err := json.Marshal(d)
	require.NoError(t, err)
	var out struct {
		Nodes []struct {
			ID       string `json:"id"`
			NodeType string `json:"node_type"`
		} `json:"nodes"`
	}
	require.NoError(t, json.Unmarshal(jb, &out))
	m := make(map[string]string, len(out.Nodes))
	for _, n := range out.Nodes {
		m[n.ID] = n.NodeType
	}
	return m
}

// TestExport_NewlyCreatedNode_AlsoCanonical verifies that even though
// CreateNode already sets the canonical type, the export depth-derive
// pass is safe and idempotent for already-canonical data.
func TestExport_NewlyCreatedNode_AlsoCanonical(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Use the normal CreateNode path which already sets canonical type.
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "NC-1", Project: "NC", Depth: 0, Seq: 1, Title: "Canonical",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeForDepth(0), // explicitly canonical
		ContentHash: "h", CreatedAt: now, UpdatedAt: now,
	}))

	data, err := s.Export(ctx, "NC", "0.1.4")
	require.NoError(t, err)

	jsonBytes, err := json.Marshal(data)
	require.NoError(t, err)
	var out struct {
		Nodes []struct {
			ID       string `json:"id"`
			NodeType string `json:"node_type"`
		} `json:"nodes"`
	}
	require.NoError(t, json.Unmarshal(jsonBytes, &out))
	require.Len(t, out.Nodes, 1)
	assert.Equal(t, "epic", out.Nodes[0].NodeType,
		"already-canonical node still exports as canonical (idempotent)")
}
