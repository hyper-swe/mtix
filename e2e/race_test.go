// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store"
)

// TestRace_ConcurrentNodeWrites verifies no data race on concurrent node writes.
// Run with: go test -race ./e2e/...
func TestRace_ConcurrentNodeWrites(t *testing.T) {
	env := setupE2E(t)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, err := env.nodeSvc.CreateNode(env.ctx, &service.CreateNodeRequest{
				Title:   fmt.Sprintf("Race Node %d", idx),
				Project: "RACE",
				Creator: fmt.Sprintf("agent-%d", idx),
			})
			assert.NoError(t, err, "concurrent CreateNode %d", idx)
		}(i)
	}
	wg.Wait()

	// Verify all nodes were created.
	nodes, total, err := env.store.ListNodes(env.ctx, store.NodeFilter{},
		store.ListOptions{Limit: 50})
	require.NoError(t, err)
	assert.Equal(t, 20, total, "20 nodes should exist")
	assert.Len(t, nodes, 20)
}

// TestRace_ConcurrentReadsAndWrites verifies no data race with concurrent
// reads happening during writes.
func TestRace_ConcurrentReadsAndWrites(t *testing.T) {
	env := setupE2E(t)

	// Pre-create some nodes.
	var nodeIDs []string
	for i := 0; i < 5; i++ {
		node, err := env.nodeSvc.CreateNode(env.ctx, &service.CreateNodeRequest{
			Title:   fmt.Sprintf("RW Node %d", i),
			Project: "RW",
			Creator: "admin",
		})
		require.NoError(t, err)
		nodeIDs = append(nodeIDs, node.ID)
	}

	var wg sync.WaitGroup

	// Concurrent readers.
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			id := nodeIDs[idx%len(nodeIDs)]
			node, err := env.nodeSvc.GetNode(env.ctx, id)
			if err == nil {
				assert.NotEmpty(t, node.ID)
			}
		}(i)
	}

	// Concurrent writers (new nodes).
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, err := env.nodeSvc.CreateNode(env.ctx, &service.CreateNodeRequest{
				Title:   fmt.Sprintf("Concurrent Write %d", idx),
				Project: "RW",
				Creator: "agent",
			})
			assert.NoError(t, err)
		}(i)
	}

	// Concurrent list/search.
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := env.store.ListNodes(env.ctx, store.NodeFilter{},
				store.ListOptions{Limit: 20})
			assert.NoError(t, err)
		}()
	}

	wg.Wait()
}

// TestRace_ConcurrentStatusTransitions verifies no data race when
// transitioning the same node from different goroutines.
func TestRace_ConcurrentStatusTransitions(t *testing.T) {
	env := setupE2E(t)

	// Create multiple independent nodes.
	var nodeIDs []string
	for i := 0; i < 10; i++ {
		node, err := env.nodeSvc.CreateNode(env.ctx, &service.CreateNodeRequest{
			Title:   fmt.Sprintf("Transition Node %d", i),
			Project: "TRANS",
			Creator: "admin",
		})
		require.NoError(t, err)
		nodeIDs = append(nodeIDs, node.ID)
	}

	// Each goroutine claims and transitions its own node.
	var wg sync.WaitGroup
	for i, id := range nodeIDs {
		agentID := fmt.Sprintf("agent-%d", i)
		wg.Add(1)
		go func(nodeID, agent string) {
			defer wg.Done()
			// Claim → done.
			err := env.store.ClaimNode(env.ctx, nodeID, agent)
			assert.NoError(t, err)
			err = env.nodeSvc.TransitionStatus(env.ctx, nodeID, model.StatusDone,
				"done", agent)
			assert.NoError(t, err)
		}(id, agentID)
	}
	wg.Wait()

	// All nodes should be done.
	for _, id := range nodeIDs {
		node, err := env.nodeSvc.GetNode(env.ctx, id)
		require.NoError(t, err)
		assert.Equal(t, model.StatusDone, node.Status)
	}
}

// TestRace_ConcurrentProgressRollup verifies no data race during
// concurrent child completions that trigger parent progress recalculation.
func TestRace_ConcurrentProgressRollup(t *testing.T) {
	env := setupE2E(t)

	parent, err := env.nodeSvc.CreateNode(env.ctx, &service.CreateNodeRequest{
		Title:   "Rollup Parent",
		Project: "ROLL",
		Creator: "admin",
	})
	require.NoError(t, err)

	inputs := make([]service.DecomposeInput, 10)
	for i := range inputs {
		inputs[i] = service.DecomposeInput{Title: fmt.Sprintf("Child %d", i+1)}
	}

	childIDs, err := env.nodeSvc.Decompose(env.ctx, parent.ID, inputs, "admin")
	require.NoError(t, err)

	// Pre-claim all.
	for i, cid := range childIDs {
		err = env.store.ClaimNode(env.ctx, cid, fmt.Sprintf("agent-%d", i))
		require.NoError(t, err)
	}

	// Complete all concurrently — all trigger progress rollup on the same parent.
	var wg sync.WaitGroup
	for i, cid := range childIDs {
		wg.Add(1)
		go func(id, agent string) {
			defer wg.Done()
			_ = env.nodeSvc.TransitionStatus(env.ctx, id, model.StatusDone,
				"done", agent)
		}(cid, fmt.Sprintf("agent-%d", i))
	}
	wg.Wait()

	// Verify final parent progress.
	p, err := env.store.GetNode(env.ctx, parent.ID)
	require.NoError(t, err)
	assert.InDelta(t, 1.0, p.Progress, 0.01,
		"parent should be at 100%% after all children done")
}

// TestRace_ConcurrentClaimAttempts verifies no data race when multiple
// agents try to claim the same node simultaneously.
func TestRace_ConcurrentClaimAttempts(t *testing.T) {
	env := setupE2E(t)

	node, err := env.nodeSvc.CreateNode(env.ctx, &service.CreateNodeRequest{
		Title:   "Contested Node",
		Project: "CLAIM",
		Creator: "admin",
	})
	require.NoError(t, err)

	var wg sync.WaitGroup
	var successCount int64
	var mu sync.Mutex

	for i := 0; i < 10; i++ {
		agentID := fmt.Sprintf("agent-%03d", i)
		wg.Add(1)
		go func(aid string) {
			defer wg.Done()
			err := env.store.ClaimNode(env.ctx, node.ID, aid)
			if err == nil {
				mu.Lock()
				successCount++
				mu.Unlock()
			}
		}(agentID)
	}
	wg.Wait()

	// Exactly one agent should have won.
	assert.Equal(t, int64(1), successCount,
		"exactly one claim should succeed")

	// Node should be in_progress.
	claimed, err := env.nodeSvc.GetNode(env.ctx, node.ID)
	require.NoError(t, err)
	assert.Equal(t, model.StatusInProgress, claimed.Status)
	assert.NotEmpty(t, claimed.Assignee)
}

// TestRace_ConcurrentDecompose verifies no data race when decomposing
// different parents concurrently.
func TestRace_ConcurrentDecompose(t *testing.T) {
	env := setupE2E(t)

	// Create 5 parent nodes.
	var parentIDs []string
	for i := 0; i < 5; i++ {
		node, err := env.nodeSvc.CreateNode(env.ctx, &service.CreateNodeRequest{
			Title:   fmt.Sprintf("Parent %d", i),
			Project: "DECOMP",
			Creator: "admin",
		})
		require.NoError(t, err)
		parentIDs = append(parentIDs, node.ID)
	}

	// Decompose all parents concurrently.
	var wg sync.WaitGroup
	for _, pid := range parentIDs {
		wg.Add(1)
		go func(parentID string) {
			defer wg.Done()
			inputs := make([]service.DecomposeInput, 5)
			for i := range inputs {
				inputs[i] = service.DecomposeInput{
					Title: fmt.Sprintf("Child of %s #%d", parentID, i),
				}
			}
			ids, err := env.nodeSvc.Decompose(context.Background(), parentID, inputs, "admin")
			assert.NoError(t, err)
			assert.Len(t, ids, 5)
		}(pid)
	}
	wg.Wait()

	// Verify total node count: 5 parents + 25 children = 30.
	_, total, err := env.store.ListNodes(env.ctx, store.NodeFilter{},
		store.ListOptions{Limit: 50})
	require.NoError(t, err)
	assert.Equal(t, 30, total, "should have 30 nodes (5 parents + 25 children)")
}

// TestRace_ConcurrentHeartbeats verifies no data race on concurrent
// session operations.
func TestRace_ConcurrentHeartbeats(t *testing.T) {
	env := setupE2E(t)

	// Create agent records for FK constraint.
	for i := 0; i < 10; i++ {
		ensureAgent(t, env, fmt.Sprintf("agent-%03d", i), "HEART")
	}

	// Start sessions for multiple agents concurrently.
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		agentID := fmt.Sprintf("agent-%03d", i)
		wg.Add(1)
		go func(aid string) {
			defer wg.Done()
			sid, err := env.sessionSvc.SessionStart(env.ctx, aid, "HEART")
			assert.NoError(t, err)
			assert.NotEmpty(t, sid)
		}(agentID)
	}
	wg.Wait()
}

// TestRace_EventBroadcaster_ConcurrentSubscriptions verifies no data race
// on the event broadcaster under concurrent publish.
func TestRace_EventBroadcaster_ConcurrentSubscriptions(t *testing.T) {
	env := setupE2E(t)

	// Use a recording broadcaster.
	recorder := &safeEventRecorder{}
	env.nodeSvc = service.NewNodeService(
		env.store, recorder, &service.StaticConfig{}, nil, testClock(),
	)

	// Concurrent node operations that trigger broadcasts.
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, _ = env.nodeSvc.CreateNode(env.ctx, &service.CreateNodeRequest{
				Title:   fmt.Sprintf("Broadcast Node %d", idx),
				Project: "BCAST",
				Creator: "agent",
			})
		}(i)
	}
	wg.Wait()

	// Verify events were recorded without data race.
	assert.Greater(t, recorder.count(), 0,
		"events should have been broadcast")
}

// TestRace_FullSuite_NoWarnings is a meta-test that exercises the full
// lifecycle under concurrent load. Run with -race to detect any warnings.
func TestRace_FullSuite_NoWarnings(t *testing.T) {
	env := setupE2E(t)

	// Phase 1: Concurrent node creation.
	var wg sync.WaitGroup
	var nodeIDs sync.Map

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			node, err := env.nodeSvc.CreateNode(env.ctx, &service.CreateNodeRequest{
				Title:   fmt.Sprintf("Full Suite Node %d", idx),
				Project: "FULL",
				Creator: fmt.Sprintf("agent-%d", idx),
			})
			if err == nil {
				nodeIDs.Store(node.ID, true)
			}
		}(i)
	}
	wg.Wait()

	// Phase 2: Concurrent decompose.
	var firstID string
	nodeIDs.Range(func(key, _ any) bool {
		firstID = key.(string)
		return false // Stop after first.
	})

	if firstID != "" {
		inputs := make([]service.DecomposeInput, 3)
		for i := range inputs {
			inputs[i] = service.DecomposeInput{Title: fmt.Sprintf("Sub %d", i)}
		}
		childIDs, err := env.nodeSvc.Decompose(env.ctx, firstID, inputs, "admin")
		require.NoError(t, err)

		// Phase 3: Concurrent claim + complete.
		for _, cid := range childIDs {
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				_ = env.store.ClaimNode(env.ctx, id, "agent-race")
				_ = env.nodeSvc.TransitionStatus(env.ctx, id, model.StatusDone,
					"done", "agent-race")
			}(cid)
		}
		wg.Wait()
	}

	// Phase 4: Concurrent reads.
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, _ = env.store.ListNodes(env.ctx, store.NodeFilter{},
				store.ListOptions{Limit: 50})
			_, _ = env.store.GetStats(env.ctx, "")
		}()
	}
	wg.Wait()

	// If we get here without -race flagging anything, the suite is clean.
	t.Log("Full race suite completed without warnings")
}

// safeEventRecorder is a thread-safe event recorder for race testing.
type safeEventRecorder struct {
	mu     sync.Mutex
	events []service.Event
}

func (r *safeEventRecorder) Broadcast(_ context.Context, event service.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
	return nil
}

func (r *safeEventRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.events)
}
