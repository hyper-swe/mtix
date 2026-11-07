// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto/sha256"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/attach"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/sync/pushlock"
	"github.com/hyper-swe/mtix/internal/testutil"
)

type enrolDataEntry struct {
	Hash [32]byte
	Mode fs.FileMode
	Time time.Time
}

// enrolDataSnapshot includes directory times and side files, not just mtix.db.
func enrolDataSnapshot(t *testing.T, root string) map[string]enrolDataEntry {
	t.Helper()
	data := filepath.Join(root, ".mtix", "data")
	got := map[string]enrolDataEntry{}
	require.NoError(t, filepath.WalkDir(data, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(data, path)
		if err != nil {
			return err
		}
		item := enrolDataEntry{Mode: info.Mode(), Time: info.ModTime()}
		if !entry.IsDir() {
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			item.Hash = sha256.Sum256(body)
		}
		got[rel] = item
		return nil
	}))
	return got
}

func heldEnrolTestLock(t *testing.T, root string) func() {
	t.Helper()
	lock, err := pushlock.AcquireNamed(filepath.Join(root, ".mtix", "enroll.d"), "enroll.lock")
	require.NoError(t, err)
	released := false
	release := func() {
		if !released {
			require.NoError(t, lock.Release())
			released = true
		}
	}
	t.Cleanup(release)
	return release
}

func prepareEnrolGuardCase(t *testing.T, root, mode string) {
	t.Helper()
	dir := filepath.Join(root, ".mtix", "enroll.d")
	switch mode {
	case "running":
		heldEnrolTestLock(t, root)
	case "interrupted":
		glueEnrolMarker(t, root)
	case "torn-marker":
		require.NoError(t, os.MkdirAll(dir, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "in-progress.json"), []byte("{torn"), 0o600))
	case "probe-error":
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "data", "enroll.lock"), 0o700))
	case "released":
		lock, err := pushlock.AcquireNamed(dir, "enroll.lock")
		require.NoError(t, err)
		require.NoError(t, lock.Release())
	default:
		t.Fatalf("unknown enrol guard case %q", mode)
	}
}

// invokeSkippedStorePath exercises the production board open on every writable
// initApp-bypassing path. The dispatcher closes any opened board in Pass.
func invokeSkippedStorePath(t *testing.T, st *oplocal.State, root, path string) error {
	t.Helper()
	ctx := t.Context()
	switch path {
	case "drain", "empty-key-drain":
		boards, err := st.DispatchBoards(ctx)
		require.NoError(t, err)
		key := boards.Listed[0].Key
		if path == "empty-key-drain" {
			key = ""
		}
		return productionGlueDrain(ctx, attach.GlueDecision{Act: true, BoardRoot: root, StoreRoot: root, BoardKey: key})
	case "pi-wait":
		_, closeStore, err := openPiWaitBoard(attach.WithStoreRoot(ctx, root))
		if closeStore != nil {
			require.NoError(t, closeStore())
		}
		return err
	case "dispatcher":
		d, err := service.NewHostDispatcher(service.HostDispatcherOptions{State: st, OpenBoard: func(ctx context.Context, row oplocal.ListedBoard) (*service.HostBoard, error) {
			return openHostBoard(ctx, row, st, service.RegistrationHost{HostID: "test-host", UID: "501", Version: "test", Protocol: attach.Protocol}, "unused-executable")
		}})
		require.NoError(t, err)
		return d.Pass(ctx)
	default:
		t.Fatalf("unknown skipped store path %q", path)
		return nil
	}
}

func TestEnrolGuard_SkippedStorePaths_RefuseWithoutDataChanges(t *testing.T) {
	for _, path := range []string{"drain", "empty-key-drain", "pi-wait", "dispatcher"} {
		for _, mode := range []string{"running", "interrupted", "torn-marker", "probe-error"} {
			t.Run(path+"/"+mode, func(t *testing.T) {
				st := testutil.OperatorState(t)
				root := listedBoardCheckout(t, st)
				prepareEnrolGuardCase(t, root, mode)
				before := enrolDataSnapshot(t, root)
				err := invokeSkippedStorePath(t, st, root, path)
				require.Error(t, err)
				if mode == "running" {
					assert.ErrorIs(t, err, service.ErrEnrolInProgress)
				} else if mode == "interrupted" {
					assert.ErrorIs(t, err, service.ErrEnrolInterrupted)
				} else {
					assert.Contains(t, err.Error(), "enrolment", "the guard error must survive")
				}
				assert.Equal(t, before, enrolDataSnapshot(t, root), "no side files, bytes or directory times may change")
			})
		}
	}
}

func TestEnrolGuard_ReleasedLock_ProductionDrainStillRuns(t *testing.T) {
	st := testutil.OperatorState(t)
	root := listedBoardCheckout(t, st)
	boards, err := st.DispatchBoards(t.Context())
	require.NoError(t, err)
	enqueueRow(t, root, forgedRow(t, boards.Listed[0].Key, t.TempDir()))
	prepareEnrolGuardCase(t, root, "released")
	require.NoError(t, invokeSkippedStorePath(t, st, root, "drain"))
	assert.Empty(t, pendingRows(t, root), "the existing released lock must not strand a queue")
}

func TestEnrolGuard_MissingDataDirectory_DrainNeverCreatesFile(t *testing.T) {
	testutil.OperatorState(t)
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, ".mtix"), 0o700))
	err := productionGlueDrain(t.Context(), attach.GlueDecision{Act: true, BoardRoot: root, StoreRoot: root})
	assert.Error(t, err, "a store moved after the predicate must not become a regular data file")
	_, err = os.Lstat(filepath.Join(root, ".mtix", "data"))
	assert.ErrorIs(t, err, os.ErrNotExist)
}

// A wait keeps its handle across polls. Starting enrolment after open must
// refuse subsequent reads and claim/result writes, not wait for a new child.
func TestEnrolGuard_PiStorePolling_RefusesAfterOpen(t *testing.T) {
	for _, mode := range []string{"running", "interrupted"} {
		t.Run(mode, func(t *testing.T) {
			st := testutil.OperatorState(t)
			root := listedBoardCheckout(t, st)
			store, closeStore, err := openPiWaitBoard(attach.WithStoreRoot(t.Context(), root))
			require.NoError(t, err)
			defer func() { require.NoError(t, closeStore()) }()
			prepareEnrolGuardCase(t, root, mode)
			want := service.ErrEnrolInProgress
			if mode == "interrupted" {
				want = service.ErrEnrolInterrupted
			}
			_, err = store.ListRegistrations(t.Context(), "test-host", "501")
			assert.ErrorIs(t, err, want)
			_, err = store.InboxList(t.Context(), "worker")
			assert.ErrorIs(t, err, want)
			_, _, err = store.ClaimWake(t.Context(), model.WakeKey{}, time.Minute)
			assert.ErrorIs(t, err, want)
			err = store.RecordWakeResult(t.Context(), model.WakeAttempt{}, model.WakeResult{})
			assert.ErrorIs(t, err, want)
		})
	}
}
