// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"time"
)

// PreSyncBackupsKept is how many pre-import backups
// (.mtix/data/backups/pre-sync-*.db) are kept (FR-15.2f, MTIX-95.31.2):
// after each new one, the oldest beyond this count are deleted.
const PreSyncBackupsKept = 5

// preSyncBackupMarker records, in .mtix/data, the tasks.json and the store
// state the newest pre-import backup was taken for, while that import has
// not succeeded, so its retry against the same store takes no second
// backup (MTIX-95.31.2). A successful import removes it.
const preSyncBackupMarker = "sync-backup.json"

// preSyncBackupRecord is the content of preSyncBackupMarker.
type preSyncBackupRecord struct {
	FileHash  string `json:"file_hash"`
	StoreHash string `json:"store_hash"`
	Backup    string `json:"backup"`
}

// preSyncBackupLayout names pre-import backups by their UTC time.
const preSyncBackupLayout = "20060102-150405"

// preSyncBackupPattern matches only the backups backupDB writes:
// pre-sync-<time>.db and, for further imports in the same second,
// pre-sync-<time>-<n>.db. Nothing else in the directory is ever deleted.
const preSyncBackupPattern = `^pre-sync-(\d{8}-\d{6})(?:-(\d+))?\.db$`

// nextPreSyncBackupPath returns the path for a new pre-import backup taken
// at now: pre-sync-<UTC time>.db, or with the first free -2, -3, ...
// suffix when that name is taken.
func nextPreSyncBackupPath(dir string, now time.Time) (string, error) {
	stamp := "pre-sync-" + now.UTC().Format(preSyncBackupLayout)
	for seq := 1; seq <= 1000; seq++ {
		name := stamp + ".db"
		if seq > 1 {
			name = fmt.Sprintf("%s-%d.db", stamp, seq)
		}
		path := filepath.Join(dir, name)
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			return path, nil
		} else if err != nil {
			return "", fmt.Errorf("check backup name %s: %w", path, err)
		}
	}
	return "", fmt.Errorf("no free backup name for %s in %s", stamp, dir)
}

// preSyncBackup is one pre-import backup file and its place in time.
type preSyncBackup struct {
	name  string
	stamp string
	seq   int
}

// prunePreSyncBackups deletes the oldest pre-import backups in dir beyond
// PreSyncBackupsKept, ordered by time and then by sequence. A backup that
// cannot be deleted is logged, never fatal.
func (s *SyncService) prunePreSyncBackups(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		s.logger.Warn("could not list pre-import backups", "dir", dir, "error", err)
		return
	}
	pattern := regexp.MustCompile(preSyncBackupPattern)
	var backups []preSyncBackup
	for _, e := range entries {
		m := pattern.FindStringSubmatch(e.Name())
		if m == nil || !e.Type().IsRegular() {
			continue
		}
		seq := 1
		if m[2] != "" {
			if n, convErr := strconv.Atoi(m[2]); convErr == nil {
				seq = n
			}
		}
		backups = append(backups, preSyncBackup{name: e.Name(), stamp: m[1], seq: seq})
	}
	sort.Slice(backups, func(i, j int) bool {
		if backups[i].stamp != backups[j].stamp {
			return backups[i].stamp < backups[j].stamp
		}
		return backups[i].seq < backups[j].seq
	})
	for len(backups) > PreSyncBackupsKept {
		victim := filepath.Join(dir, backups[0].name)
		if removeErr := os.Remove(victim); removeErr != nil {
			s.logger.Warn("could not delete an old pre-import backup", "path", victim, "error", removeErr)
		}
		backups = backups[1:]
	}
}

// backupTakenFor returns the path of the backup a failed import of the
// tasks.json with hash fileHash already took from the store in the state
// with hash storeHash, or "" when there is none: no failed import is
// recorded, it was for another file or another store state, or the backup
// is gone.
func (s *SyncService) backupTakenFor(mtixDir, fileHash, storeHash string) string {
	raw, err := os.ReadFile(filepath.Join(mtixDir, "data", preSyncBackupMarker))
	if err != nil || storeHash == "" {
		return ""
	}
	var record preSyncBackupRecord
	if json.Unmarshal(raw, &record) != nil || record.FileHash != fileHash ||
		record.StoreHash != storeHash || record.Backup == "" {
		return ""
	}
	path := filepath.Join(mtixDir, "data", "backups", filepath.Base(record.Backup))
	if info, statErr := os.Stat(path); statErr != nil || !info.Mode().IsRegular() {
		return ""
	}
	return path
}

// rememberBackup records that backupPath was taken before importing the
// tasks.json with hash fileHash into the store state with hash storeHash.
// A failure is logged: a retry then takes another backup.
func (s *SyncService) rememberBackup(mtixDir, fileHash, storeHash, backupPath string) {
	record, err := json.Marshal(preSyncBackupRecord{
		FileHash: fileHash, StoreHash: storeHash, Backup: filepath.Base(backupPath),
	})
	if err == nil {
		err = os.WriteFile(filepath.Join(mtixDir, "data", preSyncBackupMarker), record, 0o644)
	}
	if err != nil {
		s.logger.Warn("could not record the pre-import backup", "error", err)
	}
}

// forgetBackup removes the record of the last pre-import backup once its
// import succeeded: the next import, even of the same file, takes a new
// backup of the store as it then is (MTIX-95.31.2).
func (s *SyncService) forgetBackup(mtixDir string) {
	err := os.Remove(filepath.Join(mtixDir, "data", preSyncBackupMarker))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		s.logger.Warn("could not clear the pre-import backup record", "error", err)
	}
}
