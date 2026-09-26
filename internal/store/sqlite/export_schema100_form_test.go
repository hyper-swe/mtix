// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests for MTIX-95.31.11 (FR-15.2h, FR-7.8): Schema100Form returns an
// export as mtix 0.5.3 and earlier wrote it (schema 1.0.0), byte for byte,
// so the conflict baseline 0.5.3 wrote can be recognized after the upgrade.
// Written red-first.
package sqlite_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// schema100Keys are the node keys schema 1.0.0 carried, as mtix 0.5.3's
// exportNode declared them.
func schema100Keys() []string {
	keys := []string{
		"id", "parent_id", "depth", "seq", "project", "title", "description", "prompt",
		"acceptance", "node_type", "issue_type", "priority", "labels", "status", "progress",
		"assignee", "creator", "agent_state", "weight", "content_hash", "created_at",
		"updated_at", "closed_at", "defer_until", "deleted_at", "uid",
	}
	sort.Strings(keys)
	return keys
}

// checksumAs053 is computeExportChecksum of mtix 0.5.3: the SHA-256 of the
// first JSON encoding of the nodes and dependencies.
func checksumAs053(t *testing.T, data *sqlite.ExportData) string {
	t.Helper()
	raw, err := json.Marshal(struct {
		Nodes any `json:"nodes"`
		Deps  any `json:"deps"`
	}{data.Nodes, data.Dependencies})
	require.NoError(t, err)
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

// TestSchema100Form_EveryColumnNode_KeepsOnlyThe100Fields verifies each node
// keeps exactly the 1.0.0 fields, with their values, the fields 2.0.0 added
// are left out (deleted_by beside deleted_at included), the envelope says
// 1.0.0 and is otherwise unchanged, and the export passed in is not changed.
func TestSchema100Form_EveryColumnNode_KeepsOnlyThe100Fields(t *testing.T) {
	s := newTestStore(t)
	createEveryColumnNode(t, s, "COL-1", 1)
	_, err := s.WriteDB().ExecContext(context.Background(),
		`UPDATE nodes SET deleted_at = ?, deleted_by = ? WHERE id = 'COL-1'`,
		columnTestTime().Add(4*time.Hour).Format(time.RFC3339), "agent-a")
	require.NoError(t, err)
	data, err := s.Export(context.Background(), "PROJ", "0.5.4")
	require.NoError(t, err)
	before, err := json.Marshal(data)
	require.NoError(t, err)

	form, err := sqlite.Schema100Form(data)
	require.NoError(t, err)

	node := exportedNode(t, form, "COL-1")
	keys := make([]string, 0, len(node))
	for k := range node {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	assert.Equal(t, schema100Keys(), keys)
	full := exportedNode(t, data, "COL-1")
	for _, k := range schema100Keys() {
		assert.JSONEq(t, string(full[k]), string(node[k]), "value of %s", k)
	}
	assert.Equal(t, "1.0.0", form.SchemaVersion)
	assert.Equal(t, data.Version, form.Version)
	assert.Equal(t, data.ExportedAt, form.ExportedAt)
	assert.Equal(t, data.MtixVersion, form.MtixVersion)
	assert.Equal(t, data.Project, form.Project)
	assert.Equal(t, data.NodeCount, form.NodeCount)
	assert.Equal(t, data.Dependencies, form.Dependencies)
	assert.Equal(t, data.Agents, form.Agents)
	assert.Equal(t, data.Sessions, form.Sessions)
	after, err := json.Marshal(data)
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after), "the export passed in is not changed")
}

// TestSchema100Form_Checksum_ComputedAs053 verifies the checksum is the one
// mtix 0.5.3 computed, over the first JSON encoding: for text holding
// invalid UTF-8 it differs from the checksum written since (MTIX-107.39).
func TestSchema100Form_Checksum_ComputedAs053(t *testing.T) {
	tests := []struct {
		name  string
		store func(t *testing.T) *sqlite.Store
	}{
		{"valid UTF-8", func(t *testing.T) *sqlite.Store {
			s := newTestStore(t)
			createEveryColumnNode(t, s, "COL-1", 1)
			return s
		}},
		{"text holding invalid UTF-8", seedInvalidUTF8Store},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := tt.store(t).Export(context.Background(), "", "")
			require.NoError(t, err)

			form, err := sqlite.Schema100Form(data)
			require.NoError(t, err)

			assert.Equal(t, checksumAs053(t, form), form.Checksum)
		})
	}
}

// TestSchema100Form_StoreWithoutNodes_NodesStayNull verifies a store without
// nodes keeps "nodes": null, as 0.5.3 encoded it, never an empty list.
func TestSchema100Form_StoreWithoutNodes_NodesStayNull(t *testing.T) {
	data, err := newTestStore(t).Export(context.Background(), "", "")
	require.NoError(t, err)
	require.Nil(t, data.Nodes)

	form, err := sqlite.Schema100Form(data)
	require.NoError(t, err)

	raw, err := json.Marshal(form)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"nodes":null`)
	assert.Equal(t, checksumAs053(t, form), form.Checksum)
}

// TestSchema100Form_NilExport_ReturnsInvalidInput verifies a nil export is
// refused.
func TestSchema100Form_NilExport_ReturnsInvalidInput(t *testing.T) {
	_, err := sqlite.Schema100Form(nil)
	assert.ErrorIs(t, err, model.ErrInvalidInput)
}
