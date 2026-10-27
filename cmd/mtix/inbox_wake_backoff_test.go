// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"log/slog"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/testutil"
)

func cliBackoffOtherBoard(t *testing.T) *sqlite.Store {
	t.Helper()
	store, err := sqlite.New(filepath.Join(t.TempDir(), "other.db"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	clock := func() time.Time { return time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC) }
	nodes := service.NewNodeService(store, &service.NoopBroadcaster{}, &service.StaticConfig{}, slog.Default(), clock)
	prompts := service.NewPromptService(store, &service.NoopBroadcaster{}, slog.Default(), clock)
	node, err := nodes.CreateNode(t.Context(), &service.CreateNodeRequest{Project: "TEST", Title: "another board", Creator: "sender"})
	require.NoError(t, err)
	for range 3 {
		require.NoError(t, prompts.AddAnnotation(t.Context(), node.ID, "wake", "sender", "worker"))
	}
	return store
}

func cliBackoffDispatcher(t *testing.T, store *sqlite.Store, state *oplocal.State, sent *int) (*service.WakeDispatcher, []model.WakeKey) {
	t.Helper()
	host, err := state.HostID()
	require.NoError(t, err)
	uid, err := currentUserID()
	require.NoError(t, err)
	clock := func() time.Time { return time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC) }
	store.SetClock(clock)
	d, err := service.NewWakeDispatcher(service.WakeDispatcherOptions{
		Store: store, Alerts: service.NewAlerts(state, clock), Settings: state, BudgetState: state,
		Clock: clock, Held: func(string) {}, Send: func(context.Context, model.WakeAttempt) error { *sent++; return nil },
	})
	require.NoError(t, err)
	entries, err := store.InboxList(t.Context(), "worker")
	require.NoError(t, err)
	keys := make([]model.WakeKey, 0, len(entries))
	for _, entry := range entries {
		keys = append(keys, model.WakeKey{AgentID: "worker", HostID: host, UID: uid, EventSeq: entry.Seq, Epoch: 1})
	}
	return d, keys
}

func TestRunInboxAck_ResetsBackoffAcrossBoards(t *testing.T) {
	for _, cumulative := range []bool{false, true} {
		t.Run(map[bool]string{false: "selective", true: "cumulative"}[cumulative], func(t *testing.T) {
			initTestApp(t)
			state := testutil.OperatorState(t)
			t.Setenv("MTIX_WAKE_HOP", "0")
			first, _, _ := seedThreeAddressed(t, "worker")
			other := cliBackoffOtherBoard(t)
			sent := 0
			a, akeys := cliBackoffDispatcher(t, app.store, state, &sent)
			b, bkeys := cliBackoffDispatcher(t, other, state, &sent)
			require.NoError(t, a.Pass(t.Context(), akeys))
			require.NoError(t, b.Pass(t.Context(), bkeys[:2]))
			require.NoError(t, b.Pass(t.Context(), bkeys[2:]))
			require.Equal(t, 5, sent)
			require.NoError(t, runInboxAck("worker", []string{strconv.FormatInt(first, 10)}, cumulative))
			require.NoError(t, b.Pass(t.Context(), bkeys[2:]))
			require.Equal(t, 6, sent, "CLI acknowledgement must reset another board's host recipient")
		})
	}
}
