// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// countingStore counts the full reads and counts a wait runs against a real
// sqlite store (MTIX-326). Embedding keeps every other capability (the change
// token, the waiter registrar) as the real store provides it.
type countingStore struct {
	*sqlite.Store
	lists, counts atomic.Int64
}

func (c *countingStore) InboxList(ctx context.Context, agent string) ([]sqlite.InboxEvent, error) {
	c.lists.Add(1)
	return c.Store.InboxList(ctx, agent)
}

func (c *countingStore) InboxCount(ctx context.Context, agent string) (int64, int64, error) {
	c.counts.Add(1)
	return c.Store.InboxCount(ctx, agent)
}

var (
	realWaitKey   = sqlite.RegistrationKey{AgentID: "w", HostID: "h", UID: "1"}
	realWaitTouch = sqlite.SessionTouch{HostID: "h", UID: "1", Handle: "s"}
)

// newRegisteredStore opens a real store holding a node, an addressee registration
// "w" with a live session handle, and returns the context a CLI wait carries.
func newRegisteredStore(t *testing.T, clock func() time.Time) (*countingStore, context.Context) {
	t.Helper()
	st, err := sqlite.New(filepath.Join(t.TempDir(), "wait.db"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	st.SetClock(clock)
	now := clock()
	require.NoError(t, st.CreateNode(t.Context(), &model.Node{
		ID: "TEST-1", Project: "TEST", Seq: 1, Title: "Wait target",
		NodeType: model.NodeTypeStory, Status: model.StatusOpen,
		Priority: model.PriorityMedium, Weight: 1, Creator: "tester", CreatedAt: now, UpdatedAt: now,
	}))
	stamp := now.UTC().Format(time.RFC3339)
	_, err = st.RegisterSessionAtomic(t.Context(), realWaitKey, func(_ *sqlite.Registration, _ int64) (*sqlite.Registration, error) {
		return &sqlite.Registration{
			AgentID: "w", HostID: "h", UID: "1", Harness: "claude", SessionHandle: "s",
			Origin: "test", Epoch: 1, RegisteredAt: stamp, LastSeen: stamp,
			RegistrarVersion: "test", AttachProtocol: 1,
		}, nil
	})
	require.NoError(t, err)
	return &countingStore{Store: st}, sqlite.WithSessionTouch(t.Context(), realWaitTouch)
}

func fixedClock() time.Time { return time.Date(2026, 11, 28, 9, 0, 0, 0, time.UTC) }

// MTIX-326 review S1: a registered `mtix inbox --wait` commits its own session
// touch and read stamp. Those must not turn into a full read every 2 s: after
// the entry read an idle wait runs no full read, for longer than that cadence.
func TestInboxReadWait_RealStoreRegisteredIdle_NoFullReadAfterEntry(t *testing.T) {
	st, ctx := newRegisteredStore(t, fixedClock)
	svc := NewInboxService(st).WithInboxPolicy(eligibleNow())
	res, err := svc.InboxReadWait(ctx, "w", idleWaitLong+inboxReadPollInterval, InboxReadOptions{})
	require.NoError(t, err)
	assert.Empty(t, res.Events)
	assert.Equal(t, int64(1), st.lists.Load(), "the entry read only; the wait's own commits cause no read")
	assert.LessOrEqual(t, st.counts.Load(), int64(3), "entry probe plus a count for the wait's own commits")
}

// The same with another connection committing unrelated rows every 250 ms
// (other agents' touches and an armed hook waiter): counts only, no full read.
func TestInboxReadWait_RealStoreOtherWritersCommit_NoFullRead(t *testing.T) {
	st, ctx := newRegisteredStore(t, fixedClock)
	svc := NewInboxService(st).WithInboxPolicy(eligibleNow())
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(250 * time.Millisecond)
		defer tick.Stop()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			case <-tick.C:
				_ = st.SetAnnotations(context.Background(), "TEST-1", []model.Annotation{{
					ID: "o" + string(rune('a'+i%26)) + string(rune('a'+i/26)), Author: "other", Text: "x",
					Addressee: "someone-else", CreatedAt: fixedClock(),
				}})
			}
		}
	}()
	_, err := svc.InboxReadWait(ctx, "w", idleWaitLong+inboxReadPollInterval, InboxReadOptions{})
	close(stop)
	<-done
	require.NoError(t, err)
	assert.Equal(t, int64(1), st.lists.Load(), "unrelated commits never cause a full read")
}

// The lease: re-reads while parked carry no session touch, and one touching
// re-read runs once sqlite.InboxReadThrottle has passed on the wait's clock.
func TestInboxReadWait_RealStoreParkedReRead_TouchesOncePerThrottle(t *testing.T) {
	var now atomic.Value
	now.Store(fixedClock())
	clock := func() time.Time { return now.Load().(time.Time) }
	st, ctx := newRegisteredStore(t, clock)
	var offset atomic.Int64
	mono := func() time.Time { return time.Now().Add(time.Duration(offset.Load())) }
	svc := NewInboxService(st).WithInboxPolicy(eligibleNow()).
		WithWaitSafetyFloor(150 * time.Millisecond).WithWaitMonotonic(mono)
	lastSeen := func() string {
		regs, err := st.ListRegistrations(t.Context(), "h", "1")
		require.NoError(t, err)
		require.Len(t, regs, 1)
		return regs[0].LastSeen
	}
	waitCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		_, _ = svc.InboxReadWait(waitCtx, "w", 0, InboxReadOptions{})
	}()
	require.Eventually(t, func() bool { return st.lists.Load() >= 1 }, 3*time.Second, 20*time.Millisecond)
	entrySeen := lastSeen()
	now.Store(fixedClock().Add(time.Hour)) // any later touch would now write a different last_seen
	require.Eventually(t, func() bool { return st.lists.Load() >= 3 }, 3*time.Second, 20*time.Millisecond, "re-reads ran")
	assert.Equal(t, entrySeen, lastSeen(), "re-reads inside the throttle do not touch the session")
	offset.Store(int64(sqlite.InboxReadThrottle + time.Second))
	require.Eventually(t, func() bool { return lastSeen() != entrySeen }, 3*time.Second, 20*time.Millisecond, "one touching re-read after the throttle")
	cancel()
	<-finished
}
