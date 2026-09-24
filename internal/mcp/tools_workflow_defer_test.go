// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// MTIX-95.22: the mtix_defer tool accepted `until` and dropped it, and it
// accepted a value that is not a timestamp without complaint.

// newDeferToolEnv registers the workflow tools over a real store holding one
// open node, PROJ-1.
func newDeferToolEnv(t *testing.T, opts ...ToolOption) (*ToolRegistry, *sqlite.Store) {
	t.Helper()
	s := newInboxTestStore(t)
	inboxSeedNode(t, s, "PROJ-1")
	clock := func() time.Time { return time.Date(2030, 6, 1, 12, 0, 0, 0, time.UTC) }
	nodeSvc := service.NewNodeService(s, nil, nil, nil, clock)
	bgSvc := service.NewBackgroundService(s, nil, nil, clock)
	reg := NewToolRegistry()
	RegisterWorkflowTools(reg, nodeSvc, s, bgSvc, opts...)
	return reg, s
}

// deferUntilColumn reads the stored defer_until column verbatim.
func deferUntilColumn(t *testing.T, s *sqlite.Store, id string) sql.NullString {
	t.Helper()
	var v sql.NullString
	require.NoError(t, s.QueryRow(context.Background(),
		`SELECT defer_until FROM nodes WHERE id = ?`, id).Scan(&v))
	return v
}

// TestDefer_WithUntil_StoresDeferUntil verifies the MCP defer tool stores
// `until` as UTC RFC 3339 (MTIX-95.22, FR-3.8b).
func TestDefer_WithUntil_StoresDeferUntil(t *testing.T) {
	tests := []struct {
		name  string
		until string
		want  string
	}{
		{"utc timestamp", "2031-01-01T00:00:00Z", "2031-01-01T00:00:00Z"},
		{"offset timestamp is stored in utc", "2030-12-31T19:00:00-05:00", "2031-01-01T00:00:00Z"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg, s := newDeferToolEnv(t)
			args, err := json.Marshal(map[string]string{"id": "PROJ-1", "until": tt.until})
			require.NoError(t, err)

			res, err := reg.Call(context.Background(), "mtix_defer", args)
			require.NoError(t, err)
			require.False(t, res.IsError)

			got := deferUntilColumn(t, s, "PROJ-1")
			require.True(t, got.Valid, "until must be stored, not dropped")
			assert.Equal(t, tt.want, got.String)
			node, err := s.GetNode(context.Background(), "PROJ-1")
			require.NoError(t, err)
			assert.Equal(t, model.StatusDeferred, node.Status)
		})
	}
}

// TestDeferTool_UnparsableUntil_ReturnsInvalidInput verifies an `until` that
// is not an RFC 3339 timestamp is rejected as invalid input and the node is
// left untouched (MTIX-95.22).
func TestDeferTool_UnparsableUntil_ReturnsInvalidInput(t *testing.T) {
	tests := []struct {
		name  string
		until string
	}{
		{"free text", "next tuesday"},
		{"date without a time", "2031-01-01"},
		{"time without a zone", "2031-01-01T00:00:00"},
		{"space instead of T", "2031-01-01 00:00:00Z"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg, s := newDeferToolEnv(t)
			args, err := json.Marshal(map[string]string{"id": "PROJ-1", "until": tt.until})
			require.NoError(t, err)

			_, err = reg.Call(context.Background(), "mtix_defer", args)
			require.ErrorIs(t, err, model.ErrInvalidInput)

			node, err := s.GetNode(context.Background(), "PROJ-1")
			require.NoError(t, err)
			assert.Equal(t, model.StatusOpen, node.Status, "a rejected defer must not transition")
			assert.False(t, deferUntilColumn(t, s, "PROJ-1").Valid)
		})
	}
}

// TestDeferTool_WithoutUntil_StoresNoWakeTime verifies a defer without until
// still defers and stores no wake time (MTIX-95.22).
func TestDeferTool_WithoutUntil_StoresNoWakeTime(t *testing.T) {
	reg, s := newDeferToolEnv(t)

	res, err := reg.Call(context.Background(), "mtix_defer", json.RawMessage(`{"id":"PROJ-1"}`))
	require.NoError(t, err)
	require.False(t, res.IsError)

	node, err := s.GetNode(context.Background(), "PROJ-1")
	require.NoError(t, err)
	assert.Equal(t, model.StatusDeferred, node.Status)
	assert.False(t, deferUntilColumn(t, s, "PROJ-1").Valid)
}

// TestDeferTool_Author_IsProcessIdentity verifies the defer tool records the
// author identity the server process was wired with (WithAuthor: `mtix mcp`
// passes MTIX_AUTHOR_ID, else the author_id config key, and passes "" when
// neither is set), and "mcp" when no identity is wired (MTIX-95.22, MTIX-24).
func TestDeferTool_Author_IsProcessIdentity(t *testing.T) {
	tests := []struct {
		name string
		opts []ToolOption
		want string
	}{
		{"wired process identity", []ToolOption{WithAuthor("agent-7")}, "agent-7"},
		{"no identity wired", nil, "mcp"},
		{"empty identity keeps the default", []ToolOption{WithAuthor("")}, "mcp"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg, s := newDeferToolEnv(t, tt.opts...)
			ctx := context.Background()

			_, err := reg.Call(ctx, "mtix_defer", json.RawMessage(`{"id":"PROJ-1"}`))
			require.NoError(t, err)

			entries, err := s.GetActivity(ctx, "PROJ-1", 100, 0)
			require.NoError(t, err)
			require.NotEmpty(t, entries)
			assert.Equal(t, tt.want, entries[len(entries)-1].Author)
			var author string
			require.NoError(t, s.QueryRow(ctx,
				`SELECT author_id FROM sync_events WHERE node_id = ? AND op_type = ?`,
				"PROJ-1", string(model.OpTransitionStatus)).Scan(&author))
			assert.Equal(t, tt.want, author)
		})
	}
}
