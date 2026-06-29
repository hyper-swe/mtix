// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// MakeExportDep creates an exportDep for use in external tests.
func MakeExportDep(fromID, toID, depType, createdAt string) exportDep {
	return exportDep{
		FromID:    fromID,
		ToID:      toID,
		DepType:   depType,
		CreatedAt: createdAt,
	}
}

// RecomputeChecksumForTest recomputes and returns the checksum for
// modified export data in tests. Production code uses the exported
// RecomputeExportChecksum (export.go), which also stores the result.
func RecomputeChecksumForTest(t *testing.T, data *ExportData) string {
	t.Helper()
	checksum, err := computeExportChecksum(data.Nodes, data.Dependencies)
	require.NoError(t, err)
	return checksum
}

// TestExportNode is the subset of an export node tests typically set. The
// remaining columns default to sensible zero values; node_type is recomputed
// from depth on import, so it is intentionally omitted here.
type TestExportNode struct {
	ID          string
	ParentID    string
	Depth       int
	Seq         int
	Project     string
	Title       string
	ContentHash string
	UID         string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// MakeExportData assembles an ExportData (without a checksum) from the given
// nodes for use in external reconciliation tests. Call RecomputeChecksumForTest
// to set a valid checksum before importing.
func MakeExportData(project string, nodes ...TestExportNode) *ExportData {
	data := &ExportData{
		Version:       1,
		SchemaVersion: SchemaVersionV1,
		Project:       project,
		NodeCount:     len(nodes),
	}
	for _, n := range nodes {
		ts := n.CreatedAt.UTC().Format(time.RFC3339)
		data.Nodes = append(data.Nodes, exportNode{
			ID: n.ID, ParentID: n.ParentID, Depth: n.Depth, Seq: n.Seq,
			Project: n.Project, Title: n.Title, Priority: 2, Weight: 1.0,
			Status: "open", NodeType: string(model.NodeTypeForDepth(n.Depth)),
			ContentHash: n.ContentHash, UID: n.UID,
			CreatedAt: ts, UpdatedAt: n.UpdatedAt.UTC().Format(time.RFC3339),
		})
	}
	return data
}
