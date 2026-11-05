// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/sync/pushlock"
)

const (
	enrolDirName     = "enroll.d"
	enrolLockName    = "enroll.lock"
	enrolMarkerName  = "in-progress.json"
	partialDirPrefix = "data.partial-enrol-" //nolint:misspell // on-disk name next to the ADR-007 aside directory

	// enrolLockAttempts and enrolLockRetryEvery bound the wait for a guard probe
	// to let go of the enroll lock: 40 x 5 ms.
	enrolLockAttempts   = 40
	enrolLockRetryEvery = 5 * time.Millisecond
)

var (
	// ErrEnrolInProgress is returned to a process that starts while a worktree
	// enrolment owns the board (MTIX-112.17.16).
	ErrEnrolInProgress = fmt.Errorf("worktree enrolment in progress: %w", model.ErrConflict)
	// ErrEnrolInterrupted is returned while a durable enrolment marker is left
	// by a process that died mid-enrolment (MTIX-112.17.16).
	ErrEnrolInterrupted = fmt.Errorf("worktree enrolment was interrupted: %w", model.ErrConflict)
)

// EnrolMarker is the durable record written before the store moves and removed
// last. Its presence after a crash tells the next run and doctor where the
// original store and its backup are.
type EnrolMarker struct {
	Aside   string `json:"aside_dir"`
	Backup  string `json:"backup_path"`
	Started string `json:"started_at"`
	PID     int    `json:"pid"`
}

func enrolDir(mtixDir string) string { return filepath.Join(mtixDir, enrolDirName) }

func enrolMarkerPath(mtixDir string) string {
	return filepath.Join(enrolDir(mtixDir), enrolMarkerName)
}

// WriteEnrolMarker durably writes the marker (temp file, fsync, rename, fsync of
// the directory) so a crash never leaves a torn marker.
func WriteEnrolMarker(mtixDir string, m EnrolMarker) (retErr error) {
	body, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("encode enrolment marker: %w", err)
	}
	dir := enrolDir(mtixDir)
	if mkErr := os.MkdirAll(dir, 0o700); mkErr != nil {
		return fmt.Errorf("create %s: %w", dir, mkErr)
	}
	tmp := enrolMarkerPath(mtixDir) + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) //nolint:gosec // G304: path under the worktree's own .mtix/enroll.d
	if err != nil {
		return fmt.Errorf("create enrolment marker: %w", err)
	}
	defer func() {
		if retErr != nil {
			retErr = errors.Join(retErr, os.Remove(tmp))
		}
	}()
	if _, err := f.Write(body); err != nil {
		return errors.Join(fmt.Errorf("write enrolment marker: %w", err), f.Close())
	}
	if err := f.Sync(); err != nil {
		return errors.Join(fmt.Errorf("sync enrolment marker: %w", err), f.Close())
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close enrolment marker: %w", err)
	}
	if err := os.Rename(tmp, enrolMarkerPath(mtixDir)); err != nil {
		return fmt.Errorf("publish enrolment marker: %w", err)
	}
	return syncMarkerDir(dir)
}

// syncMarkerDir flushes the directory entry; Windows cannot sync a directory.
func syncMarkerDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	return syncDir(dir)
}

// ReadEnrolMarker returns the marker, or nil when none exists.
func ReadEnrolMarker(mtixDir string) (*EnrolMarker, error) {
	body, err := os.ReadFile(enrolMarkerPath(mtixDir)) //nolint:gosec // G304: the worktree's own .mtix/enroll.d
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read enrolment marker: %w", err)
	}
	var m EnrolMarker
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("decode enrolment marker: %w", err)
	}
	return &m, nil
}

func removeEnrolMarker(mtixDir string) error {
	if err := os.Remove(enrolMarkerPath(mtixDir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove enrolment marker: %w", err)
	}
	return syncMarkerDir(enrolDir(mtixDir))
}

// Moved reports whether the store was moved aside: the aside directory exists.
// A crash between the marker and the move leaves the live store in place.
func (m *EnrolMarker) Moved() bool {
	_, err := os.Lstat(m.Aside)
	return err == nil
}

// RecoveryHint is the exact next step for a found marker.
func (m *EnrolMarker) RecoveryHint() string {
	if !m.Moved() {
		return fmt.Sprintf("the store never moved: it is still in the data directory (backup %s); run the worktree enrolment again, which clears the marker", m.Backup)
	}
	return fmt.Sprintf("the original store is in %s (backup %s); run the worktree enrolment again, which first puts the original store back", m.Aside, m.Backup)
}

// CheckEnrolGuard refuses a command that would write to a board whose
// enrolment is running (the enroll lock is held) or was interrupted (a marker is
// left). A worktree that never ran an enrolment has no lock file and passes at
// no cost. allowInterrupted lets doctor report the marker (MTIX-112.17.16).
func CheckEnrolGuard(mtixDir string, allowInterrupted bool) error {
	lockFile := filepath.Join(enrolDir(mtixDir), "data", enrolLockName)
	if _, err := os.Lstat(lockFile); err == nil {
		lock, lockErr := acquireEnrolLock(mtixDir)
		if errors.Is(lockErr, pushlock.ErrLockHeld) {
			return fmt.Errorf("%w%s; retry when it finishes", ErrEnrolInProgress, holderSuffix(mtixDir))
		}
		if lockErr != nil {
			return fmt.Errorf("probe enrolment lock: %w", lockErr)
		}
		if relErr := lock.Release(); relErr != nil {
			return fmt.Errorf("release enrolment probe: %w", relErr)
		}
	}
	m, err := ReadEnrolMarker(mtixDir)
	if err != nil {
		return err
	}
	if m != nil && !allowInterrupted {
		return fmt.Errorf("%w: %s", ErrEnrolInterrupted, m.RecoveryHint())
	}
	return nil
}

// acquireEnrolLock takes the enroll lock, retrying for a moment on contention:
// every ordinary command's guard probes by locking it for microseconds, so a
// held lock is only an enrolment once it stays held past the retry window
// (about 200 ms), never because two commands probed at the same instant.
func acquireEnrolLock(mtixDir string) (*pushlock.PushLock, error) {
	for attempt := 0; ; attempt++ {
		lock, err := pushlock.AcquireNamed(enrolDir(mtixDir), enrolLockName)
		if !errors.Is(err, pushlock.ErrLockHeld) || attempt >= enrolLockAttempts {
			return lock, err
		}
		time.Sleep(enrolLockRetryEvery)
	}
}

func holderSuffix(mtixDir string) string {
	if m, err := ReadEnrolMarker(mtixDir); err == nil && m != nil {
		return fmt.Sprintf(" (holder pid %d since %s)", m.PID, m.Started)
	}
	return ""
}
