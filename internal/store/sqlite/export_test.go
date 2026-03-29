// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// TestExport_ProducesValidJSON verifies export creates valid JSON structure per FR-7.8.
func TestExport_ProducesValidJSON(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "EX-1", Project: "EX", Depth: 0, Seq: 1, Title: "Export test",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "h1", CreatedAt: now, UpdatedAt: now,
	}))

	data, err := s.Export(ctx, "EX", "0.1.0")
	require.NoError(t, err)
	require.NotNil(t, data)

	// Verify basic structure.
	assert.Equal(t, 1, data.Version)
	assert.Equal(t, "EX", data.Project)
	assert.Equal(t, "0.1.0", data.MtixVersion)
	assert.NotEmpty(t, data.ExportedAt)
	assert.NotEmpty(t, data.Checksum)

	// Verify it's JSON-marshalable.
	jsonBytes, err := json.Marshal(data)
	require.NoError(t, err)
	assert.True(t, json.Valid(jsonBytes), "export should produce valid JSON")
}

// TestExport_IncludesSchemaVersion verifies schema_version field exists per FR-15.1/FR-15.2g.
// The schema_version enables auto-import to reject incompatible export files.
func TestExport_IncludesSchemaVersion(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "EX-1", Project: "EX", Depth: 0, Seq: 1, Title: "Schema version test",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "h1", CreatedAt: now, UpdatedAt: now,
	}))

	data, err := s.Export(ctx, "EX", "0.1.0")
	require.NoError(t, err)

	// schema_version must be a semver string.
	assert.Equal(t, "1.0.0", data.SchemaVersion, "export must include schema_version per FR-15.2g")

	// Verify it appears in serialized JSON.
	jsonBytes, err := json.Marshal(data)
	require.NoError(t, err)
	assert.Contains(t, string(jsonBytes), `"schema_version":"1.0.0"`)
}

// TestExport_ChecksumIsCorrect verifies checksum is reproducible and valid per FR-7.8.
func TestExport_ChecksumIsCorrect(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "EX-1", Project: "EX", Depth: 0, Seq: 1, Title: "Checksum test",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "h1", CreatedAt: now, UpdatedAt: now,
	}))

	data, err := s.Export(ctx, "EX", "0.1.0")
	require.NoError(t, err)

	// Verify checksum independently.
	valid, err := sqlite.VerifyExportChecksum(data)
	require.NoError(t, err)
	assert.True(t, valid, "checksum should verify correctly")
}

// TestExport_NodeCountMatchesArray verifies node_count matches actual nodes per FR-7.8.
func TestExport_NodeCountMatchesArray(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	for i := 1; i <= 3; i++ {
		require.NoError(t, s.CreateNode(ctx, &model.Node{
			ID: fmt.Sprintf("EX-%d", i), Project: "EX", Depth: 0, Seq: i,
			Title: fmt.Sprintf("Node %d", i),
			Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
			NodeType: model.NodeTypeIssue, ContentHash: fmt.Sprintf("h%d", i),
			CreatedAt: now.Add(time.Duration(i) * time.Second),
			UpdatedAt: now.Add(time.Duration(i) * time.Second),
		}))
	}

	data, err := s.Export(ctx, "EX", "0.1.0")
	require.NoError(t, err)
	assert.Equal(t, len(data.Nodes), data.NodeCount)
	assert.Equal(t, 3, data.NodeCount)
}

// TestExport_IncludesSoftDeleted verifies soft-deleted nodes are included per FR-7.8.
func TestExport_IncludesSoftDeleted(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "EX-1", Project: "EX", Depth: 0, Seq: 1, Title: "Active node",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "h1", CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "EX-2", Project: "EX", Depth: 0, Seq: 2, Title: "Will be deleted",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "h2",
		CreatedAt: now.Add(time.Second), UpdatedAt: now.Add(time.Second),
	}))

	// Soft-delete one node.
	require.NoError(t, s.DeleteNode(ctx, "EX-2", false, "tester"))

	data, err := s.Export(ctx, "EX", "0.1.0")
	require.NoError(t, err)
	assert.Equal(t, 2, data.NodeCount, "soft-deleted node should be in export")

	// Verify the deleted node is present.
	found := false
	for _, n := range data.Nodes {
		if n.ID == "EX-2" {
			found = true
			assert.NotEmpty(t, n.DeletedAt, "deleted_at should be set")
		}
	}
	assert.True(t, found, "soft-deleted node EX-2 should be in export")
}

// TestExport_EmptyDB_ReturnsEmptyExport verifies empty database export.
func TestExport_EmptyDB_ReturnsEmptyExport(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	data, err := s.Export(ctx, "EMPTY", "0.1.0")
	require.NoError(t, err)
	assert.Equal(t, 0, data.NodeCount)
	assert.Empty(t, data.Nodes)
}

// TestExport_NodesAreSortedByID verifies canonical sort order for checksum.
func TestExport_NodesAreSortedByID(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Create in non-alphabetical order.
	for _, id := range []string{"EX-3", "EX-1", "EX-2"} {
		require.NoError(t, s.CreateNode(ctx, &model.Node{
			ID: id, Project: "EX", Depth: 0, Seq: 1, Title: "Node " + id,
			Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
			NodeType: model.NodeTypeIssue, ContentHash: "h-" + id,
			CreatedAt: now, UpdatedAt: now,
		}))
	}

	data, err := s.Export(ctx, "EX", "0.1.0")
	require.NoError(t, err)

	// Verify sorted order.
	for i := 1; i < len(data.Nodes); i++ {
		assert.Less(t, data.Nodes[i-1].ID, data.Nodes[i].ID,
			"nodes should be sorted by ID")
	}
}

// TestVerifyExportChecksum_NilData_ReturnsError verifies nil handling.
func TestVerifyExportChecksum_NilData_ReturnsError(t *testing.T) {
	_, err := sqlite.VerifyExportChecksum(nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, model.ErrInvalidInput)
}

// TestExport_WithDependencies_ExportsDeps verifies dependency export per FR-7.8.
func TestExport_WithDependencies_ExportsDeps(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Create two nodes and a dependency between them.
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "EX-1", Project: "EX", Depth: 0, Seq: 1, Title: "Node A",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "h1", CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "EX-2", Project: "EX", Depth: 0, Seq: 2, Title: "Node B",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "h2",
		CreatedAt: now.Add(time.Second), UpdatedAt: now.Add(time.Second),
	}))

	dep := &model.Dependency{
		FromID: "EX-1", ToID: "EX-2", DepType: model.DepTypeRelated,
		CreatedAt: now, CreatedBy: "pm-1",
	}
	require.NoError(t, s.AddDependency(ctx, dep))

	data, err := s.Export(ctx, "EX", "0.1.0")
	require.NoError(t, err)
	require.Len(t, data.Dependencies, 1)
	assert.Equal(t, "EX-1", data.Dependencies[0].FromID)
	assert.Equal(t, "EX-2", data.Dependencies[0].ToID)
}

// TestExport_WithAgentsAndSessions_ExportsAll verifies agent/session export per FR-7.8.
func TestExport_WithAgentsAndSessions_ExportsAll(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	nowStr := now.Format(time.RFC3339)

	// Insert an agent directly (agents table).
	_, err := s.WriteDB().ExecContext(ctx,
		`INSERT INTO agents (agent_id, project, state, last_heartbeat)
		 VALUES (?, ?, ?, ?)`,
		"agent-001", "EX", "idle", nowStr,
	)
	require.NoError(t, err)

	// Insert a session directly (sessions table).
	_, err = s.WriteDB().ExecContext(ctx,
		`INSERT INTO sessions (id, agent_id, project, started_at, status)
		 VALUES (?, ?, ?, ?, ?)`,
		"sess-001", "agent-001", "EX", nowStr, "active",
	)
	require.NoError(t, err)

	data, err := s.Export(ctx, "EX", "0.1.0")
	require.NoError(t, err)
	require.Len(t, data.Agents, 1)
	assert.Equal(t, "agent-001", data.Agents[0].AgentID)
	require.Len(t, data.Sessions, 1)
	assert.Equal(t, "sess-001", data.Sessions[0].ID)
}

// TestExport_DependenciesSortedCanonically verifies dep sort order for checksum.
func TestExport_DependenciesSortedCanonically(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Create three nodes.
	for i := 1; i <= 3; i++ {
		require.NoError(t, s.CreateNode(ctx, &model.Node{
			ID: fmt.Sprintf("EX-%d", i), Project: "EX", Depth: 0, Seq: i,
			Title: fmt.Sprintf("Node %d", i),
			Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
			NodeType: model.NodeTypeIssue, ContentHash: fmt.Sprintf("h%d", i),
			CreatedAt: now.Add(time.Duration(i) * time.Second),
			UpdatedAt: now.Add(time.Duration(i) * time.Second),
		}))
	}

	// Add deps in non-canonical order: EX-2->EX-3, then EX-1->EX-3.
	require.NoError(t, s.AddDependency(ctx, &model.Dependency{
		FromID: "EX-2", ToID: "EX-3", DepType: model.DepTypeRelated,
		CreatedAt: now, CreatedBy: "pm-1",
	}))
	require.NoError(t, s.AddDependency(ctx, &model.Dependency{
		FromID: "EX-1", ToID: "EX-3", DepType: model.DepTypeRelated,
		CreatedAt: now, CreatedBy: "pm-1",
	}))

	data, err := s.Export(ctx, "EX", "0.1.0")
	require.NoError(t, err)
	require.Len(t, data.Dependencies, 2)

	// Should be sorted by from_id: EX-1 before EX-2.
	assert.Equal(t, "EX-1", data.Dependencies[0].FromID)
	assert.Equal(t, "EX-2", data.Dependencies[1].FromID)
}

// TestVerifyExportChecksum_TamperedData_ReturnsFalse verifies tamper detection.
func TestVerifyExportChecksum_TamperedData_ReturnsFalse(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "EX-1", Project: "EX", Depth: 0, Seq: 1, Title: "Tamper test",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "h1", CreatedAt: now, UpdatedAt: now,
	}))

	data, err := s.Export(ctx, "EX", "0.1.0")
	require.NoError(t, err)

	// Tamper with a node title after export.
	data.Nodes[0].Title = "TAMPERED"

	valid, err := sqlite.VerifyExportChecksum(data)
	require.NoError(t, err)
	assert.False(t, valid, "tampered export should fail checksum verification")
}
