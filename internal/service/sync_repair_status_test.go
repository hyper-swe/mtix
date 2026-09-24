// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// Acceptance tests for MTIX-95.6: `mtix sync repair --status` through the
// service layer, on ADR-006 scenario S7 as a pre-MTIX-95.2 pull left it
// (claim, push, done, then a pull that replayed the claim reverted the node
// to in_progress with closed_at still set). The service lists the
// differences by default; with apply it takes a verified backup first and
// then repairs each node.

// repairStatusFixture returns a sync service whose store holds the S7 revert
// on TEST-1, and the .mtix directory.
func repairStatusFixture(t *testing.T) (*service.SyncService, *sqlite.Store, string) {
	t.Helper()
	ctx := context.Background()
	svc, st, dir := newTestSyncService(t)
	now := time.Now().UTC()
	require.NoError(t, st.CreateNode(ctx, &model.Node{
		ID: "TEST-1", Project: "TEST", Seq: 1, Title: "S7", Status: model.StatusOpen,
		Priority: model.PriorityMedium, Weight: 1.0, NodeType: model.NodeTypeIssue,
		ContentHash: "h", CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, st.ClaimNode(ctx, "TEST-1", "agent-a"))
	// A push marks the claim pushed.
	_, err := st.WriteDB().Exec(`UPDATE sync_events SET sync_status = 'pushed' WHERE sync_status = 'pending'`)
	require.NoError(t, err)
	require.NoError(t, st.TransitionStatus(ctx, "TEST-1", model.StatusDone, "finished", "agent-a"))
	// The pre-MTIX-95.2 pull replayed the claim with the v0.5.0-beta applyClaim statement.
	_, err = st.WriteDB().Exec(`UPDATE nodes SET status = ?, assignee = ?, agent_state = ?, updated_at = ?
		 WHERE id = ? AND deleted_at IS NULL`, "in_progress", "agent-a", "working", "2026-09-01T00:00:00Z", "TEST-1")
	require.NoError(t, err)
	return svc, st, filepath.Join(dir, ".mtix")
}

// statusAndClosedAt reads node id's status and closed_at from db.
func statusAndClosedAt(t *testing.T, db *sql.DB, id string) (string, sql.NullString) {
	t.Helper()
	var (
		status   string
		closedAt sql.NullString
	)
	// The two columns the S7 revert and its repair are about.
	require.NoError(t, db.QueryRow(`SELECT status, closed_at FROM nodes WHERE id = ?`, id).Scan(&status, &closedAt))
	return status, closedAt
}

// syncEventCount returns the number of events in the local log.
func syncEventCount(t *testing.T, st *sqlite.Store) int {
	t.Helper()
	var n int
	// Every event, whatever its status.
	require.NoError(t, st.WriteDB().QueryRow(`SELECT COUNT(*) FROM sync_events`).Scan(&n))
	return n
}

// backupDir is where the repair's backups go.
func backupDir(mtixDir string) string { return filepath.Join(mtixDir, "data", "backups") }

// TestSyncRepairStatus_DryRun_ListsReplayRevert: the S7 fixture shows exactly
// one difference (TEST-1's status), and the dry run writes nothing: no row, no
// event, no backup.
func TestSyncRepairStatus_DryRun_ListsReplayRevert(t *testing.T) {
	svc, st, mtixDir := repairStatusFixture(t)
	events := syncEventCount(t, st)

	report, err := svc.RepairStatus(context.Background(), mtixDir, false, "repairer")

	require.NoError(t, err)
	require.False(t, report.Apply)
	require.Len(t, report.Differences, 1)
	require.Equal(t, "TEST-1", report.Differences[0].NodeID)
	require.Len(t, report.Differences[0].Columns, 1)
	col := report.Differences[0].Columns[0]
	require.Equal(t, "status", col.Column)
	require.Equal(t, "in_progress", *col.Current)
	require.Equal(t, "done", *col.Expected)
	require.Empty(t, report.Backup)
	require.Empty(t, report.Repaired)
	status, _ := statusAndClosedAt(t, st.WriteDB(), "TEST-1")
	require.Equal(t, "in_progress", status)
	require.Equal(t, events, syncEventCount(t, st))
	_, statErr := os.Stat(backupDir(mtixDir))
	require.True(t, os.IsNotExist(statErr), "a dry run takes no backup")
}

// TestSyncRepairStatus_Apply_RestoresDoneAndClosedAt: apply takes a verified
// backup of the damaged store under .mtix/data/backups first, then restores
// TEST-1 to done with closed_at set and emits exactly one event.
func TestSyncRepairStatus_Apply_RestoresDoneAndClosedAt(t *testing.T) {
	svc, st, mtixDir := repairStatusFixture(t)
	events := syncEventCount(t, st)

	report, err := svc.RepairStatus(context.Background(), mtixDir, true, "repairer")

	require.NoError(t, err)
	require.True(t, report.Apply)
	wantBackup := filepath.Join(backupDir(mtixDir), "pre-repair-status-20260314T120000Z.db")
	require.Equal(t, wantBackup, report.Backup)
	require.Len(t, report.Repaired, 1)
	require.Equal(t, "TEST-1", report.Repaired[0].NodeID)

	status, closedAt := statusAndClosedAt(t, st.WriteDB(), "TEST-1")
	require.Equal(t, "done", status)
	require.True(t, closedAt.Valid, "the restored node has closed_at set")
	require.Equal(t, events+1, syncEventCount(t, st), "exactly one event is emitted")
	var payload string
	// The newest event is the repair's status change.
	require.NoError(t, st.WriteDB().QueryRow(`SELECT payload FROM sync_events
		WHERE op_type = 'transition_status' ORDER BY lamport_clock DESC LIMIT 1`).Scan(&payload))
	require.JSONEq(t, `{"from":"in_progress","to":"done","reason":"sync repair"}`, payload)

	// The backup is a verified copy of the store as it was before the repair.
	backup, err := sql.Open("sqlite", wantBackup+"?mode=ro")
	require.NoError(t, err)
	t.Cleanup(func() { _ = backup.Close() })
	var check string
	require.NoError(t, backup.QueryRow(`PRAGMA quick_check`).Scan(&check))
	require.Equal(t, "ok", check)
	backupStatus, _ := statusAndClosedAt(t, backup, "TEST-1")
	require.Equal(t, "in_progress", backupStatus, "the backup holds the pre-repair state")
}

// TestSyncRepairStatus_Idempotent: a second run reports no differences, and a
// second apply takes no backup and writes nothing.
func TestSyncRepairStatus_Idempotent(t *testing.T) {
	ctx := context.Background()
	svc, st, mtixDir := repairStatusFixture(t)
	_, err := svc.RepairStatus(ctx, mtixDir, true, "repairer")
	require.NoError(t, err)
	events := syncEventCount(t, st)
	backups, err := os.ReadDir(backupDir(mtixDir))
	require.NoError(t, err)

	dry, err := svc.RepairStatus(ctx, mtixDir, false, "repairer")
	require.NoError(t, err)
	require.Empty(t, dry.Differences)
	again, err := svc.RepairStatus(ctx, mtixDir, true, "repairer")
	require.NoError(t, err)
	require.Empty(t, again.Differences)
	require.Empty(t, again.Repaired)
	require.Empty(t, again.Backup, "nothing to repair, so no backup")

	require.Equal(t, events, syncEventCount(t, st))
	after, err := os.ReadDir(backupDir(mtixDir))
	require.NoError(t, err)
	require.Len(t, after, len(backups))
}

// TestSyncRepairStatus_BackupFails_WritesNothing: when the backup cannot be
// written, apply fails before it changes any node.
func TestSyncRepairStatus_BackupFails_WritesNothing(t *testing.T) {
	svc, st, mtixDir := repairStatusFixture(t)
	events := syncEventCount(t, st)
	// A regular file where the backups directory must go.
	require.NoError(t, os.WriteFile(backupDir(mtixDir), []byte("not a directory"), 0o600))

	report, err := svc.RepairStatus(context.Background(), mtixDir, true, "repairer")

	require.Error(t, err)
	require.Nil(t, report)
	status, _ := statusAndClosedAt(t, st.WriteDB(), "TEST-1")
	require.Equal(t, "in_progress", status)
	require.Equal(t, events, syncEventCount(t, st))
}
