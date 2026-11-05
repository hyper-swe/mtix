// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/sync/pushlock"
)

func (w *enrolWorld) mtix() string { return filepath.Join(w.root, ".mtix") }

func (w *enrolWorld) assertMarker(t *testing.T, want bool) {
	t.Helper()
	m, err := service.ReadEnrolMarker(w.mtix())
	require.NoError(t, err)
	assert.Equal(t, want, m != nil)
}

func TestEnrolWorktree_WriterLockHeld_RefusedNothingTouched(t *testing.T) {
	for _, name := range []string{"push.lock", "dispatch.lock"} {
		t.Run(name, func(t *testing.T) {
			w := newEnrolWorld(t)
			var held *pushlock.PushLock
			var err error
			if name == "push.lock" {
				held, err = pushlock.Acquire(w.mtix())
			} else {
				held, err = pushlock.AcquireNamed(w.mtix(), name)
			}
			require.NoError(t, err)
			defer func() { require.NoError(t, held.Release()) }()
			before := treeHash(t, w.data)

			_, err = w.run()

			require.ErrorIs(t, err, pushlock.ErrLockHeld)
			assert.Contains(t, err.Error(), name, "the refusal names the lock that is held")
			assert.Empty(t, w.calls, "nothing is read, backed up or moved while another writer holds the board")
			assert.Equal(t, before, treeHash(t, w.data))
			w.assertMarker(t, false)
		})
	}
}

func TestEnrolWorktree_WriterAppearsBeforeMove_RefusedNothingMoved(t *testing.T) {
	w := newEnrolWorld(t)
	w.events = []sqlite.LocalEvent{{ID: "ev-1", NodeID: "PROJ-1", Op: "create_node", Status: "applied", At: enrolNow}}
	w.onSnapshot = func(n int, _ string, s *service.WorktreeSnapshot) *service.WorktreeSnapshot {
		if n == 2 { // the re-read after the backup: a writer slipped an event in
			s.Events = append(append([]sqlite.LocalEvent(nil), s.Events...), sqlite.LocalEvent{ID: "ev-late", NodeID: "PROJ-9", Op: "comment", Status: "pending", At: enrolNow})
		}
		return s
	}
	before := treeHash(t, w.data)

	_, err := w.run()

	require.ErrorIs(t, err, model.ErrConflict)
	assert.Equal(t, before, treeHash(t, w.data))
	assert.NotContains(t, w.calls, "clone")
	w.assertMarker(t, false)
}

func TestEnrolWorktree_WriterThroughOpenDescriptorAfterMove_RolledBackWithItsEvent(t *testing.T) {
	w := newEnrolWorld(t)
	w.onSnapshot = func(n int, dir string, s *service.WorktreeSnapshot) *service.WorktreeSnapshot {
		if n == 3 { // the moved-aside store, read after the clone and the carry
			assert.Contains(t, dir, "data.pre-enrol-", "the verification reads the moved-aside store")
			s.Events = []sqlite.LocalEvent{{ID: "ev-late", NodeID: "PROJ-9", Op: "comment", Status: "pending", At: enrolNow}}
		}
		return s
	}
	before := treeHash(t, w.data)

	_, err := w.run()

	require.ErrorIs(t, err, model.ErrConflict)
	assert.Contains(t, err.Error(), "original store was restored")
	assert.Equal(t, before, treeHash(t, w.data), "the original store, with whatever the writer added, is back")
	assert.NotContains(t, w.calls, "enrol", "the enrolment is not recorded over an incomplete carry")
	w.assertMarker(t, false)
}

func TestEnrolWorktree_Marker_WrittenBeforeMoveAndRemovedLast(t *testing.T) {
	w := newEnrolWorld(t)
	var atFirstRename *service.EnrolMarker
	w.opts.Rename = func(a, b string) error {
		if atFirstRename == nil {
			m, err := service.ReadEnrolMarker(w.mtix())
			require.NoError(t, err)
			atFirstRename = m
		}
		return os.Rename(a, b)
	}
	w.svc = service.NewWorktreeEnrolment(w.opts)
	enrolSawMarker := false
	ports := w.ports()
	inner := ports.Enroll
	ports.Enroll = func(ctx context.Context, r service.EnrollmentRequest) error {
		m, err := service.ReadEnrolMarker(w.mtix())
		require.NoError(t, err)
		enrolSawMarker = m != nil
		return inner(ctx, r)
	}
	w.opts.Ports = ports
	w.svc = service.NewWorktreeEnrolment(w.opts)

	res, err := w.run()

	require.NoError(t, err)
	require.NotNil(t, atFirstRename, "the marker exists before the store moves")
	assert.Equal(t, res.AsideDir, atFirstRename.Aside)
	assert.Equal(t, res.BackupPath, atFirstRename.Backup)
	assert.Equal(t, os.Getpid(), atFirstRename.PID)
	assert.True(t, enrolSawMarker, "the marker is still there when the enrolment is recorded")
	w.assertMarker(t, false)
}

func TestEnrolWorktree_CrashBetweenMoveAndClone_RerunCarriesTheOriginalAcks(t *testing.T) {
	w := newEnrolWorld(t)
	ports := w.ports()
	innerClone := ports.Clone
	ports.Clone = func(ctx context.Context, root string) error {
		require.NoError(t, innerClone(ctx, root)) // a partial store exists
		panic("simulated crash: the process dies mid-clone")
	}
	w.opts.Ports = ports
	w.svc = service.NewWorktreeEnrolment(w.opts)
	require.Panics(t, func() { _, _ = w.run() })

	// What the next command and doctor see after the crash.
	w.assertMarker(t, true)
	guardErr := service.CheckEnrolGuard(w.mtix(), false)
	require.ErrorIs(t, guardErr, service.ErrEnrolInterrupted)
	assert.Contains(t, guardErr.Error(), "data.pre-enrol-20261103T102030Z")
	assert.NoError(t, service.CheckEnrolGuard(w.mtix(), true), "doctor may still run")

	// The re-run starts from the ORIGINAL store, never the partial clone.
	w2 := w
	w2.opts.Ports = w.ports()
	w2.opts.Clock = func() time.Time { return enrolNow.Add(time.Hour) }
	w2.svc = service.NewWorktreeEnrolment(w2.opts)
	w2.calls, w2.snapDirs, w2.snapSeen = nil, nil, nil

	res, err := w2.run()

	require.NoError(t, err)
	require.NotEmpty(t, w2.snapSeen)
	for _, seen := range w2.snapSeen {
		assert.Equal(t, "OLD-STORE-BYTES", seen, "every snapshot is taken from the original store, never the partial clone")
	}
	assert.Contains(t, w2.calls, "carry", "the original store's carry is imported (the fake asserts it is the original)")
	assert.Equal(t, "data.pre-enrol-20261103T112030Z", filepath.Base(res.AsideDir))
	kept, readErr := os.ReadFile(filepath.Join(w.mtix(), "data.partial-enrol-20261103T102030Z", "mtix.db"))
	require.NoError(t, readErr, "the first attempt's partial store is kept, not deleted")
	assert.Equal(t, "CLONED", string(kept))
	w.assertMarker(t, false)
	assert.NoError(t, service.CheckEnrolGuard(w.mtix(), false))
}

func TestEnrolWorktree_CrashBeforeMove_RerunProceeds(t *testing.T) {
	w := newEnrolWorld(t)
	require.NoError(t, service.WriteEnrolMarker(w.mtix(), service.EnrolMarker{
		Aside: filepath.Join(w.mtix(), "data.pre-enrol-20261103T000000Z"), Backup: "b", Started: "t", PID: 1,
	}))
	_, err := w.run()
	require.NoError(t, err)
	w.assertMarker(t, false)
}

func TestCheckEnrolGuard_States(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".mtix")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, service.CheckEnrolGuard(dir, false), "a board that never enrolled passes")
	_, statErr := os.Lstat(filepath.Join(dir, "enroll.d"))
	assert.ErrorIs(t, statErr, os.ErrNotExist, "and the check creates nothing")

	lock, err := pushlock.AcquireNamed(filepath.Join(dir, "enroll.d"), "enroll.lock")
	require.NoError(t, err)
	require.NoError(t, service.WriteEnrolMarker(dir, service.EnrolMarker{Aside: "a", Backup: "b", Started: "2026-11-03T10:20:30Z", PID: 4242}))
	err = service.CheckEnrolGuard(dir, true)
	require.ErrorIs(t, err, service.ErrEnrolInProgress, "a held lock refuses even doctor")
	require.ErrorIs(t, err, model.ErrConflict)
	assert.Contains(t, err.Error(), "pid 4242", "the refusal names the holder")
	require.NoError(t, lock.Release())

	err = service.CheckEnrolGuard(dir, false)
	require.ErrorIs(t, err, service.ErrEnrolInterrupted)
	assert.NoError(t, service.CheckEnrolGuard(dir, true))
}

func TestEnrolWorktree_EnrolFailsAfterRecording_KeepsTheClone(t *testing.T) {
	boom := errors.New("lock release failed after recording")
	tests := []struct {
		name         string
		enrolled     func() (bool, error)
		wantData     string
		wantMarker   bool
		wantContains string
	}{
		{"recorded: keep the clone", func() (bool, error) { return true, nil }, "CLONED", false, "enrolment was recorded"},
		{"not recorded: roll back", func() (bool, error) { return false, nil }, "OLD-STORE-BYTES", false, "original store was restored"},
		{"unknown: touch nothing more", func() (bool, error) { return false, errors.New("registry unreadable") }, "CLONED", true, "could not tell"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newEnrolWorld(t)
			w.fail["enrol"] = boom
			w.enrolled = tt.enrolled

			_, err := w.run()

			require.ErrorIs(t, err, boom)
			assert.Contains(t, err.Error(), tt.wantContains)
			b, readErr := os.ReadFile(filepath.Join(w.data, "mtix.db"))
			require.NoError(t, readErr)
			assert.Equal(t, tt.wantData, string(b))
			w.assertMarker(t, tt.wantMarker)
			if tt.wantData == "CLONED" {
				old, oldErr := os.ReadFile(filepath.Join(w.mtix(), "data.pre-enrol-20261103T102030Z", "mtix.db"))
				require.NoError(t, oldErr, "the original store stays aside")
				assert.Equal(t, "OLD-STORE-BYTES", string(old))
			}
		})
	}
}

func TestEnrolWorktree_HubLookupChunkBoundary_EveryIDChecked(t *testing.T) {
	w := newEnrolWorld(t)
	for i := 0; i < 1001; i++ {
		w.events = append(w.events, sqlite.LocalEvent{ID: fmt.Sprintf("ev-%04d", i), NodeID: "PROJ-1", Op: "comment", Status: "applied", At: enrolNow})
	}
	w.missing = []string{"ev-0500", "ev-1000"}

	_, err := w.run()

	var refusal *service.WorktreeLocalEventsError
	require.ErrorAs(t, err, &refusal)
	assert.Len(t, refusal.Events, 2)
	assert.Contains(t, err.Error(), "ev-0500")
	assert.Contains(t, err.Error(), "ev-1000")
	hubCalls := 0
	for _, c := range w.calls {
		if c == "hub" {
			hubCalls++
		}
	}
	assert.Equal(t, 3, hubCalls, "1001 ids are asked for in three chunks of at most 500")
}
