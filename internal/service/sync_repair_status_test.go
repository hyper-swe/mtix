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
// then repairs each node, skipping flagged nodes unless forced.

// createRepairNode creates node id in st.
func createRepairNode(t *testing.T, st *sqlite.Store, id string, seq int) {
	t.Helper()
	now := time.Now().UTC()
	require.NoError(t, st.CreateNode(context.Background(), &model.Node{
		ID: id, Project: "TEST", Seq: seq, Title: "S7 " + id, Status: model.StatusOpen,
		Priority: model.PriorityMedium, Weight: 1.0, NodeType: model.NodeTypeIssue,
		ContentHash: "h", CreatedAt: now, UpdatedAt: now,
	}))
}

// revertLikeS7 claims id, pushes, marks it done, then replays the claim with
// the v0.5.0-beta applyClaim statement, stamped with a pull time after the
// winner, as a pre-MTIX-95.2 pull did.
func revertLikeS7(t *testing.T, st *sqlite.Store, id string) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, st.ClaimNode(ctx, id, "agent-a"))
	// A push marks the claim pushed.
	_, err := st.WriteDB().Exec(`UPDATE sync_events SET sync_status = 'pushed' WHERE sync_status = 'pending'`)
	require.NoError(t, err)
	require.NoError(t, st.TransitionStatus(ctx, id, model.StatusDone, "finished", "agent-a"))
	pulledAt := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	// The pre-MTIX-95.2 applyClaim UPDATE.
	_, err = st.WriteDB().Exec(`UPDATE nodes SET status = ?, assignee = ?, agent_state = ?, updated_at = ?
		 WHERE id = ? AND deleted_at IS NULL`, "in_progress", "agent-a", "working", pulledAt, id)
	require.NoError(t, err)
}

// repairStatusFixture returns a sync service whose store holds the S7 revert
// on TEST-1, and the .mtix directory.
func repairStatusFixture(t *testing.T) (*service.SyncService, *sqlite.Store, string) {
	t.Helper()
	svc, st, dir := newTestSyncService(t)
	createRepairNode(t, st, "TEST-1", 1)
	revertLikeS7(t, st, "TEST-1")
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

// dryRun and applyOnly are the two usual modes.
var (
	dryRun    = service.StatusRepairOptions{}
	applyOnly = service.StatusRepairOptions{Apply: true}
)

// TestSyncRepairStatus_DryRun_ListsReplayRevert: the S7 fixture shows exactly
// one difference (TEST-1's status), a replay, and the dry run writes nothing:
// no row, no event, no backup.
func TestSyncRepairStatus_DryRun_ListsReplayRevert(t *testing.T) {
	svc, st, mtixDir := repairStatusFixture(t)
	events := syncEventCount(t, st)

	report, err := svc.RepairStatus(context.Background(), mtixDir, dryRun, "repairer")

	require.NoError(t, err)
	require.False(t, report.Apply)
	require.Len(t, report.Differences, 1)
	d := report.Differences[0]
	require.Equal(t, "TEST-1", d.NodeID)
	require.False(t, d.Flagged)
	require.NotEmpty(t, d.MatchedEventID)
	require.NotEmpty(t, d.WinnerWallClock)
	require.Len(t, d.Columns, 1)
	require.Equal(t, "status", d.Columns[0].Column)
	require.Equal(t, "in_progress", *d.Columns[0].Current)
	require.Equal(t, "done", *d.Columns[0].Expected)
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

	report, err := svc.RepairStatus(context.Background(), mtixDir, applyOnly, "repairer")

	require.NoError(t, err)
	require.True(t, report.Apply)
	wantBackup := filepath.Join(backupDir(mtixDir), "pre-repair-status-20260314T120000Z.db")
	require.Equal(t, wantBackup, report.Backup)
	require.Len(t, report.Repaired, 1)
	require.Equal(t, "TEST-1", report.Repaired[0].NodeID)
	require.Empty(t, report.Skipped)

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
	t.Cleanup(func() { require.NoError(t, backup.Close()) })
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
	_, err := svc.RepairStatus(ctx, mtixDir, applyOnly, "repairer")
	require.NoError(t, err)
	events := syncEventCount(t, st)
	backups, err := os.ReadDir(backupDir(mtixDir))
	require.NoError(t, err)

	dry, err := svc.RepairStatus(ctx, mtixDir, dryRun, "repairer")
	require.NoError(t, err)
	require.Empty(t, dry.Differences)
	again, err := svc.RepairStatus(ctx, mtixDir, applyOnly, "repairer")
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

	report, err := svc.RepairStatus(context.Background(), mtixDir, applyOnly, "repairer")

	require.Error(t, err)
	require.Nil(t, report)
	status, _ := statusAndClosedAt(t, st.WriteDB(), "TEST-1")
	require.Equal(t, "in_progress", status)
	require.Equal(t, events, syncEventCount(t, st))
}

// flaggedFixture adds TEST-2, claimed and then set to done without an event
// (as an import of a teammate's tasks.json does), to the S7 fixture.
func flaggedFixture(t *testing.T) (*service.SyncService, *sqlite.Store, string) {
	t.Helper()
	svc, st, mtixDir := repairStatusFixture(t)
	createRepairNode(t, st, "TEST-2", 2)
	require.NoError(t, st.ClaimNode(context.Background(), "TEST-2", "agent-a"))
	// A newer state that arrived without an event.
	_, err := st.WriteDB().Exec(`UPDATE nodes SET status = 'done', closed_at = ? WHERE id = 'TEST-2'`,
		time.Now().UTC().Format(time.RFC3339))
	require.NoError(t, err)
	return svc, st, mtixDir
}

// TestSyncRepairStatus_FlaggedNode_SkippedUnlessForced: apply repairs the
// replay and skips the flagged node; apply with force repairs it too.
func TestSyncRepairStatus_FlaggedNode_SkippedUnlessForced(t *testing.T) {
	ctx := context.Background()
	svc, st, mtixDir := flaggedFixture(t)

	report, err := svc.RepairStatus(ctx, mtixDir, applyOnly, "repairer")

	require.NoError(t, err)
	require.Len(t, report.Differences, 2)
	require.Len(t, report.Repaired, 1)
	require.Equal(t, "TEST-1", report.Repaired[0].NodeID)
	require.Equal(t, filepath.Join(backupDir(mtixDir), "pre-repair-status-20260314T120000Z.db"), report.Backup)
	require.Len(t, report.Skipped, 1)
	require.Equal(t, "TEST-2", report.Skipped[0].NodeID)
	require.True(t, report.Skipped[0].Flagged)
	status, _ := statusAndClosedAt(t, st.WriteDB(), "TEST-2")
	require.Equal(t, "done", status, "a flagged node is not applied without force")

	forced, err := svc.RepairStatus(ctx, mtixDir, service.StatusRepairOptions{Apply: true, Force: true}, "repairer")
	require.NoError(t, err)
	require.True(t, forced.Force)
	require.Equal(t, filepath.Join(backupDir(mtixDir), "pre-repair-status-20260314T120000Z-2.db"), forced.Backup,
		"a backup taken in the same second never overwrites the first")
	require.Len(t, forced.Repaired, 1)
	require.Equal(t, "TEST-2", forced.Repaired[0].NodeID)
	require.Empty(t, forced.Skipped)
	status, _ = statusAndClosedAt(t, st.WriteDB(), "TEST-2")
	require.Equal(t, "in_progress", status)
}

// TestSyncRepairStatus_ForceWithoutApply_Refused: --force only means
// something with --apply.
func TestSyncRepairStatus_ForceWithoutApply_Refused(t *testing.T) {
	svc, _, mtixDir := repairStatusFixture(t)
	_, err := svc.RepairStatus(context.Background(), mtixDir, service.StatusRepairOptions{Force: true}, "repairer")
	require.ErrorIs(t, err, model.ErrInvalidInput)
}

// TestSyncRepairStatus_RepairFailsPartWay_ReturnsReportAndError: a repair that
// fails part-way returns the report of the nodes already repaired with the
// error; those stay repaired and the failed node is unchanged.
func TestSyncRepairStatus_RepairFailsPartWay_ReturnsReportAndError(t *testing.T) {
	svc, st, mtixDir := repairStatusFixture(t)
	createRepairNode(t, st, "TEST-2", 2)
	revertLikeS7(t, st, "TEST-2")
	// Any write to TEST-2 now fails.
	_, err := st.WriteDB().Exec(`CREATE TRIGGER fail_test2 BEFORE UPDATE ON nodes
		WHEN NEW.id = 'TEST-2' BEGIN SELECT RAISE(ABORT, 'injected failure'); END`)
	require.NoError(t, err)

	report, err := svc.RepairStatus(context.Background(), mtixDir, applyOnly, "repairer")

	require.Error(t, err)
	require.Contains(t, err.Error(), "TEST-2")
	require.NotNil(t, report)
	require.Len(t, report.Repaired, 1)
	require.Equal(t, "TEST-1", report.Repaired[0].NodeID)
	status, _ := statusAndClosedAt(t, st.WriteDB(), "TEST-1")
	require.Equal(t, "done", status)
	status, _ = statusAndClosedAt(t, st.WriteDB(), "TEST-2")
	require.Equal(t, "in_progress", status)
}

// TestSyncRepairStatus_ApplyWithOnlyFlaggedNodes_WritesNothing: when every
// difference is flagged, apply without force has nothing to write, so it
// takes no backup and changes nothing.
func TestSyncRepairStatus_ApplyWithOnlyFlaggedNodes_WritesNothing(t *testing.T) {
	svc, st, dir := newTestSyncService(t)
	mtixDir := filepath.Join(dir, ".mtix")
	createRepairNode(t, st, "TEST-2", 2)
	require.NoError(t, st.ClaimNode(context.Background(), "TEST-2", "agent-a"))
	// A newer state that arrived without an event.
	_, err := st.WriteDB().Exec(`UPDATE nodes SET status = 'done' WHERE id = 'TEST-2'`)
	require.NoError(t, err)
	events := syncEventCount(t, st)

	report, err := svc.RepairStatus(context.Background(), mtixDir, applyOnly, "repairer")

	require.NoError(t, err)
	require.Empty(t, report.Backup)
	require.Empty(t, report.Repaired)
	require.Len(t, report.Skipped, 1)
	require.Equal(t, events, syncEventCount(t, st))
	_, statErr := os.Stat(backupDir(mtixDir))
	require.True(t, os.IsNotExist(statErr), "nothing to write, so no backup")
}
