// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/sync/pushlock"
)

// run restores an interrupted earlier attempt, then prepares under the writer
// locks and swaps the store.
func (s *WorktreeEnrolment) run(ctx context.Context, root string, r EnrollmentRequest) (*WorktreeEnrolResult, error) {
	mtix := filepath.Join(root, ".mtix")
	if err := s.recoverInterrupted(mtix); err != nil {
		return nil, err
	}
	stamp := s.o.Clock().UTC().Format("20060102T150405Z")
	res := &WorktreeEnrolResult{
		BackupPath: filepath.Join(mtix, "backups", "pre-enroll-"+stamp+".db"),
		AsideDir:   filepath.Join(mtix, asideDirPrefix+stamp),
	}
	snap, err := s.prepare(ctx, root, res)
	if err != nil {
		return nil, err
	}
	return res, s.swap(ctx, root, r, snap, res)
}

// prepare holds the push and dispatch locks while it reads the store, refuses
// local-only events, backs up, writes the marker and re-reads the store; the
// locks are released only after the last re-check, immediately before the move.
func (s *WorktreeEnrolment) prepare(ctx context.Context, root string, res *WorktreeEnrolResult) (snap *WorktreeSnapshot, retErr error) {
	mtix := filepath.Join(root, ".mtix")
	data := filepath.Join(mtix, "data")
	if _, err := os.Lstat(res.AsideDir); err == nil {
		return nil, fmt.Errorf("%s already exists: %w", res.AsideDir, model.ErrConflict)
	}
	locks, err := holdWriterLocks(mtix)
	if err != nil {
		return nil, err
	}
	markerWritten := false
	defer func() {
		if retErr != nil && markerWritten {
			retErr = errors.Join(retErr, removeEnrolMarker(mtix))
		}
		retErr = errors.Join(retErr, locks.release())
	}()
	if snap, err = s.checkedSnapshot(ctx, root, data); err != nil {
		return nil, err
	}
	if mkErr := os.MkdirAll(filepath.Dir(res.BackupPath), 0o700); mkErr != nil {
		return nil, fmt.Errorf("create backup directory: %w", mkErr)
	}
	if bkErr := s.p().Backup(ctx, root, res.BackupPath); bkErr != nil {
		return nil, fmt.Errorf("back up worktree store: %w", bkErr)
	}
	m := EnrolMarker{Aside: res.AsideDir, Backup: res.BackupPath, Started: s.o.Clock().UTC().Format("2006-01-02T15:04:05Z"), PID: os.Getpid()}
	if mkErr := WriteEnrolMarker(mtix, m); mkErr != nil {
		return nil, mkErr
	}
	markerWritten = true
	again, err := s.p().Snapshot(ctx, data)
	if err != nil {
		return nil, fmt.Errorf("re-read worktree store: %w", err)
	}
	if !sameSnapshot(snap, again) {
		return nil, fmt.Errorf("the worktree store changed while enrolment prepared (another process wrote to it); nothing was moved, run the enrolment again: %w", model.ErrConflict)
	}
	return snap, nil
}

func (s *WorktreeEnrolment) checkedSnapshot(ctx context.Context, root, data string) (*WorktreeSnapshot, error) {
	snap, err := s.p().Snapshot(ctx, data)
	if err != nil {
		return nil, fmt.Errorf("read worktree store: %w", err)
	}
	if !snap.HubConnected {
		return nil, fmt.Errorf("%s is not a hub-board worktree: %w", root, model.ErrInvalidInput)
	}
	if err := s.refuseLocalOnly(ctx, root, snap.Events); err != nil {
		return nil, err
	}
	// Anything local the hub lacks, or a board file that is not the store's own
	// export, would be rewritten away by the first write after the swap: refuse
	// now, before the backup, the marker or any move.
	if err := refuseBoard(s.p().PreflightBoard(ctx, root)); err != nil {
		return nil, err
	}
	return snap, nil
}

// recoverInterrupted undoes an enrolment that died after writing its marker:
// the half-built store is kept aside and the original store is put back, so the
// new attempt carries the original acks, never the partial store's.
func (s *WorktreeEnrolment) recoverInterrupted(mtix string) error {
	m, err := ReadEnrolMarker(mtix)
	if err != nil || m == nil {
		return err
	}
	if _, statErr := os.Lstat(m.Aside); errors.Is(statErr, os.ErrNotExist) {
		return removeEnrolMarker(mtix) // died before the move: the original is still in place
	}
	data := filepath.Join(mtix, "data")
	if _, statErr := os.Lstat(data); statErr == nil {
		partial := filepath.Join(mtix, partialDirPrefix+filepath.Base(m.Aside)[len(asideDirPrefix):])
		if err := s.keepPartial(data, partial); err != nil {
			return fmt.Errorf("interrupted enrolment: %w; %s", err, m.RecoveryHint())
		}
	}
	if err := s.o.Rename(m.Aside, data); err != nil {
		return fmt.Errorf("restore the original store from %s: %w", m.Aside, err)
	}
	return removeEnrolMarker(mtix)
}

// keepPartial moves a half-built store aside; it is never deleted.
func (s *WorktreeEnrolment) keepPartial(data, partial string) error {
	if _, err := os.Lstat(partial); err == nil {
		return fmt.Errorf("%s already exists: %w", partial, model.ErrConflict)
	}
	if err := s.o.Rename(data, partial); err != nil {
		return fmt.Errorf("keep the partial store aside: %w", err)
	}
	return nil
}

// writerLocks are the push, dispatch and daemon locks every other writer honors.
type writerLocks []*pushlock.PushLock

func holdWriterLocks(mtix string) (writerLocks, error) {
	var held writerLocks
	// sync.daemon.lock is the lifetime lock of a project daemon, which holds
	// dispatch.lock only per pass: between passes it is the only sign of it.
	for _, name := range []string{"sync.push.lock", "dispatch.lock", "sync.daemon.lock"} {
		l, err := pushlock.AcquireNamed(mtix, name)
		if err != nil {
			relErr := held.release()
			return nil, errors.Join(fmt.Errorf("the %s is held by another process (a sync push, project daemon or dispatcher is using this worktree): stop it and run the enrolment again: %w", name, err), relErr)
		}
		held = append(held, l)
	}
	return held, nil
}

func (w writerLocks) release() error {
	var errs []error
	for _, l := range w {
		errs = append(errs, l.Release())
	}
	return errors.Join(errs...)
}

// sameSnapshot reports whether two reads of a store saw the same journal and inbox state.
func sameSnapshot(a, b *WorktreeSnapshot) bool {
	if a.HubConnected != b.HubConnected || !reflect.DeepEqual(a.Events, b.Events) {
		return false
	}
	return reflect.DeepEqual(canonicalCarry(a.Carry), canonicalCarry(b.Carry))
}

func canonicalCarry(c *sqlite.InboxCarry) sqlite.InboxCarry {
	if c == nil {
		return sqlite.InboxCarry{}
	}
	out := sqlite.InboxCarry{Acks: append([]sqlite.CarriedAck(nil), c.Acks...), Deliveries: append([]sqlite.CarriedDelivery(nil), c.Deliveries...)}
	sort.Slice(out.Acks, func(i, j int) bool {
		return out.Acks[i].AgentID+"\x00"+out.Acks[i].EventID < out.Acks[j].AgentID+"\x00"+out.Acks[j].EventID
	})
	sort.Slice(out.Deliveries, func(i, j int) bool {
		a, b := out.Deliveries[i], out.Deliveries[j]
		return a.AgentID+"\x00"+a.EventID+"\x00"+a.HookName < b.AgentID+"\x00"+b.EventID+"\x00"+b.HookName
	})
	return out
}
