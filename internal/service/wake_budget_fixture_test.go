// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"log/slog"
	"os/user"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/testutil"
)

type wakeBudgetFixture struct {
	state     *oplocal.State
	alerts    *service.Alerts
	host, uid string
	sent      atomic.Int64
	now       time.Time
}

func newWakeBudgetFixture(t *testing.T, limit string) *wakeBudgetFixture {
	t.Helper()
	state := testutil.OperatorState(t)
	t.Setenv("MTIX_WAKE_HOP", "0")
	// Frequency tests isolate the rate limit from the separate unacked guard.
	threshold := "1000"
	require.NoError(t, state.UpdateWakeSetting(t.Context(), oplocal.WakeSettingUpdate{Key: "wake.backoff_after_unacked", Value: &threshold}, nil))
	if limit != "" {
		require.NoError(t, state.UpdateWakeSetting(t.Context(), oplocal.WakeSettingUpdate{Key: "wake.wakes_per_hour", Value: &limit}, nil))
	}
	host, err := state.HostID()
	require.NoError(t, err)
	operator, err := user.Current()
	require.NoError(t, err)
	f := &wakeBudgetFixture{state: state, host: host, uid: operator.Uid, now: time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)}
	f.alerts = service.NewAlerts(state, func() time.Time { return f.now })
	return f
}

type wakeBudgetBoard struct {
	fixture *wakeBudgetFixture
	store   *sqlite.Store
	path    string
	keys    []model.WakeKey
}

func (f *wakeBudgetFixture) board(t *testing.T, count int) *wakeBudgetBoard {
	t.Helper()
	return f.boardFor(t, count, "budget-worker")
}

func (f *wakeBudgetFixture) boardFor(t *testing.T, count int, agent string) *wakeBudgetBoard {
	t.Helper()
	b := &wakeBudgetBoard{fixture: f, path: filepath.Join(t.TempDir(), "wake.db")}
	b.open(t)
	t.Cleanup(func() { require.NoError(t, b.store.Close()) })
	clock := func() time.Time { return f.now }
	nodes := service.NewNodeService(b.store, &service.NoopBroadcaster{}, &service.StaticConfig{}, slog.Default(), clock)
	prompts := service.NewPromptService(b.store, &service.NoopBroadcaster{}, slog.Default(), clock)
	node, err := nodes.CreateNode(t.Context(), &service.CreateNodeRequest{Project: "TEST", Title: "recipient budget", Creator: "sender"})
	require.NoError(t, err)
	for range count {
		require.NoError(t, prompts.AddAnnotation(t.Context(), node.ID, "budget event", "sender", agent))
	}
	entries, err := b.store.InboxList(t.Context(), agent)
	require.NoError(t, err)
	require.Len(t, entries, count)
	for _, entry := range entries {
		b.keys = append(b.keys, model.WakeKey{AgentID: agent, HostID: f.host, UID: f.uid, EventSeq: entry.Seq, Epoch: 1})
	}
	return b
}

func (b *wakeBudgetBoard) open(t *testing.T) {
	t.Helper()
	var err error
	b.store, err = sqlite.New(b.path, slog.Default())
	require.NoError(t, err)
	b.store.SetClock(func() time.Time { return b.fixture.now })
}

func (b *wakeBudgetBoard) dispatcher(t *testing.T, ack bool, sendErr error) *service.WakeDispatcher {
	t.Helper()
	return b.dispatcherWithStore(t, b.store, ack, sendErr)
}

func (b *wakeBudgetBoard) dispatcherWithStore(t *testing.T, backend service.WakeStore, ack bool, sendErr error) *service.WakeDispatcher {
	t.Helper()
	f := b.fixture
	d, err := service.NewWakeDispatcher(service.WakeDispatcherOptions{
		Store: backend, Alerts: f.alerts, Settings: f.state, Held: func(string) {},
		BudgetState: f.state, Clock: func() time.Time { return f.now },
		Send: func(ctx context.Context, attempt model.WakeAttempt) error {
			f.sent.Add(1)
			if ack {
				if err := service.NewInboxService(b.store).InboxAck(ctx, attempt.Key.AgentID, attempt.Key.EventSeq); err != nil {
					return err
				}
			}
			return sendErr
		},
	})
	require.NoError(t, err)
	return d
}

func budgetLedgerRows(t *testing.T, b *wakeBudgetBoard) int64 {
	t.Helper()
	stats, err := b.store.WakeLedgerStats(t.Context())
	require.NoError(t, err)
	return stats.Rows
}
