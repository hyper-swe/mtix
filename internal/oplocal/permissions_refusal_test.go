// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPermissions_ValidationRefusesBatch(t *testing.T) {
	cases := map[string]func(*PermissionBatch){
		"id":                func(b *PermissionBatch) { b.ID = "bad" },
		"board":             func(b *PermissionBatch) { b.BoardID = "bad" },
		"time":              func(b *PermissionBatch) { b.Time = PermissionBatch{}.Time },
		"mode":              func(b *PermissionBatch) { b.Mode = "automatic" },
		"source":            func(b *PermissionBatch) { b.Source = "" },
		"hub":               func(b *PermissionBatch) { b.Hub = HubIdentity{Kind: "unknown"} },
		"empty":             func(b *PermissionBatch) { b.Changes = nil },
		"too-many":          func(b *PermissionBatch) { b.Changes = make([]PermissionChange, 1025) },
		"agent":             func(b *PermissionBatch) { b.Changes[0].AgentID = "bad\nagent" },
		"duplicate":         func(b *PermissionBatch) { b.Changes = append(b.Changes, b.Changes[0]) },
		"markers-unsorted":  func(b *PermissionBatch) { b.Markers = []string{"Z", "A"} },
		"markers-value":     func(b *PermissionBatch) { b.Markers = []string{"CODEX_THREAD_ID=secret"} },
		"markers-duplicate": func(b *PermissionBatch) { b.Markers = []string{"A", "A"} },
		"node":              func(b *PermissionBatch) { b.NodeID = "bad\nnode" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			s := alertFixture(t)
			batch := permissionFixtureBatch()
			change(&batch)
			require.Error(t, s.ApplyPermissions(t.Context(), batch, nil))
		})
	}
}
func writePermissionFixture(t *testing.T, s *State, name string, data []byte) {
	t.Helper()
	dir, err := s.directory(true)
	require.NoError(t, err)
	require.NoError(t, writeData(dir, name, data, false, nil, nil))
	require.NoError(t, closeFile(dir))
}
func TestPermissions_MalformedRetainedStateRefusesUnchanged(t *testing.T) {
	for _, kind := range []string{"foreign-grant", "duplicate-grant", "bad-board-grant", "incomplete-log", "foreign-log", "unknown-log-key", "pending-host", "pending-shape", "pending-hash", "null-grants", "null-agent-grants", "orphan-pending-log"} {
		t.Run(kind, func(t *testing.T) { testPermissionRetainedRefusal(t, kind) })
	}
}
func testPermissionRetainedRefusal(t *testing.T, kind string) {
	t.Helper()
	s := alertFixture(t)
	batch := permissionFixtureBatch()
	require.NoError(t, s.ApplyPermissions(t.Context(), batch, nil))
	grants, err := s.AgentGrants()
	require.NoError(t, err)
	mutatePermissionRetainedFixture(t, s, kind, grants)
	before := alertStateSnapshot(t, s)
	batch.Changes[0].AgentID = "new"
	batch.NodeID = "1"
	calls := 0
	publisher := func(context.Context, PermissionBatch) error { calls++; return nil }
	require.Error(t, s.ApplyPermissions(t.Context(), batch, publisher))
	require.Zero(t, calls)
	require.Equal(t, before, alertStateSnapshot(t, s))
	_, err = s.AgentGrants()
	require.Error(t, err)
	_, err = s.Confirmations(t.Context())
	require.Error(t, err)
}
func mutatePermissionRetainedFixture(t *testing.T, s *State, kind string, grants []AgentGrant) {
	t.Helper()
	batch := permissionFixtureBatch()
	switch kind {
	case "foreign-grant":
		grants[0].HostID = "foreign"
		require.NoError(t, s.WriteJSON("grants", map[string]any{"agent_grants": grants}))
	case "duplicate-grant":
		require.NoError(t, s.WriteJSON("grants", map[string]any{"agent_grants": append(grants, grants[0])}))
	case "bad-board-grant":
		require.NoError(t, s.WriteJSON("grants", map[string]any{"board_grants": []Grant{{BoardID: "bad"}}}))
	case "incomplete-log":
		writePermissionFixture(t, s, "confirmations.jsonl", []byte("{}"))
	case "foreign-log":
		rows, _ := s.Confirmations(t.Context())
		rows[0].HostID = "foreign"
		data, _ := encodeConfirmations(rows)
		writePermissionFixture(t, s, "confirmations.jsonl", data)
	case "unknown-log-key":
		rows, _ := s.Confirmations(t.Context())
		data, _ := json.Marshal(rows[0])
		data = append(data[:len(data)-1], []byte(",\"unknown\":true}\n")...)
		writePermissionFixture(t, s, "confirmations.jsonl", data)
	case "pending-host":
		raw, _ := json.Marshal(record{HostID: "foreign", Data: json.RawMessage(`{}`)})
		writePermissionFixture(t, s, "permissions-pending.json", raw)
	case "pending-shape":
		require.NoError(t, s.WriteJSON("permissions-pending", permissionPending{Rotate: true}))
	case "null-grants":
		require.NoError(t, s.WriteJSON("grants", nil))
	case "null-agent-grants":
		require.NoError(t, s.WriteJSON("grants", map[string]any{"agent_grants": nil}))
	case "orphan-pending-log":
		rows, _ := s.Confirmations(t.Context())
		rows[0].BoardLine = "pending"
		data, _ := encodeConfirmations(rows)
		writePermissionFixture(t, s, "confirmations.jsonl", data)
	case "pending-hash":
		batch.ID = strings.Repeat("1", 32)
		require.NoError(t, s.WriteJSON("permissions-pending", permissionPending{Batch: &batch, ActiveHash: "bad", ArchiveHash: "absent"}))
	}
}
func TestPermissions_PublisherRequiredAndCancelledReads(t *testing.T) {
	s := alertFixture(t)
	batch := permissionFixtureBatch()
	batch.NodeID = "1"
	require.Error(t, s.ApplyPermissions(t.Context(), batch, nil))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := s.Confirmations(ctx)
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorIs(t, s.ApplyPermissions(ctx, batch, nil), context.Canceled)
}
func TestPermissions_LogSymlinkRefused(t *testing.T) {
	if os.PathSeparator != '/' {
		t.Skip("native symlink fixture")
	}
	s := alertFixture(t)
	batch := permissionFixtureBatch()
	require.NoError(t, s.ApplyPermissions(t.Context(), batch, nil))
	path, err := s.Path()
	require.NoError(t, err)
	original := filepath.Join(path, "confirmations.jsonl")
	saved := filepath.Join(path, "saved.jsonl")
	require.NoError(t, os.Rename(original, saved))
	require.NoError(t, os.Symlink(saved, original))
	_, err = s.Confirmations(t.Context())
	require.Error(t, err)
	require.Error(t, s.ApplyPermissions(t.Context(), batch, nil))
}
