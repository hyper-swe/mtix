// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests for MTIX-95.31.1 (from MTIX-95.22): import rejects a time value that
// no reader could parse back (not RFC 3339, or a UTC year outside 1 to 9999,
// model.IsStorableTime) with a clear error, and writes nothing. Written
// red-first.
package sqlite_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// seedTimeValidationSource returns an export of a store holding an
// annotated node with a child, a dependency, an agent and a session, so
// every time field of the export format is present.
func seedTimeValidationSource(t *testing.T) *sqlite.ExportData {
	t.Helper()
	src := seedRoundTripStore(t)
	data, err := src.Export(context.Background(), "", "")
	require.NoError(t, err)
	require.NotEmpty(t, data.Dependencies)
	require.NotEmpty(t, data.Agents)
	require.NotEmpty(t, data.Sessions)
	return data
}

// setDocField sets key on the first element of doc[section].
func setDocField(t *testing.T, doc map[string]any, section, key string, value any) {
	t.Helper()
	items, ok := doc[section].([]any)
	require.True(t, ok && len(items) > 0, "export has no %s", section)
	item, ok := items[0].(map[string]any)
	require.True(t, ok)
	item[key] = value
}

// setNodeStreamTime sets created_at on the first entry of node id's
// annotations or activity array.
func setNodeStreamTime(t *testing.T, doc map[string]any, id, stream, value string) {
	t.Helper()
	entries, ok := docNode(t, doc, id)[stream].([]any)
	require.True(t, ok && len(entries) > 0, "node %s carries no %s", id, stream)
	entry, ok := entries[0].(map[string]any)
	require.True(t, ok)
	entry["created_at"] = value
}

// TestImport_UnstorableTime_RejectedAndWritesNothing verifies that every
// time field of an export is checked before import writes anything, in
// both modes: a value outside years 1..9999 in UTC (including one pushed
// out of range by its zone offset) or not RFC 3339 is rejected with
// ErrInvalidInput naming the field, and the store is left unchanged
// (MTIX-95.31.1, model.IsStorableTime).
func TestImport_UnstorableTime_RejectedAndWritesNothing(t *testing.T) {
	tests := []struct {
		name  string
		field string
		edit  func(t *testing.T, doc map[string]any)
	}{
		{"node created_at year 0", "created_at", func(t *testing.T, doc map[string]any) {
			docNode(t, doc, "COL-2")["created_at"] = "0000-12-31T23:00:00Z"
		}},
		{"node updated_at year 10000", "updated_at", func(t *testing.T, doc map[string]any) {
			docNode(t, doc, "COL-2")["updated_at"] = "10000-01-01T00:00:00Z"
		}},
		{"node closed_at pushed past 9999 by its offset", "closed_at", func(t *testing.T, doc map[string]any) {
			docNode(t, doc, "COL-2")["closed_at"] = "9999-12-31T23:30:00-01:00"
		}},
		{"node defer_until pushed before year 1 by its offset", "defer_until", func(t *testing.T, doc map[string]any) {
			docNode(t, doc, "COL-2")["defer_until"] = "0001-01-01T00:30:00+01:00"
		}},
		{"node deleted_at not RFC 3339", "deleted_at", func(t *testing.T, doc map[string]any) {
			docNode(t, doc, "COL-2")["deleted_at"] = "yesterday"
		}},
		{"node invalidated_at year 0", "invalidated_at", func(t *testing.T, doc map[string]any) {
			docNode(t, doc, "COL-2")["invalidated_at"] = "0000-06-01T00:00:00Z"
		}},
		{"annotation created_at year 0", "annotations", func(t *testing.T, doc map[string]any) {
			setNodeStreamTime(t, doc, "COL-2", "annotations", "0000-06-01T00:00:00Z")
		}},
		{"activity created_at pushed before year 1", "activity", func(t *testing.T, doc map[string]any) {
			setNodeStreamTime(t, doc, "COL-2", "activity", "0001-01-01T00:10:00+00:30")
		}},
		{"dependency created_at year 10000", "dependency", func(t *testing.T, doc map[string]any) {
			setDocField(t, doc, "dependencies", "created_at", "10000-01-01T00:00:00Z")
		}},
		{"agent last_heartbeat year 0", "last_heartbeat", func(t *testing.T, doc map[string]any) {
			setDocField(t, doc, "agents", "last_heartbeat", "0000-01-01T00:00:00Z")
		}},
		{"session started_at year 0", "started_at", func(t *testing.T, doc map[string]any) {
			setDocField(t, doc, "sessions", "started_at", "0000-01-01T00:00:00Z")
		}},
		{"session ended_at year 10000", "ended_at", func(t *testing.T, doc map[string]any) {
			setDocField(t, doc, "sessions", "ended_at", "10000-01-01T00:00:00Z")
		}},
	}
	data := seedTimeValidationSource(t)
	for _, tt := range tests {
		for _, mode := range []sqlite.ImportMode{sqlite.ImportModeReplace, sqlite.ImportModeMerge} {
			t.Run(tt.name+"/"+string(mode), func(t *testing.T) {
				ctx := context.Background()
				bad := editExportResealed(t, data, func(doc map[string]any) { tt.edit(t, doc) })
				dst := newTestStore(t)
				createAnnotatedNode(t, dst, "KEEP-1", 1)
				before := nodeColumnRows(t, dst)

				_, err := dst.Import(ctx, bad, mode, false)
				require.Error(t, err)
				assert.ErrorIs(t, err, model.ErrInvalidInput)
				assert.Contains(t, err.Error(), tt.field)
				assert.Equal(t, before, nodeColumnRows(t, dst), "a rejected import must write nothing")
			})
		}
	}
}

// TestImport_BoundaryTimes_Accepted verifies that the first and last
// storable instants (year 1 and year 9999 in UTC) import (MTIX-95.31.1).
func TestImport_BoundaryTimes_Accepted(t *testing.T) {
	data := seedTimeValidationSource(t)
	edge := editExportResealed(t, data, func(doc map[string]any) {
		n := docNode(t, doc, "COL-2")
		n["created_at"] = "0001-01-01T00:00:00Z"
		n["updated_at"] = "9999-12-31T23:59:59Z"
		n["closed_at"] = "9999-12-31T22:59:59-01:00"
	})
	dst := newTestStore(t)
	_, err := dst.Import(context.Background(), edge, sqlite.ImportModeReplace, false)
	require.NoError(t, err)
	node, err := dst.GetNode(context.Background(), "COL-2")
	require.NoError(t, err)
	assert.Equal(t, 1, node.CreatedAt.Year())
	assert.Equal(t, 9999, node.UpdatedAt.Year())
}
