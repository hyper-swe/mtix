// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests for FR-26.6 (automated rolling backups). Written RED-first per
// TDD-WORKFLOW.md §1.1.
package service_test

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
)

// newBackupFixture returns a scheduler over a real store plus the backups
// directory and a controllable clock.
func newBackupFixture(t *testing.T, retain int) (*service.BackupScheduler, string, *time.Time) {
	t.Helper()
	_, store, dir := newTestSyncService(t)
	now := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)

	backupsDir := filepath.Join(dir, ".mtix", "data", "backups")
	sched := service.NewBackupScheduler(store, backupsDir,
		time.Hour, retain,
		slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
		func() time.Time { return now })
	return sched, backupsDir, &now
}

func listBackups(t *testing.T, dir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "mtix-*.db"))
	require.NoError(t, err)
	return matches
}

// TestMaybeBackup_FirstRun_CreatesVerifiedBackup: happy path — the first
// call creates a backup; an immediate second call is gated by interval.
func TestMaybeBackup_FirstRun_CreatesVerifiedBackup(t *testing.T) {
	sched, dir, _ := newBackupFixture(t, 7)
	ctx := context.Background()

	created, err := sched.MaybeBackup(ctx)
	require.NoError(t, err)
	assert.NotEmpty(t, created, "first run must create a backup")
	assert.Len(t, listBackups(t, dir), 1)

	created, err = sched.MaybeBackup(ctx)
	require.NoError(t, err)
	assert.Empty(t, created, "within the interval no new backup is taken")
	assert.Len(t, listBackups(t, dir), 1)
}

// TestMaybeBackup_IntervalElapsed_RotatesOldBackups: after each interval a
// new backup is taken and only the newest retain are kept.
func TestMaybeBackup_IntervalElapsed_RotatesOldBackups(t *testing.T) {
	sched, dir, now := newBackupFixture(t, 2)
	ctx := context.Background()

	for i := 0; i < 4; i++ {
		created, err := sched.MaybeBackup(ctx)
		require.NoError(t, err)
		assert.NotEmpty(t, created, "round %d must back up", i)
		*now = now.Add(2 * time.Hour)
	}

	remaining := listBackups(t, dir)
	assert.Len(t, remaining, 2, "rotation must keep exactly retain backups")
}

// TestMaybeBackup_ZeroInterval_Disabled: interval 0 disables the feature.
func TestMaybeBackup_ZeroInterval_Disabled(t *testing.T) {
	_, store, dir := newTestSyncService(t)
	backupsDir := filepath.Join(dir, ".mtix", "data", "backups")
	sched := service.NewBackupScheduler(store, backupsDir, 0, 7,
		slog.Default(), time.Now)

	created, err := sched.MaybeBackup(context.Background())
	require.NoError(t, err)
	assert.Empty(t, created)
	assert.Empty(t, listBackups(t, backupsDir))
}

// TestMaybeBackup_FailureKeepsOldBackups: error path — when the new backup
// cannot be taken (disk floor), previously good backups must survive.
func TestMaybeBackup_FailureKeepsOldBackups(t *testing.T) {
	sched, dir, now := newBackupFixture(t, 7)
	ctx := context.Background()

	_, err := sched.MaybeBackup(ctx)
	require.NoError(t, err)
	good := listBackups(t, dir)
	require.Len(t, good, 1)

	*now = now.Add(2 * time.Hour)
	require.NoError(t, os.Chmod(dir, 0o555)) // new backup file cannot be created
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	_, err = sched.MaybeBackup(ctx)
	require.Error(t, err, "backup failure must be reported to the caller for logging")

	assert.Equal(t, good, listBackups(t, dir),
		"a failed backup must never disturb existing good backups")
}

// TestMaybeBackup_ForeignFileInBackupsDir_DoesNotBreakGateOrRotation:
// a user-created file matching the glob but not the timestamp pattern
// must neither hold the interval gate open (backup-per-mutation) nor be
// deleted by rotation.
func TestMaybeBackup_ForeignFileInBackupsDir_DoesNotBreakGateOrRotation(t *testing.T) {
	sched, dir, now := newBackupFixture(t, 1)
	ctx := context.Background()

	require.NoError(t, os.MkdirAll(dir, 0o755))
	foreign := filepath.Join(dir, "mtix-keep-me.db")
	require.NoError(t, os.WriteFile(foreign, []byte("user data"), 0o644))

	created, err := sched.MaybeBackup(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, created)

	// Gate must hold on the REAL newest backup, not fall open.
	created, err = sched.MaybeBackup(ctx)
	require.NoError(t, err)
	assert.Empty(t, created, "a foreign file must not defeat the interval gate")

	// Rotation (retain=1) must never touch the foreign file.
	*now = now.Add(2 * time.Hour)
	_, err = sched.MaybeBackup(ctx)
	require.NoError(t, err)
	assert.FileExists(t, foreign, "rotation must never delete files mtix did not create")
}

// TestNewBackupSchedulerFromEnv_AppliesOverrides covers the env-override
// parsing branches (MTIX_BACKUP_INTERVAL / MTIX_BACKUP_RETAIN).
func TestNewBackupSchedulerFromEnv_AppliesOverrides(t *testing.T) {
	_, store, dir := newTestSyncService(t)
	backupsDir := filepath.Join(dir, ".mtix", "data", "backups")

	t.Run("valid overrides", func(t *testing.T) {
		t.Setenv("MTIX_BACKUP_INTERVAL", "6h")
		t.Setenv("MTIX_BACKUP_RETAIN", "3")
		s := service.NewBackupSchedulerFromEnv(store, backupsDir, slog.Default())
		assert.Equal(t, 6*time.Hour, s.Interval())
		assert.Equal(t, 3, s.Retain())
	})

	t.Run("zero interval disables", func(t *testing.T) {
		t.Setenv("MTIX_BACKUP_INTERVAL", "0")
		s := service.NewBackupSchedulerFromEnv(store, backupsDir, slog.Default())
		assert.Equal(t, time.Duration(0), s.Interval())
	})

	t.Run("garbage falls back to defaults", func(t *testing.T) {
		t.Setenv("MTIX_BACKUP_INTERVAL", "not-a-duration")
		t.Setenv("MTIX_BACKUP_RETAIN", "-5")
		s := service.NewBackupSchedulerFromEnv(store, backupsDir, slog.Default())
		assert.Equal(t, service.DefaultBackupInterval, s.Interval())
		assert.Equal(t, service.DefaultBackupRetain, s.Retain())
	})
}
