// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
)

// deadProcessID returns the pid of a child that has already exited.
func deadProcessID(t *testing.T) int {
	t.Helper()
	child := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^$")
	require.NoError(t, child.Run())
	return child.Process.Pid
}

// seedWakeScannerBoard journals one comment addressed to a dead-session agent and adds an
// expired dead registration, on the board app.store holds (FR-20, ADR-007 section 2.7).
func seedWakeScannerBoard(t *testing.T, hostID, uid string) {
	t.Helper()
	now := time.Now().UTC()
	node := &model.Node{ID: "TEST-1", Project: "TEST", Seq: 1, Title: "Task", Status: model.StatusOpen,
		Priority: model.PriorityMedium, Weight: 1, NodeType: model.NodeTypeStory, Creator: "sender",
		ContentHash: model.ComputeContentHash("Task", "", "", "", nil), CreatedAt: now, UpdatedAt: now}
	require.NoError(t, app.store.CreateNode(t.Context(), node))
	require.NoError(t, app.store.SetAnnotations(t.Context(), "TEST-1", []model.Annotation{{
		ID: "01ANN00000", Author: "sender", Text: "please look", CreatedAt: now, Addressee: "dead-agent"}}))
	pid := deadProcessID(t)
	for _, row := range []struct{ agent, seen string }{
		{"dead-agent", now.Format(time.RFC3339)},
		{"old-agent", now.Add(-48 * time.Hour).Format(time.RFC3339)},
	} {
		_, err := app.store.WriteDB().ExecContext(t.Context(), `INSERT INTO agent_registrations
			(agent_id,harness,uid,host_id,pid,pid_start_time,origin,epoch,high_water,pending_hold,registered_at,last_seen,registrar_version,attach_protocol)
			VALUES (?,?,?,?,?,?,?,1,0,0,?,?,'test',1)`,
			row.agent, "codex", uid, hostID, pid, "gone", "manual", row.seen, row.seen)
		require.NoError(t, err)
	}
}

// TestWakeScanner_RealBinaryMutation_MarksDeadRouteStaleAndExpiresOldRegistration runs the
// real binary: a CLI mutation is the trigger, and the scan marks the dead session's row stale
// (its event stays in the inbox) and deletes the registration unseen for over 24 hours.
func TestWakeScanner_RealBinaryMutation_MarksDeadRouteStaleAndExpiresOldRegistration(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "mtix")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, ".")
	output, err := build.CombinedOutput()
	require.NoError(t, err, "build real binary: %s", output)
	initTestApp(t)
	env, err := oplocal.FromProcess()
	require.NoError(t, err)
	hostID, err := oplocal.New(env).HostID()
	require.NoError(t, err)
	uid, err := currentUserID()
	require.NoError(t, err)
	seedWakeScannerBoard(t, hostID, uid)

	trigger := exec.CommandContext(t.Context(), binary, "create", "trigger a scan")
	trigger.Dir = filepath.Dir(app.mtixDir)
	trigger.Env = append(os.Environ(), "MTIX_AUTHOR_ID=test")
	out, err := trigger.CombinedOutput()
	require.NoError(t, err, "create: %s", out)

	var stale string
	require.NoError(t, app.store.ReadDB().QueryRowContext(t.Context(),
		"SELECT COALESCE(stale_since,'') FROM agent_registrations WHERE agent_id=?", "dead-agent").Scan(&stale))
	require.NotEmpty(t, stale, "the dead route must be marked stale by the trigger")
	var old int
	require.NoError(t, app.store.ReadDB().QueryRowContext(t.Context(),
		"SELECT count(*) FROM agent_registrations WHERE agent_id=?", "old-agent").Scan(&old))
	require.Zero(t, old, "the registration unseen for over 24 hours must expire")
}
