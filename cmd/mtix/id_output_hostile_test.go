// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/testutil"
)

// MTIX-100.13: ids that arrive by sync (a stored dependency id, an agent id)
// and the id argument echoed by dep show reach the terminal through the same
// safe helper as every other stored text. --json stays the exact record.

// hostileIDFixtures returns the shared hostile id catalog plus a readable
// non-ASCII id, so the visible text is asserted to survive.
func hostileIDFixtures() []testutil.HostileFixture {
	return append(testutil.HostileIDs(),
		testutil.HostileFixture{Name: "readable", Value: "agent-Café-日本"})
}

// seedHostileDependency stores one blocking dependency whose from-id is the
// given value, as a synced node id would arrive (service-level id validation
// never sees it), and returns the blocked node id.
func seedHostileDependency(t *testing.T, fromID string) string {
	t.Helper()
	ctx := context.Background()
	from, err := app.nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "TEST", Title: "blocker", Creator: "u"})
	require.NoError(t, err)
	to, err := app.nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "TEST", Title: "blocked", Creator: "u"})
	require.NoError(t, err)
	db := app.store.WriteDB()
	_, err = db.ExecContext(ctx, `PRAGMA foreign_keys = OFF`)
	require.NoError(t, err)
	// Rename the blocker to the hostile id, then record the dependency edge.
	_, err = db.ExecContext(ctx, `UPDATE nodes SET id = ? WHERE id = ?`, fromID, from.ID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx,
		`INSERT INTO dependencies (from_id, to_id, dep_type, created_at, created_by)
		 VALUES (?, ?, 'blocks', '2026-01-01T00:00:00Z', 'u')`, fromID, to.ID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `PRAGMA foreign_keys = ON`)
	require.NoError(t, err)
	return to.ID
}

func TestDepShow_HostileStoredFromID_NoControlBytes(t *testing.T) {
	for _, f := range hostileIDFixtures() {
		t.Run(f.Name, func(t *testing.T) {
			initTestApp(t)
			blocked := seedHostileDependency(t, f.Value)
			out := captureStdout(t, func() { require.NoError(t, runDepShow(blocked)) })
			assert.Empty(t, terminalViolation(out))
			assert.Contains(t, out, "Blockers for "+blocked)
			if want := format.SafeLine(f.Value); want != "" {
				assert.Contains(t, out, want, "visible text stays")
			}
		})
	}
}

func TestDepShow_HostileIDArgument_NoControlBytes(t *testing.T) {
	for _, f := range hostileIDFixtures() {
		t.Run(f.Name, func(t *testing.T) {
			initTestApp(t)
			out := captureStdout(t, func() { require.NoError(t, runDepShow(f.Value)) })
			assert.Empty(t, terminalViolation(out))
			assert.True(t, strings.HasPrefix(out, "No blockers for "))
			if want := format.SafeLine(f.Value); want != "" {
				assert.Contains(t, out, want, "visible text stays")
			}
		})
	}
}

func TestDepShow_HostileIDs_JSONByteExact(t *testing.T) {
	for _, f := range hostileIDFixtures() {
		t.Run(f.Name, func(t *testing.T) {
			initTestApp(t)
			app.jsonOutput = true
			t.Cleanup(func() { app.jsonOutput = false })
			blocked := seedHostileDependency(t, f.Value)
			out := captureStdout(t, func() { require.NoError(t, runDepShow(blocked)) })
			var deps []map[string]any
			require.NoError(t, json.Unmarshal([]byte(out), &deps))
			require.Len(t, deps, 1)
			assert.Equal(t, stdlibJSONRoundTrip(t, f.Value), deps[0]["from_id"])
		})
	}
}

func seedStaleAgent(t *testing.T, agentID string) {
	t.Helper()
	_, err := app.store.WriteDB().ExecContext(context.Background(),
		`INSERT INTO agents (agent_id, project, state, state_changed_at, last_heartbeat)
		 VALUES (?, 'TEST', 'idle', '2000-01-01T00:00:00Z', '2000-01-01T00:00:00Z')`, agentID)
	require.NoError(t, err)
}

func TestStale_HostileAgentID_NoControlBytes(t *testing.T) {
	for _, f := range hostileIDFixtures() {
		t.Run(f.Name, func(t *testing.T) {
			initTestApp(t)
			seedStaleAgent(t, f.Value)
			out := captureStdout(t, func() { require.NoError(t, runStale("", false)) })
			assert.Empty(t, terminalViolation(out))
			assert.Contains(t, out, "Stale agents:")
			if want := format.SafeLine(f.Value); want != "" {
				assert.Contains(t, out, want, "visible text stays")
			}
		})
	}
}

func TestStale_HostileAgentID_JSONByteExact(t *testing.T) {
	for _, f := range hostileIDFixtures() {
		t.Run(f.Name, func(t *testing.T) {
			initTestApp(t)
			app.jsonOutput = true
			t.Cleanup(func() { app.jsonOutput = false })
			seedStaleAgent(t, f.Value)
			out := captureStdout(t, func() { require.NoError(t, runStale("", false)) })
			var ids []string
			require.NoError(t, json.Unmarshal([]byte(out), &ids))
			require.Len(t, ids, 1)
			assert.Equal(t, stdlibJSONRoundTrip(t, f.Value), ids[0])
		})
	}
}

// TestIDOutput_PlainIDs_RenderByteIdentical pins that ordinary ids are not
// quoted or altered by the display rule (MTIX-100.13).
func TestIDOutput_PlainIDs_RenderByteIdentical(t *testing.T) {
	initTestApp(t)
	out := captureStdout(t, func() { require.NoError(t, runDepShow("MTIX-1.2")) })
	assert.Equal(t, "No blockers for MTIX-1.2\n", out)

	seedStaleAgent(t, "agent-1")
	out = captureStdout(t, func() { require.NoError(t, runStale("", false)) })
	assert.Equal(t, "Stale agents:\n  agent-1\n", out)
}
