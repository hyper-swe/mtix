// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// racyStore runs hook once, right after the first GetNode returns: the
// window between the service's ownership read and its write (MTIX-325.1).
type racyStore struct {
	store.Store
	once sync.Once
	hook func()
}

func (r *racyStore) GetNode(ctx context.Context, id string) (*model.Node, error) {
	n, err := r.Store.GetNode(ctx, id)
	r.once.Do(func() { r.hook() })
	return n, err
}

// newRacyService returns a service whose store runs hook after the first read.
func newRacyService(t *testing.T) (*service.NodeService, *sqlite.Store, *racyStore) {
	t.Helper()
	svc, s, wrapped := newServiceOver(t, func(s *sqlite.Store) store.Store {
		return &racyStore{Store: s, hook: func() {}}
	})
	return svc, s, wrapped.(*racyStore)
}

// newServiceOver builds a NodeService on a real SQLite store seen through wrap.
func newServiceOver(t *testing.T, wrap func(*sqlite.Store) store.Store) (*service.NodeService, *sqlite.Store, store.Store) {
	t.Helper()
	s, err := sqlite.New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	wrapped := wrap(s)
	svc := service.NewNodeService(wrapped, &service.NoopBroadcaster{}, &service.StaticConfig{}, slog.Default(),
		func() time.Time { return time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC) })
	return svc, s, wrapped
}

// barrierStore holds the first n GetNode calls after arming until all n have
// read, so every taker reads the assignee before any of them writes. The
// check-then-write window is then open on every run, and only the store's
// compare-and-set can keep a second taker out (MTIX-325.1).
type barrierStore struct {
	store.Store
	armed   atomic.Bool
	n       int32
	arrived atomic.Int32
	release chan struct{}
}

func (b *barrierStore) GetNode(ctx context.Context, id string) (*model.Node, error) {
	n, err := b.Store.GetNode(ctx, id)
	if !b.armed.Load() {
		return n, err
	}
	c := b.arrived.Add(1)
	if c == b.n {
		close(b.release)
	}
	if c <= b.n {
		select {
		case <-b.release:
		case <-time.After(30 * time.Second):
		}
	}
	return n, err
}

func reassign(t *testing.T, s *sqlite.Store, id, to string) func() {
	return func() {
		require.NoError(t, s.UpdateNode(context.Background(), id, assigneeUpdate(to)))
	}
}

// TestUpdateNode_AssigneeCAS_ConcurrentReassign_Refused verifies a reassign
// landing between the ownership read and the write is refused (MTIX-325.1).
func TestUpdateNode_AssigneeCAS_ConcurrentReassign_Refused(t *testing.T) {
	for _, tc := range []struct{ name, owner, actor, val string }{
		{"owner_clears", "agent-a", "agent-a", ""},
		{"owner_hands_over", "agent-a", "agent-a", "agent-b"},
		{"unclaimed_taken", "", "agent-b", "agent-b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, s, rs := newRacyService(t)
			id := claimedTestNode(t, svc, "agent-a")
			if tc.owner == "" {
				require.NoError(t, svc.UnclaimNode(context.Background(), id, "free", "agent-a", false))
			}
			rs.once = sync.Once{}
			rs.hook = reassign(t, s, id, "agent-c")

			err := svc.UpdateNode(updateAs(tc.actor, false, ""), id, assigneeUpdate(tc.val))

			require.ErrorIs(t, err, model.ErrAlreadyClaimed)
			got, getErr := s.GetNode(context.Background(), id)
			require.NoError(t, getErr)
			assert.Equal(t, "agent-c", got.Assignee)
		})
	}
}

// TestUpdateNode_AssigneeCAS_ForcedConcurrentReassign_NoStaleAudit verifies a
// forced change racing a reassign is refused and writes no audit entry.
func TestUpdateNode_AssigneeCAS_ForcedConcurrentReassign_NoStaleAudit(t *testing.T) {
	svc, s, rs := newRacyService(t)
	id := claimedTestNode(t, svc, "agent-a")
	rs.once = sync.Once{}
	rs.hook = reassign(t, s, id, "agent-c")

	err := svc.UpdateNode(updateAs("agent-b", true, "stuck"), id, assigneeUpdate(""))

	require.ErrorIs(t, err, model.ErrAlreadyClaimed)
	got, getErr := s.GetNode(context.Background(), id)
	require.NoError(t, getErr)
	assert.Equal(t, "agent-c", got.Assignee)
	acts, actErr := s.GetActivity(context.Background(), id, 50, 0)
	require.NoError(t, actErr)
	for _, a := range acts {
		assert.NotContains(t, a.Text, "forced")
	}
}

// TestUnclaimNode_ConcurrentReassign_Refused verifies an unclaim racing a
// reassign cannot release the new owner's claim (MTIX-325.1).
func TestUnclaimNode_ConcurrentReassign_Refused(t *testing.T) {
	svc, s, rs := newRacyService(t)
	id := claimedTestNode(t, svc, "agent-a")
	rs.once = sync.Once{}
	rs.hook = reassign(t, s, id, "agent-c")

	err := svc.UnclaimNode(context.Background(), id, "done", "agent-a", false)

	require.ErrorIs(t, err, model.ErrAlreadyClaimed)
	got, getErr := s.GetNode(context.Background(), id)
	require.NoError(t, getErr)
	assert.Equal(t, "agent-c", got.Assignee)
	assert.Equal(t, model.StatusInProgress, got.Status)
}

// TestUpdateNode_AssigneeCAS_ParallelTakers_ExactlyOneWins races agents
// claiming one unclaimed node through update under -race (MTIX-325.1). A
// barrier after each taker's ownership read forces every read to precede every
// write, so without the compare-and-set all takers win and the test fails on
// every run, not by chance.
func TestUpdateNode_AssigneeCAS_ParallelTakers_ExactlyOneWins(t *testing.T) {
	const n = 12
	svc, s, wrapped := newServiceOver(t, func(s *sqlite.Store) store.Store {
		return &barrierStore{Store: s, n: n, release: make(chan struct{})}
	})
	id := claimedTestNode(t, svc, "agent-a")
	require.NoError(t, svc.UnclaimNode(context.Background(), id, "free", "agent-a", false))
	wrapped.(*barrierStore).armed.Store(true)

	var mu sync.Mutex
	var winners []string
	var losers []error
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		agent := fmt.Sprintf("taker-%d", i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			err := svc.UpdateNode(updateAs(agent, false, ""), id, assigneeUpdate(agent))
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				winners = append(winners, agent)
				return
			}
			losers = append(losers, err)
		}()
	}
	close(start)
	wg.Wait()

	require.Len(t, winners, 1, "exactly one taker must win")
	require.Len(t, losers, n-1)
	for _, err := range losers {
		assert.ErrorIs(t, err, model.ErrAlreadyClaimed)
	}
	got, err := s.GetNode(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, winners[0], got.Assignee)
}
