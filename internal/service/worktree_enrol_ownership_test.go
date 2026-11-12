// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/sync/pushlock"
)

// TestEnrolWorktree_IdleDaemonLifetimeLockHeld_RefusedNothingTouched: a project
// daemon takes dispatch.lock only per pass, so between passes the writer locks
// are free; its lifetime lock is the known owner and must refuse the enrolment.
func TestEnrolWorktree_IdleDaemonLifetimeLockHeld_RefusedNothingTouched(t *testing.T) {
	w := newEnrolWorld(t)
	held, err := pushlock.AcquireNamed(w.mtix(), "sync.daemon.lock")
	require.NoError(t, err)
	defer func() { require.NoError(t, held.Release()) }()
	before := treeHash(t, w.data)

	_, err = w.run()

	require.ErrorIs(t, err, pushlock.ErrLockHeld)
	assert.Contains(t, err.Error(), "sync.daemon.lock")
	assert.Empty(t, w.calls)
	assert.Equal(t, before, treeHash(t, w.data))
	w.assertMarker(t, false)
}

// TestEnrolWorktree_RefusedOnALaterLock_ReleasesTheEarlierOnes: a refusal must
// not leave the locks it already took held.
func TestEnrolWorktree_RefusedOnALaterLock_ReleasesTheEarlierOnes(t *testing.T) {
	for _, held := range []string{"dispatch.lock", "sync.daemon.lock"} {
		t.Run(held, func(t *testing.T) {
			w := newEnrolWorld(t)
			other, err := pushlock.AcquireNamed(w.mtix(), held)
			require.NoError(t, err)
			defer func() { require.NoError(t, other.Release()) }()

			_, err = w.run()
			require.ErrorIs(t, err, pushlock.ErrLockHeld)

			for _, name := range []string{"sync.push.lock", "dispatch.lock", "sync.daemon.lock"} {
				if name == held {
					continue
				}
				again, acqErr := pushlock.AcquireNamed(w.mtix(), name)
				require.NoError(t, acqErr, "%s was left held by the refused enrolment", name)
				require.NoError(t, again.Release())
			}
		})
	}
}

// TestCheckEnrolGuard_ConcurrentProbes_NeverReportAFalseEnrolmentInProgress:
// the lock file stays after any enrolment, and the guard probes by locking it,
// so two commands probing at once must not see each other as an enrolment.
func TestCheckEnrolGuard_ConcurrentProbes_NeverReportAFalseEnrolmentInProgress(t *testing.T) {
	mtix := filepath.Join(t.TempDir(), ".mtix")
	first, err := pushlock.AcquireNamed(filepath.Join(mtix, "enroll.d"), "enroll.lock")
	require.NoError(t, err)
	require.NoError(t, first.Release()) // an enrolment ran once: the lock file stays

	var wg sync.WaitGroup
	var mu sync.Mutex
	var falsePositives []error
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 300; i++ {
				if probeErr := service.CheckEnrolGuard(mtix, false); probeErr != nil {
					mu.Lock()
					falsePositives = append(falsePositives, probeErr)
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()

	assert.Empty(t, falsePositives, "no enrolment runs, so no probe may refuse")
}

// TestCheckEnrolGuard_LockHeldLongerThanAProbe_StillRefuses: the retry that
// absorbs probe collisions does not hide a real, running enrolment.
func TestCheckEnrolGuard_LockHeldLongerThanAProbe_StillRefuses(t *testing.T) {
	mtix := filepath.Join(t.TempDir(), ".mtix")
	held, err := pushlock.AcquireNamed(filepath.Join(mtix, "enroll.d"), "enroll.lock")
	require.NoError(t, err)
	defer func() { require.NoError(t, held.Release()) }()

	err = service.CheckEnrolGuard(mtix, false)

	require.ErrorIs(t, err, service.ErrEnrolInProgress)
}

// TestEnrolWorktree_EnrolLockBrieflyHeldByAProbe_StillEnrols: a command's guard
// probe holds the enroll lock for microseconds; it must not fail an enrolment.
func TestEnrolWorktree_EnrolLockBrieflyHeldByAProbe_StillEnrols(t *testing.T) {
	w := newEnrolWorld(t)
	probe, err := pushlock.AcquireNamed(filepath.Join(w.mtix(), "enroll.d"), "enroll.lock")
	require.NoError(t, err)
	go func() {
		time.Sleep(20 * time.Millisecond)
		_ = probe.Release()
	}()

	_, err = w.run()

	require.NoError(t, err)
}

// carryWith returns a copy of the world's carry with one more ack and/or delivery.
func carryWith(c *sqlite.InboxCarry, ack *sqlite.CarriedAck, del *sqlite.CarriedDelivery) *sqlite.InboxCarry {
	out := &sqlite.InboxCarry{Acks: append([]sqlite.CarriedAck(nil), c.Acks...), Deliveries: append([]sqlite.CarriedDelivery(nil), c.Deliveries...)}
	if ack != nil {
		out.Acks = append(out.Acks, *ack)
	}
	if del != nil {
		out.Deliveries = append(out.Deliveries, *del)
	}
	return out
}

// TestEnrolWorktree_OnlyTheInboxStateChanges_IsDetected: an ack or a delivery
// written after the snapshot changes no event, so only the carry comparison
// protects the acknowledgements story.
func TestEnrolWorktree_OnlyTheInboxStateChanges_IsDetected(t *testing.T) {
	late := map[string]struct {
		ack *sqlite.CarriedAck
		del *sqlite.CarriedDelivery
	}{
		"ack":      {ack: &sqlite.CarriedAck{AgentID: "a", EventID: "e-late"}},
		"delivery": {del: &sqlite.CarriedDelivery{AgentID: "a", EventID: "e-late", HookName: "wake"}},
	}
	for name, c := range late {
		for _, read := range []struct {
			label string
			nth   int
		}{{"before the move", 2}, {"on the moved-aside store", 3}} {
			t.Run(name+" "+read.label, func(t *testing.T) {
				w := newEnrolWorld(t)
				w.onSnapshot = func(n int, _ string, s *service.WorktreeSnapshot) *service.WorktreeSnapshot {
					if n == read.nth {
						s.Carry = carryWith(w.carry, c.ack, c.del)
					}
					return s
				}
				before := treeHash(t, w.data)

				_, err := w.run()

				require.ErrorIs(t, err, model.ErrConflict)
				assert.NotContains(t, w.calls, "enrol")
				assert.Equal(t, before, treeHash(t, w.data))
				w.assertMarker(t, false)
			})
		}
	}
}

func TestEnrolWorktree_SameInboxStateInAnyOrder_IsNotAChange(t *testing.T) {
	w := newEnrolWorld(t)
	w.carry = &sqlite.InboxCarry{Acks: []sqlite.CarriedAck{{AgentID: "a", EventID: "e1"}, {AgentID: "b", EventID: "e2"}}}
	w.onSnapshot = func(n int, _ string, s *service.WorktreeSnapshot) *service.WorktreeSnapshot {
		if n == 2 || n == 3 {
			s.Carry = &sqlite.InboxCarry{Acks: []sqlite.CarriedAck{{AgentID: "b", EventID: "e2"}, {AgentID: "a", EventID: "e1"}}}
		}
		return s
	}

	_, err := w.run()

	require.NoError(t, err)
}

// TestEnrolWorktree_TasksJSONRecordedAsSeen: the fresh store has no
// sync.sha256, so without this the first command would import tasks.json over
// the hub clone. The hash of the board file on disk is recorded after the clone.
func TestEnrolWorktree_TasksJSONRecordedAsSeen(t *testing.T) {
	w := newEnrolWorld(t)
	body := []byte(`{"nodes":[]}`)
	require.NoError(t, os.WriteFile(filepath.Join(w.mtix(), "tasks.json"), body, 0o600))

	_, err := w.run()

	require.NoError(t, err)
	got, readErr := os.ReadFile(filepath.Join(w.data, "sync.sha256"))
	require.NoError(t, readErr)
	assert.Equal(t, fmt.Sprintf("%x", sha256.Sum256(body)), string(got))
}

func TestEnrolWorktree_NoTasksJSON_WritesNoSeenHash(t *testing.T) {
	w := newEnrolWorld(t)

	_, err := w.run()

	require.NoError(t, err)
	_, statErr := os.Lstat(filepath.Join(w.data, "sync.sha256"))
	assert.ErrorIs(t, statErr, os.ErrNotExist)
}

// TestEnrolWorktree_RecordingTheSeenHashFails_RestoresTheOriginal: an enrolment
// that cannot make the clone safe from the import must not complete.
func TestEnrolWorktree_RecordingTheSeenHashFails_RestoresTheOriginal(t *testing.T) {
	w := newEnrolWorld(t)
	require.NoError(t, os.MkdirAll(filepath.Join(w.mtix(), "tasks.json"), 0o700)) // unreadable as a file
	before := treeHash(t, w.data)

	_, err := w.run()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "original store was restored")
	assert.Equal(t, before, treeHash(t, w.data))
	assert.NotContains(t, w.calls, "enrol")
}

func TestEnrolWorktree_MoveAsideFails_LeavesNoMarker(t *testing.T) {
	w := newEnrolWorld(t)
	w.opts.Rename = func(string, string) error { return errors.New("injected rename failure") }
	w.svc = service.NewWorktreeEnrolment(w.opts)

	_, err := w.run()

	require.Error(t, err)
	w.assertMarker(t, false)
}

func TestEnrolWorktree_CorruptMarker_RefusesBeforeTouchingTheStore(t *testing.T) {
	w := newEnrolWorld(t)
	require.NoError(t, os.MkdirAll(filepath.Join(w.mtix(), "enroll.d"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(w.mtix(), "enroll.d", "in-progress.json"), []byte("{not json"), 0o600))
	before := treeHash(t, w.data)

	_, err := w.run()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode enrolment marker")
	assert.Empty(t, w.calls)
	assert.Equal(t, before, treeHash(t, w.data))
}

func TestEnrolWorktree_FailureAfterMove_ReportsThePartialStoreDirectory(t *testing.T) {
	w := newEnrolWorld(t)
	w.fail["carry"] = errors.New("injected failure")

	res, err := w.run()

	require.Error(t, err)
	require.NotNil(t, res)
	assert.Equal(t, filepath.Join(w.mtix(), "data.partial-enrol-20261103T102030Z"), res.PartialDir)
}

func TestEnrolMarker_RecoveryHint_SaysWhereTheOriginalStoreIs(t *testing.T) {
	aside := filepath.Join(t.TempDir(), "data.pre-enrol-T")
	m := service.EnrolMarker{Aside: aside, Backup: "/x/bk.db"}

	assert.False(t, m.Moved())
	assert.Contains(t, m.RecoveryHint(), "never moved")
	assert.NotContains(t, m.RecoveryHint(), "original store is in "+aside)

	require.NoError(t, os.MkdirAll(aside, 0o700))
	assert.True(t, m.Moved())
	assert.Contains(t, m.RecoveryHint(), "original store is in "+aside)
}
