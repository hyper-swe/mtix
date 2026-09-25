// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// schemaVersion100 is the schema_version of the export mtix 0.5.3 and
// earlier wrote (see SchemaVersionV1).
const schemaVersion100 = "1.0.0"

// Schema100Form returns data as mtix 0.5.3 and earlier exported the same
// store, the 1.0.0 form (MTIX-95.31.11, FR-15.2h): schema_version 1.0.0,
// each node with only the fields 1.0.0 carried (the list above exportNode;
// every field added since is left out, whatever it holds), and the checksum
// those versions computed (FR-7.8): the SHA-256 of the first JSON encoding
// of the nodes and dependencies. That checksum equals the one written since
// except for text holding invalid UTF-8 (computeExportChecksum,
// MTIX-107.39). A store without nodes keeps nodes null, as 0.5.3 encoded
// it. The JSON encoding of the result is therefore byte for byte the one
// 0.5.3 produced, which lets the conflict baseline it wrote be recognized.
// data is not changed; the result shares its dependencies, agents and
// sessions. Returns ErrInvalidInput for a nil export.
func Schema100Form(data *ExportData) (*ExportData, error) {
	if data == nil {
		return nil, fmt.Errorf("nil export data: %w", model.ErrInvalidInput)
	}
	form := *data
	form.SchemaVersion = schemaVersion100
	if data.Nodes != nil {
		form.Nodes = make([]exportNode, len(data.Nodes))
		for i := range data.Nodes {
			form.Nodes[i] = schema100Node(&data.Nodes[i])
		}
	}
	canonical, err := json.Marshal(exportChecksumDoc{Nodes: form.Nodes, Deps: form.Dependencies})
	if err != nil {
		return nil, fmt.Errorf("encode the 1.0.0 form for its checksum: %w", err)
	}
	form.Checksum = fmt.Sprintf("%x", sha256.Sum256(canonical))
	return &form, nil
}

// schema100Node returns a copy of n with only the fields schema 1.0.0
// carried, in their order (MTIX-95.31.11).
func schema100Node(n *exportNode) exportNode {
	return exportNode{
		ID: n.ID, ParentID: n.ParentID, Depth: n.Depth, Seq: n.Seq, Project: n.Project,
		Title: n.Title, Description: n.Description, Prompt: n.Prompt, Acceptance: n.Acceptance,
		NodeType: n.NodeType, IssueType: n.IssueType, Priority: n.Priority, Labels: n.Labels,
		Status: n.Status, Progress: n.Progress, Assignee: n.Assignee, Creator: n.Creator,
		AgentState: n.AgentState, Weight: n.Weight, ContentHash: n.ContentHash,
		CreatedAt: n.CreatedAt, UpdatedAt: n.UpdatedAt, ClosedAt: n.ClosedAt,
		DeferUntil: n.DeferUntil, DeletedAt: n.DeletedAt, UID: n.UID,
	}
}
