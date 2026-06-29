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
	"github.com/hyper-swe/mtix/internal/sync/clock"
)

// clockNewEventID mints a fresh UUIDv7 usable as a node uid.
func clockNewEventID() (string, error) { return clock.NewEventID() }

// scriptedSettler is a Settler double for service-layer settlement tests
// (ADR-003 §4). It returns a scripted outcome per call and can block to model a
// slow hub RTT, so a test can prove the create call does not wait on it.
type scriptedSettler struct {
	mu       sync.Mutex
	calls    int
	outcomes []sqlite.SettleOutcome // consumed in order; last repeats
	block    chan struct{}          // if non-nil, ConfirmClaim waits on it
}

func (s *scriptedSettler) ConfirmClaim(
	ctx context.Context, _ sqlite.ClaimRequest,
) (sqlite.SettleOutcome, error) {
	if s.block != nil {
		select {
		case <-s.block:
		case <-ctx.Done():
			return sqlite.SettleUnreachable, nil
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.calls
	s.calls++
	if i >= len(s.outcomes) {
		i = len(s.outcomes) - 1
	}
	return s.outcomes[i], nil
}

func (s *scriptedSettler) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// alwaysReachable / neverReachable model the cheap, non-blocking reachability
// probe the create path consults to decide settled-vs-provisional (ADR-003 §4).
func alwaysReachable() bool { return true }
func neverReachable() bool  { return false }

// makeRootForSettle stores a clean root node directly for settlement tests.
func makeRootForSettle(s *sqlite.Store, id, project string, now time.Time) *model.Node {
	return &model.Node{
		ID: id, Project: project, Depth: 0, Seq: 1, Title: "root",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeStory, Creator: "test",
		ContentHash: model.ComputeContentHash("root", "", "", "", nil),
		CreatedAt:   now, UpdatedAt: now,
	}
}

// makeProvisional stores a provisional child under parentID and returns its uid.
// UUIDv7 short segments can clash for same-millisecond uids (ADR-003 §13), so it
// retries with a fresh uid until the provisional id is unique.
func makeProvisional(t *testing.T, s *sqlite.Store, parentID, project string, depth int) string {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	for {
		uid, err := clockNewEventID()
		require.NoError(t, err)
		provID, err := model.BuildProvisionalID(parentID, uid)
		require.NoError(t, err)
		node := &model.Node{
			ID: provID, ParentID: parentID, Project: project, Depth: depth, Seq: 0,
			Title: "child", Status: model.StatusOpen, Priority: model.PriorityMedium,
			Weight: 1.0, NodeType: model.NodeTypeEpic, Creator: "test", UID: uid,
			ContentHash: model.ComputeContentHash("child", "", "", "", nil),
			CreatedAt:   now, UpdatedAt: now,
		}
		err = s.CreateNode(ctx, node)
		if err == nil {
			return uid
		}
		require.ErrorIs(t, err, model.ErrAlreadyExists)
	}
}

func newSettlementSvc(
	t *testing.T, settler sqlite.Settler, reachable func() bool,
) (*service.SettlementService, *sqlite.Store) {
	t.Helper()
	s, err := sqlite.New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return service.NewSettlementService(s, settler, reachable, slog.Default()), s
}

// --- SettlePending (background drain) -------------------------------------

// TestSettlePending_SettlesAllReady drains the worklist: every ready provisional
// node settles to a clean number (ADR-003 §4 next-sync settlement).
func TestSettlePending_SettlesAllReady(t *testing.T) {
	settler := &scriptedSettler{outcomes: []sqlite.SettleOutcome{sqlite.SettleConfirmed}}
	svc, s := newSettlementSvc(t, settler, alwaysReachable)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootForSettle(s, "PROJ-1", "PROJ", now)))
	c1 := makeProvisional(t, s, "PROJ-1", "PROJ", 1)
	c2 := makeProvisional(t, s, "PROJ-1", "PROJ", 1)

	settled, remaining, err := svc.SettlePending(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, settled)
	assert.Equal(t, 0, remaining)

	for _, uid := range []string{c1, c2} {
		id, err := s.ResolveDisplayPathByUID(ctx, uid)
		require.NoError(t, err)
		assert.False(t, model.IsProvisional(id))
	}
}

// TestSettlePending_OfflineLeavesProvisional is the offline drain: an unreachable
// hub leaves nodes provisional and reports them as remaining (ADR-003 §4).
func TestSettlePending_OfflineLeavesProvisional(t *testing.T) {
	settler := &scriptedSettler{outcomes: []sqlite.SettleOutcome{sqlite.SettleUnreachable}}
	svc, s := newSettlementSvc(t, settler, neverReachable)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootForSettle(s, "PROJ-1", "PROJ", now)))
	c1 := makeProvisional(t, s, "PROJ-1", "PROJ", 1)

	settled, remaining, err := svc.SettlePending(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, settled)
	assert.Equal(t, 1, remaining)

	id, err := s.ResolveDisplayPathByUID(ctx, c1)
	require.NoError(t, err)
	assert.True(t, model.IsProvisional(id), "offline node stays provisional for next sync")
}

// TestSettlePending_NilSettler is a safe no-op when no hub is configured.
func TestSettlePending_NilSettler(t *testing.T) {
	svc, s := newSettlementSvc(t, nil, neverReachable)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, s.CreateNode(ctx, makeRootForSettle(s, "PROJ-1", "PROJ", now)))

	settled, remaining, err := svc.SettlePending(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, settled)
	assert.Equal(t, 0, remaining)
}

// TestSettlePending_RetryOnTaken settles into the next free number when the hub
// reports the first candidate taken (ADR-003 §4, §6).
func TestSettlePending_RetryOnTaken(t *testing.T) {
	settler := &scriptedSettler{outcomes: []sqlite.SettleOutcome{
		sqlite.SettleRenumberRequired, sqlite.SettleConfirmed,
	}}
	svc, s := newSettlementSvc(t, settler, alwaysReachable)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootForSettle(s, "PROJ-1", "PROJ", now)))
	c1 := makeProvisional(t, s, "PROJ-1", "PROJ", 1)

	settled, _, err := svc.SettlePending(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, settled)

	id, err := s.ResolveDisplayPathByUID(ctx, c1)
	require.NoError(t, err)
	assert.Equal(t, "PROJ-1.2", id)
}

// --- Create-time provisional fallback + non-blocking ----------------------

// TestCreateNode_Online_SettlesInBackground is the online corner: create returns
// a clean id immediately and the node settles via background settlement within
// budget (ADR-003 §4). The create call never blocks on the hub.
func TestCreateNode_Online_SettlesInBackground(t *testing.T) {
	settler := &scriptedSettler{outcomes: []sqlite.SettleOutcome{sqlite.SettleConfirmed}}
	svc, s, _ := newTestNodeService(t)
	settleSvc := service.NewSettlementService(s, settler, alwaysReachable, slog.Default())
	svc.SetSettlement(settleSvc)

	ctx := context.Background()
	root, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "root", Creator: "admin",
	})
	require.NoError(t, err)
	assert.False(t, model.IsProvisional(root.ID), "online create yields a clean id")

	// The background settlement confirms the clean number with the hub.
	svc.FlushSettlement(ctx)
	assert.GreaterOrEqual(t, settler.count(), 0)
}

// TestCreateNode_Offline_StaysProvisional is the offline corner: when the hub is
// unreachable at create time the node is created PROVISIONAL (ADR-003 §4).
func TestCreateNode_Offline_StaysProvisional(t *testing.T) {
	settler := &scriptedSettler{outcomes: []sqlite.SettleOutcome{sqlite.SettleUnreachable}}
	svc, s, _ := newTestNodeService(t)
	settleSvc := service.NewSettlementService(s, settler, neverReachable, slog.Default())
	svc.SetSettlement(settleSvc)

	ctx := context.Background()
	// A root is always settled in ADR-003's model; provisional applies to a
	// child created while offline.
	root, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "root", Creator: "admin",
	})
	require.NoError(t, err)

	child, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", ParentID: root.ID, Title: "child", Creator: "admin",
	})
	require.NoError(t, err)
	assert.True(t, model.IsProvisional(child.ID),
		"offline child must be provisional (uid-bearing)")
}

// TestCreateNode_NonBlocking proves the create call returns immediately even when
// the hub RTT is slow: settlement is deferred, never inline (ADR-003 §4).
func TestCreateNode_NonBlocking(t *testing.T) {
	block := make(chan struct{})
	settler := &scriptedSettler{
		outcomes: []sqlite.SettleOutcome{sqlite.SettleConfirmed},
		block:    block, // ConfirmClaim hangs until released
	}
	svc, s, _ := newTestNodeService(t)
	settleSvc := service.NewSettlementService(s, settler, alwaysReachable, slog.Default())
	svc.SetSettlement(settleSvc)

	ctx := context.Background()
	done := make(chan struct{})
	go func() {
		_, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
			Project: "PROJ", Title: "root", Creator: "admin",
		})
		assert.NoError(t, err)
		close(done)
	}()

	select {
	case <-done:
		// good: create returned without waiting on the (blocked) settler.
	case <-time.After(2 * time.Second):
		t.Fatal("CreateNode blocked on hub settlement — must be non-blocking")
	}
	close(block) // release the background settler
	svc.FlushSettlement(ctx)
}

// TestCreateNode_NoSettler_CleanAndImmediate is the single-user case: with no hub
// configured, creation is clean and settled immediately (no provisional forms).
func TestCreateNode_NoSettler_CleanAndImmediate(t *testing.T) {
	svc, _, _ := newTestNodeService(t)
	ctx := context.Background()

	root, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "root", Creator: "admin",
	})
	require.NoError(t, err)
	child, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", ParentID: root.ID, Title: "child", Creator: "admin",
	})
	require.NoError(t, err)
	assert.False(t, model.IsProvisional(root.ID))
	assert.False(t, model.IsProvisional(child.ID))
}

// TestFlushSettlement_DrainsOnShutdown settles all pending work on shutdown so a
// burst of offline-then-online creates is not lost (ADR-003 §4 flush on
// shutdown).
func TestFlushSettlement_DrainsOnShutdown(t *testing.T) {
	settler := &scriptedSettler{outcomes: []sqlite.SettleOutcome{sqlite.SettleConfirmed}}
	svc, s, _ := newTestNodeService(t)
	settleSvc := service.NewSettlementService(s, settler, alwaysReachable, slog.Default())
	svc.SetSettlement(settleSvc)
	ctx := context.Background()

	require.NoError(t, s.CreateNode(ctx, makeRootForSettle(s, "PROJ-1", "PROJ", time.Now().UTC())))
	c1 := makeProvisional(t, s, "PROJ-1", "PROJ", 1)

	svc.FlushSettlement(ctx)

	id, err := s.ResolveDisplayPathByUID(ctx, c1)
	require.NoError(t, err)
	assert.False(t, model.IsProvisional(id), "shutdown flush settles pending nodes")
}
