// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// Automated rolling backups per NFR-2.8 / MTIX-26.6.
//
// mtix backup has always produced verified snapshots (VACUUM INTO +
// quick_check) but only when invoked by hand — the affected user in the
// 2026-05-19 incident had none. The scheduler makes snapshots routine:
// after mutations, if the newest backup is older than the interval, take
// a new one into .mtix/data/backups/ and rotate. A failed backup never
// fails the triggering command and never disturbs existing backups.
const (
	// DefaultBackupInterval gates how often an automatic backup is taken.
	DefaultBackupInterval = 24 * time.Hour

	// DefaultBackupRetain is how many rolling backups are kept.
	DefaultBackupRetain = 7

	// backupIntervalEnv overrides DefaultBackupInterval (Go duration,
	// e.g. "6h"; "0" disables automatic backups).
	backupIntervalEnv = "MTIX_BACKUP_INTERVAL"

	// backupRetainEnv overrides DefaultBackupRetain (positive integer).
	backupRetainEnv = "MTIX_BACKUP_RETAIN"

	// backupTimestampLayout names backup files sortably:
	// mtix-20260611-090000.db.
	backupTimestampLayout = "20060102-150405"
)

// BackupScheduler takes interval-gated rolling backups of the store.
type BackupScheduler struct {
	store    *sqlite.Store
	dir      string // destination directory (.mtix/data/backups)
	interval time.Duration
	retain   int
	logger   *slog.Logger
	clock    func() time.Time

	mu sync.Mutex // one backup at a time
}

// NewBackupScheduler wires a scheduler. interval 0 disables it; retain
// values below 1 are clamped to 1 (a rotation that deletes every backup
// would defeat the point).
func NewBackupScheduler(
	store *sqlite.Store,
	dir string,
	interval time.Duration,
	retain int,
	logger *slog.Logger,
	clock func() time.Time,
) *BackupScheduler {
	if retain < 1 {
		retain = 1
	}
	return &BackupScheduler{
		store:    store,
		dir:      dir,
		interval: interval,
		retain:   retain,
		logger:   logger,
		clock:    clock,
	}
}

// Interval returns the configured backup interval (0 = disabled).
func (b *BackupScheduler) Interval() time.Duration { return b.interval }

// Retain returns the configured number of backups to keep.
func (b *BackupScheduler) Retain() int { return b.retain }

// NewBackupSchedulerFromEnv builds a scheduler with the documented env
// overrides applied (MTIX_BACKUP_INTERVAL, MTIX_BACKUP_RETAIN).
func NewBackupSchedulerFromEnv(store *sqlite.Store, dir string, logger *slog.Logger) *BackupScheduler {
	interval := DefaultBackupInterval
	if v := os.Getenv(backupIntervalEnv); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= 0 {
			interval = d
		} else if v == "0" {
			interval = 0
		}
	}
	retain := DefaultBackupRetain
	if v := os.Getenv(backupRetainEnv); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			retain = n
		}
	}
	return NewBackupScheduler(store, dir, interval, retain, logger, func() time.Time { return time.Now().UTC() })
}

// MaybeBackup takes a backup if the newest one is older than the
// interval. Returns the created path, or "" when gated or disabled.
// Rotation runs only AFTER a successful new backup; failures never touch
// existing backups (NFR-2.8).
func (b *BackupScheduler) MaybeBackup(ctx context.Context) (string, error) {
	if b.interval <= 0 {
		return "", nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.clock()
	existing := b.sortedBackups()
	if newest, ok := newestBackupTime(existing); ok && now.Sub(newest) < b.interval {
		return "", nil
	}

	if err := os.MkdirAll(b.dir, 0o755); err != nil {
		return "", fmt.Errorf("create backups dir: %w", err)
	}

	dest := filepath.Join(b.dir, fmt.Sprintf("mtix-%s.db", now.Format(backupTimestampLayout)))
	if _, err := b.store.Backup(ctx, dest); err != nil {
		return "", fmt.Errorf("automatic backup: %w", err)
	}

	b.rotate(append(existing, filepath.Base(dest)))
	b.logger.Info("auto_backup_completed", "event", "auto_backup_completed", "path", dest)
	return dest, nil
}

// backupNameRE matches ONLY filenames this scheduler creates. Anything
// else in the directory — a user's hand-copied database, a foreign file —
// is invisible to gating and untouchable by rotation. A file we did not
// create must never hold the interval gate open or be deleted.
var backupNameRE = regexp.MustCompile(`^mtix-\d{8}-\d{6}\.db$`)

// newestBackupTime returns the timestamp of the most recent backup from
// its sortable filename (filesystem mtimes survive copies less reliably).
func newestBackupTime(names []string) (time.Time, bool) {
	if len(names) == 0 {
		return time.Time{}, false
	}
	newest := names[len(names)-1]
	stamp := newest[len("mtix-") : len(newest)-len(".db")]
	ts, err := time.Parse(backupTimestampLayout, stamp)
	if err != nil {
		return time.Time{}, false
	}
	return ts, true
}

// rotate deletes the oldest scheduler-created backups beyond retain.
// Best effort: an undeletable old backup is logged, never fatal.
func (b *BackupScheduler) rotate(names []string) {
	sort.Strings(names)
	for len(names) > b.retain {
		victim := filepath.Join(b.dir, names[0])
		if err := os.Remove(victim); err != nil {
			b.logger.Warn("backup rotation could not remove old backup",
				"path", victim, "error", err)
			return
		}
		b.logger.Info("auto_backup_rotated_out", "event", "auto_backup_rotated_out", "path", victim)
		names = names[1:]
	}
}

// sortedBackups lists scheduler-created backup filenames oldest-first.
func (b *BackupScheduler) sortedBackups() []string {
	matches, err := filepath.Glob(filepath.Join(b.dir, "mtix-*.db"))
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(matches))
	for _, m := range matches {
		if name := filepath.Base(m); backupNameRE.MatchString(name) {
			names = append(names, name)
		}
	}
	sort.Strings(names) // timestamp layout is lexicographically sortable
	return names
}
