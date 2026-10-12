// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// MTIX-151: every service path that validates a status (or a parent's
// status) on a GetNode read must stay correct when a conflicting write lands
// between that read and the service's own write. hookStore runs a one-shot
// function right after the next GetNode returns, which places the conflicting
// write exactly in that window.

// hookStore wraps the SQLite store and fires a one-shot hook after GetNode.
type hookStore struct {
	*sqlite.Store
	mu    sync.Mutex
	hooks []func()
}

// take pops the next armed hook, or nil.
func (h *hookStore) take() func() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.hooks) == 0 {
		return nil
	}
	fn := h.hooks[0]
	h.hooks = h.hooks[1:]
	return fn
}

// arm queues a one-shot hook; hooks run in order, one per GetNode call.
func (h *hookStore) arm(fn func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.hooks = append(h.hooks, fn)
}

func (h *hookStore) GetNode(ctx context.Context, id string) (*model.Node, error) {
	n, err := h.Store.GetNode(ctx, id)
	if fn := h.take(); fn != nil {
		fn()
	}
	return n, err
}

// RestoreInvalidated fires the hook just before the store call: Restore reads
// nothing outside the store transaction any more, so the conflicting write
// can only land before the transaction begins and must then be honoured.
func (h *hookStore) RestoreInvalidated(ctx context.Context, id, author string) (model.Status, error) {
	if fn := h.take(); fn != nil {
		fn()
	}
	return h.Store.RestoreInvalidated(ctx, id, author)
}

func newRaceService(t *testing.T) (*service.NodeService, *hookStore) {
	t.Helper()
	s, err := sqlite.New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	hs := &hookStore{Store: s}
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	svc := service.NewNodeService(hs, &service.NoopBroadcaster{},
		&service.StaticConfig{AutoClaimEnabled: false}, slog.Default(),
		func() time.Time { return now })
	return svc, hs
}

func raceNode(t *testing.T, svc *service.NodeService, parent string) string {
	t.Helper()
	n, err := svc.CreateNode(context.Background(), &service.CreateNodeRequest{
		Project: "PROJ", Title: "race node", Creator: "admin", ParentID: parent,
	})
	require.NoError(t, err)
	return n.ID
}

func statusOf(t *testing.T, hs *hookStore, id string) model.Status {
	t.Helper()
	n, err := hs.Store.GetNode(context.Background(), id)
	require.NoError(t, err)
	return n.Status
}

func mustDo(t *testing.T, err error) {
	t.Helper()
	require.NoError(t, err)
}

// TestTransitionStatus_CancelBetweenReadAndWrite_RejectsDone proves the store
// re-validates inside its own transaction: done cannot follow a cancel.
func TestTransitionStatus_CancelBetweenReadAndWrite_RejectsDone(t *testing.T) {
	svc, hs := newRaceService(t)
	ctx := context.Background()
	id := raceNode(t, svc, "")
	mustDo(t, hs.ClaimNode(ctx, id, "agent-1"))

	hs.arm(func() { mustDo(t, hs.CancelNode(ctx, id, "stop", "admin", false)) })
	err := svc.TransitionStatus(ctx, id, model.StatusDone, "", "agent-1")

	require.ErrorIs(t, err, model.ErrInvalidTransition)
	assert.Equal(t, model.StatusCancelled, statusOf(t, hs, id))
}

// TestDeferNode_CancelBetweenReadAndWrite_RejectsDefer proves a deferral
// cannot resurrect a node cancelled after the service's read.
func TestDeferNode_CancelBetweenReadAndWrite_RejectsDefer(t *testing.T) {
	svc, hs := newRaceService(t)
	ctx := context.Background()
	id := raceNode(t, svc, "")

	hs.arm(func() { mustDo(t, hs.CancelNode(ctx, id, "stop", "admin", false)) })
	err := svc.DeferNode(ctx, id, nil, "later", "admin")

	require.ErrorIs(t, err, model.ErrInvalidTransition)
	assert.Equal(t, model.StatusCancelled, statusOf(t, hs, id))
}

// TestDeferNode_ClaimBetweenReadAndWrite_StaysConsistent covers defer vs claim:
// whichever order wins, status and assignment must agree afterwards.
func TestDeferNode_ClaimBetweenReadAndWrite_StaysConsistent(t *testing.T) {
	svc, hs := newRaceService(t)
	ctx := context.Background()
	id := raceNode(t, svc, "")

	hs.arm(func() { mustDo(t, hs.ClaimNode(ctx, id, "agent-1")) })
	require.NoError(t, svc.DeferNode(ctx, id, nil, "later", "admin"))

	n, err := hs.Store.GetNode(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, model.StatusDeferred, n.Status)
	assert.Nil(t, n.DeferUntil)
}

// TestRestore_CancelBetweenReadAndWrite_KeepsCancelled: a node invalidated,
// then cancelled after Restore's read, must not be reopened by the restore.
func TestRestore_CancelBetweenReadAndWrite_KeepsCancelled(t *testing.T) {
	svc, hs := newRaceService(t)
	ctx := context.Background()
	id := raceNode(t, svc, "")
	mustDo(t, hs.TransitionStatus(ctx, id, model.StatusInvalidated, "stale", "system"))

	hs.arm(func() { mustDo(t, hs.CancelNode(ctx, id, "stop", "admin", false)) })
	err := svc.Restore(ctx, id, "admin")

	require.ErrorIs(t, err, model.ErrInvalidTransition)
	assert.Equal(t, model.StatusCancelled, statusOf(t, hs, id))
}

// TestRestore_RestoredTwice_SecondRestoreRejected: a second Restore whose read
// saw invalidated must not run once the first already restored the node.
func TestRestore_RestoredTwice_SecondRestoreRejected(t *testing.T) {
	svc, hs := newRaceService(t)
	ctx := context.Background()
	id := raceNode(t, svc, "")
	mustDo(t, hs.TransitionStatus(ctx, id, model.StatusInvalidated, "stale", "system"))

	hs.arm(func() {
		mustDo(t, svc.Restore(ctx, id, "other"))
		mustDo(t, hs.ClaimNode(ctx, id, "agent-1"))
	})
	err := svc.Restore(ctx, id, "admin")

	require.ErrorIs(t, err, model.ErrInvalidTransition)
	assert.Equal(t, model.StatusInProgress, statusOf(t, hs, id))
}

// TestRerun_ReopenedAndClaimedBetweenReadAndWrite_NotClobbered: the rerun's
// auto-reopen must not push a node someone just reopened and claimed back to open.
func TestRerun_ReopenedAndClaimedBetweenReadAndWrite_NotClobbered(t *testing.T) {
	svc, hs := newRaceService(t)
	ctx := context.Background()
	id := raceNode(t, svc, "")
	mustDo(t, hs.ClaimNode(ctx, id, "agent-1"))
	mustDo(t, hs.TransitionStatus(ctx, id, model.StatusDone, "", "agent-1"))

	hs.arm(func() {
		mustDo(t, hs.TransitionStatus(ctx, id, model.StatusOpen, "reopen", "admin"))
		mustDo(t, hs.ClaimNode(ctx, id, "agent-2"))
	})
	require.NoError(t, svc.Rerun(ctx, id, service.RerunAll, "rerun", "admin"))

	n, err := hs.Store.GetNode(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, model.StatusInProgress, n.Status)
	assert.Equal(t, "agent-2", n.Assignee)
}

// TestDecompose_ParentCancelledBetweenReadAndWrite_CreatesNothing proves the
// store rejects children under a parent that became terminal after the check.
func TestDecompose_ParentCancelledBetweenReadAndWrite_CreatesNothing(t *testing.T) {
	svc, hs := newRaceService(t)
	ctx := context.Background()
	parent := raceNode(t, svc, "")

	hs.arm(func() { mustDo(t, hs.CancelNode(ctx, parent, "stop", "admin", false)) })
	_, err := svc.Decompose(ctx, parent, []service.DecomposeInput{{Title: "child"}}, "admin")

	require.ErrorIs(t, err, model.ErrInvalidInput)
	kids, kerr := hs.GetDirectChildren(ctx, parent)
	require.NoError(t, kerr)
	assert.Empty(t, kids)
}

// TestCreateNode_ParentCancelledBetweenReadAndWrite_CreatesNothing is the
// single-child form of the decompose race.
func TestCreateNode_ParentCancelledBetweenReadAndWrite_CreatesNothing(t *testing.T) {
	svc, hs := newRaceService(t)
	ctx := context.Background()
	parent := raceNode(t, svc, "")

	hs.arm(func() { mustDo(t, hs.CancelNode(ctx, parent, "stop", "admin", false)) })
	_, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "child", Creator: "admin", ParentID: parent,
	})

	require.ErrorIs(t, err, model.ErrInvalidInput)
	kids, kerr := hs.GetDirectChildren(ctx, parent)
	require.NoError(t, kerr)
	assert.Empty(t, kids)
}

// TestDecomposeDryRun_ParentCancelledBetweenReadAndWrite_WritesNothing: the
// dry run is read-only, so a stale parent check can only yield a stale preview.
func TestDecomposeDryRun_ParentCancelledBetweenReadAndWrite_WritesNothing(t *testing.T) {
	svc, hs := newRaceService(t)
	ctx := context.Background()
	parent := raceNode(t, svc, "")

	hs.arm(func() { mustDo(t, hs.CancelNode(ctx, parent, "stop", "admin", false)) })
	_, err := svc.DecomposeDryRun(ctx, parent, []service.DecomposeInput{{Title: "child"}})
	require.NoError(t, err)

	kids, kerr := hs.GetDirectChildren(ctx, parent)
	require.NoError(t, kerr)
	assert.Empty(t, kids)
	assert.Equal(t, model.StatusCancelled, statusOf(t, hs, parent))
}

// raceRounds is the iteration count for the real-interleaving tests.
const raceRounds = 150

// runPair starts a and b together and returns their errors.
func runPair(a, b func() error) (errA, errB error) {
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); <-start; errA = a() }()
	go func() { defer wg.Done(); <-start; errB = b() }()
	close(start)
	wg.Wait()
	return errA, errB
}

// TestStateMachineRaces_ConcurrentDoneAndCancel_OneWinnerMatchesFinalState
// runs done vs cancel on an in-progress node for many rounds under -race.
func TestStateMachineRaces_ConcurrentDoneAndCancel_OneWinnerMatchesFinalState(t *testing.T) {
	svc, hs := newRaceService(t)
	ctx := context.Background()
	for i := 0; i < raceRounds; i++ {
		id := raceNode(t, svc, "")
		mustDo(t, hs.ClaimNode(ctx, id, "agent-1"))
		errDone, errCancel := runPair(
			func() error { return svc.TransitionStatus(ctx, id, model.StatusDone, "", "agent-1") },
			func() error { return svc.CancelNode(ctx, id, "stop", "admin", false) },
		)
		got := statusOf(t, hs, id)
		require.True(t, (errDone == nil) != (errCancel == nil), "round %d: exactly one wins: %v / %v", i, errDone, errCancel)
		if errDone == nil {
			require.Equal(t, model.StatusDone, got, "round %d", i)
		} else {
			require.Equal(t, model.StatusCancelled, got, "round %d", i)
		}
	}
}

// TestStateMachineRaces_ConcurrentDeferAndCancel_CancelAlwaysSticks runs
// defer vs cancel: a successful cancel must always be the final state.
func TestStateMachineRaces_ConcurrentDeferAndCancel_CancelAlwaysSticks(t *testing.T) {
	svc, hs := newRaceService(t)
	ctx := context.Background()
	for i := 0; i < raceRounds; i++ {
		id := raceNode(t, svc, "")
		errDefer, errCancel := runPair(
			func() error { return svc.DeferNode(ctx, id, nil, "later", "admin") },
			func() error { return svc.CancelNode(ctx, id, "stop", "admin", false) },
		)
		require.NoError(t, errCancel, "round %d", i)
		_ = errDefer
		require.Equal(t, model.StatusCancelled, statusOf(t, hs, id), "round %d", i)
	}
}

// TestStateMachineRaces_ConcurrentRestoreAndCancel_CancelAlwaysSticks runs
// restore vs cancel on an invalidated node: a successful cancel must be final.
func TestStateMachineRaces_ConcurrentRestoreAndCancel_CancelAlwaysSticks(t *testing.T) {
	svc, hs := newRaceService(t)
	ctx := context.Background()
	for i := 0; i < raceRounds; i++ {
		id := raceNode(t, svc, "")
		mustDo(t, hs.TransitionStatus(ctx, id, model.StatusInvalidated, "stale", "system"))
		_, errCancel := runPair(
			func() error { return svc.Restore(ctx, id, "admin") },
			func() error { return svc.CancelNode(ctx, id, "stop", "admin", false) },
		)
		require.NoError(t, errCancel, "round %d", i)
		require.Equal(t, model.StatusCancelled, statusOf(t, hs, id), "round %d", i)
	}
}

// TestStateMachineRaces_ConcurrentCreateChildAndCancelParent_NoChildOfCancelled
// runs child create vs parent cancel: if the cancel won, no child may appear
// afterwards, so a cancelled parent never gains children after the cancel.
func TestStateMachineRaces_ConcurrentCreateChildAndCancelParent_NoChildAfterCancel(t *testing.T) {
	svc, hs := newRaceService(t)
	ctx := context.Background()
	for i := 0; i < raceRounds; i++ {
		parent := raceNode(t, svc, "")
		var errCreate error
		_, errCancel := runPair(
			func() error {
				_, errCreate = svc.CreateNode(ctx, &service.CreateNodeRequest{
					Project: "PROJ", Title: "child", Creator: "admin", ParentID: parent,
				})
				return errCreate
			},
			func() error { return svc.CancelNode(ctx, parent, "stop", "admin", false) },
		)
		require.NoError(t, errCancel, "round %d", i)
		kids, err := hs.GetDirectChildren(ctx, parent)
		require.NoError(t, err)
		if errCreate != nil {
			require.ErrorIs(t, errCreate, model.ErrInvalidInput, "round %d", i)
			require.Empty(t, kids, "round %d", i)
		} else {
			require.Len(t, kids, 1, "round %d", i)
		}
	}
}

// rerunFrom runs Rerun on id and returns its error.
func rerunFrom(svc *service.NodeService, id string) error {
	return svc.Rerun(context.Background(), id, service.RerunAll, "rerun", "admin")
}

// TestRerun_TerminalChangesToOtherTerminal_ReopensFromNewStatus: a conflict
// into another terminal status must be re-decided, not swallowed (F2).
func TestRerun_TerminalChangesToOtherTerminal_ReopensFromNewStatus(t *testing.T) {
	svc, hs := newRaceService(t)
	ctx := context.Background()
	id := raceNode(t, svc, "")
	mustDo(t, hs.ClaimNode(ctx, id, "agent-1"))
	mustDo(t, hs.TransitionStatus(ctx, id, model.StatusDone, "", "agent-1"))

	hs.arm(func() {
		mustDo(t, hs.TransitionStatus(ctx, id, model.StatusOpen, "reopen", "admin"))
		mustDo(t, hs.CancelNode(ctx, id, "stop", "admin", false))
	})
	require.NoError(t, rerunFrom(svc, id))
	assert.Equal(t, model.StatusOpen, statusOf(t, hs, id))
}

// terminalFlip leaves a done node cancelled (and a cancelled node done), so
// the guarded reopen always meets a different terminal status.
func terminalFlip(t *testing.T, hs *hookStore, id string, from model.Status) func() {
	ctx := context.Background()
	return func() {
		mustDo(t, hs.TransitionStatus(ctx, id, model.StatusOpen, "reopen", "admin"))
		if from == model.StatusDone {
			mustDo(t, hs.CancelNode(ctx, id, "stop", "admin", false))
			return
		}
		mustDo(t, hs.ClaimNode(ctx, id, "agent-1"))
		mustDo(t, hs.TransitionStatus(ctx, id, model.StatusDone, "", "agent-1"))
	}
}

// TestRerun_ParentKeepsChanging_FailsWithConflictAfterBound: the conflict
// budget is finite and its exhaustion is reported as ErrConflict (F1).
func TestRerun_ParentKeepsChanging_FailsWithConflictAfterBound(t *testing.T) {
	svc, hs := newRaceService(t)
	ctx := context.Background()
	id := raceNode(t, svc, "")
	mustDo(t, hs.ClaimNode(ctx, id, "agent-1"))
	mustDo(t, hs.TransitionStatus(ctx, id, model.StatusDone, "", "agent-1"))
	hs.arm(terminalFlip(t, hs, id, model.StatusDone))
	hs.arm(terminalFlip(t, hs, id, model.StatusCancelled))
	hs.arm(terminalFlip(t, hs, id, model.StatusDone))

	err := rerunFrom(svc, id)
	require.ErrorIs(t, err, model.ErrConflict)
	assert.Equal(t, model.StatusCancelled, statusOf(t, hs, id), "the rerun must not have reopened it")
}

// TestRerun_RestoreLosesRaceToCancel_ReopensCancelled: the tolerated
// ErrInvalidTransition from Restore sends the loop back to re-decide (F3).
func TestRerun_RestoreLosesRaceToCancel_ReopensCancelled(t *testing.T) {
	svc, hs := newRaceService(t)
	ctx := context.Background()
	id := raceNode(t, svc, "")
	mustDo(t, hs.TransitionStatus(ctx, id, model.StatusInvalidated, "stale", "system"))

	hs.arm(func() { mustDo(t, hs.CancelNode(ctx, id, "stop", "admin", false)) })
	require.NoError(t, rerunFrom(svc, id))
	assert.Equal(t, model.StatusOpen, statusOf(t, hs, id))
}

// TestRerun_InvalidatedParent_RestoresToPreviousStatusFirst pins FR-3.5c:
// restore-first, so an in-progress parent stays in progress and keeps its owner (F4).
func TestRerun_InvalidatedParent_RestoresToPreviousStatusFirst(t *testing.T) {
	svc, hs := newRaceService(t)
	ctx := context.Background()
	id := raceNode(t, svc, "")
	mustDo(t, hs.ClaimNode(ctx, id, "agent-1"))
	mustDo(t, hs.TransitionStatus(ctx, id, model.StatusInvalidated, "stale", "system"))

	require.NoError(t, rerunFrom(svc, id))
	n, err := hs.Store.GetNode(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, model.StatusInProgress, n.Status)
	assert.Equal(t, "agent-1", n.Assignee)
}

// TestRerun_RestoreCommitsAfterTwoConflicts_StillSucceeds: a restore that
// commits is progress and must not be reported as a failure (F8).
func TestRerun_RestoreCommitsAfterTwoConflicts_StillSucceeds(t *testing.T) {
	svc, hs := newRaceService(t)
	ctx := context.Background()
	id := raceNode(t, svc, "")
	mustDo(t, hs.ClaimNode(ctx, id, "agent-1"))
	mustDo(t, hs.TransitionStatus(ctx, id, model.StatusDone, "", "agent-1"))

	hs.arm(terminalFlip(t, hs, id, model.StatusDone)) // read done -> now cancelled
	hs.arm(func() {                                   // read cancelled -> now invalidated
		mustDo(t, hs.TransitionStatus(ctx, id, model.StatusInvalidated, "stale", "system"))
	})
	require.NoError(t, rerunFrom(svc, id))
	assert.Equal(t, model.StatusOpen, statusOf(t, hs, id))
}

// TestRestore_Succeeds_BroadcastsStatusChanged pins the Restore event (F6).
func TestRestore_Succeeds_BroadcastsStatusChanged(t *testing.T) {
	s, err := sqlite.New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	bc := newRecordingBroadcaster()
	svc := service.NewNodeService(s, bc, &service.StaticConfig{}, slog.Default(),
		func() time.Time { return time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC) })
	ctx := context.Background()
	id := raceNode(t, svc, "")
	require.NoError(t, s.TransitionStatus(ctx, id, model.StatusInvalidated, "stale", "system"))
	bc.Reset()

	require.NoError(t, svc.Restore(ctx, id, "admin"))
	events := bc.Events()
	require.Len(t, events, 1)
	assert.Equal(t, service.EventStatusChanged, events[0].Type)
	assert.Equal(t, id, events[0].NodeID)
	assert.Equal(t, "admin", events[0].Author)
}
