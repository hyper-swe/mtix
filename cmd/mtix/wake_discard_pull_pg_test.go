// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/testutil"
)

type discardPullWakeRig struct {
	scanner   *service.WakeScanner
	sent      []int64
	host, uid string
}

type discardPullLiveness struct{}

func (discardPullLiveness) Probe(context.Context, sqlite.Registration) service.LiveState {
	return service.LiveAlive
}

func newDiscardPullWakeRig(t *testing.T) *discardPullWakeRig {
	t.Helper()
	state := testutil.OperatorState(t)
	values, reports, err := state.WakeSettings("worker")
	require.NoError(t, err)
	require.Empty(t, reports)
	require.Equal(t, "5", values["wake.backoff_after_unacked"].Value, "exercise the default backoff")
	host, err := state.HostID()
	require.NoError(t, err)
	uid, err := currentUserID()
	require.NoError(t, err)
	f := &discardPullWakeRig{host: host, uid: uid}
	_, err = app.store.RegisterSessionAtomic(t.Context(), sqlite.RegistrationKey{AgentID: "worker", HostID: host, UID: uid},
		func(_ *sqlite.Registration, tail int64) (*sqlite.Registration, error) {
			return &sqlite.Registration{AgentID: "worker", HostID: host, UID: uid, Harness: "claude", Epoch: 1, Origin: "test", HighWater: tail,
				RegisteredAt: time.Now().UTC().Format(time.RFC3339), LastSeen: time.Now().UTC().Format(time.RFC3339), RegistrarVersion: "test", AttachProtocol: 1}, nil
		})
	require.NoError(t, err)
	dispatcher, err := service.NewWakeDispatcher(service.WakeDispatcherOptions{
		Store: app.store, Alerts: service.NewAlerts(state, time.Now), Settings: state, BudgetState: state, Clock: time.Now,
		HostID: host, UID: uid, Held: func(string) {},
		Send: func(_ context.Context, attempt model.WakeAttempt) error {
			f.sent = append(f.sent, attempt.Key.EventSeq)
			return nil
		},
	})
	require.NoError(t, err)
	f.scanner, err = service.NewWakeScanner(service.WakeScannerOptions{
		Store: app.store, Dispatcher: dispatcher, Liveness: discardPullLiveness{}, HostID: host, UID: uid, Clock: time.Now,
		Wakeable: func(context.Context, string) (bool, string, error) { return true, "", nil },
		Writable: func() error { return nil }, ExpireStale: func(context.Context) error { return nil },
	})
	require.NoError(t, err)
	return f
}

func TestWakeDiscardPull_PGHistoryDoesNotWake_FreshMailWakesOnce(t *testing.T) {
	pool := openCmdHub(t)
	t.Setenv(sqlite.AuthorIDEnv, "sender")
	t.Setenv("MTIX_WAKE_HOP", "0")
	initTestApp(t)
	node, err := app.nodeSvc.CreateNode(t.Context(), &service.CreateNodeRequest{Project: "TEST", Title: "history", Creator: "sender"})
	require.NoError(t, err)
	for range 3 {
		require.NoError(t, app.promptSvc.AddAnnotation(t.Context(), node.ID, "history", "sender", "worker"))
	}
	history, err := app.store.ReadPendingEvents(t.Context(), 100)
	require.NoError(t, err)
	require.Len(t, history, 4)
	pushToHub(t, pool, history...)
	require.NoError(t, markPushed(t.Context(), app.store, eventIDs(history)))
	f := newDiscardPullWakeRig(t)
	require.NoError(t, sqlite.DiscardLocal(t.Context(), app.store, app.mtixDir))
	var stdout, stderr bytes.Buffer
	require.NoError(t, runSyncPull(t.Context(), &stdout, &stderr, nil, cloudOpts, 2), stderr.String())
	pulled, err := app.store.InboxList(t.Context(), "worker")
	require.NoError(t, err)
	require.Len(t, pulled, 3, "the real hub and pull restored all addressed history")
	tail, err := app.store.JournalTail(t.Context())
	require.NoError(t, err)
	for range 3 {
		require.NoError(t, f.scanner.Scan(t.Context()))
	}
	assert.Empty(t, f.sent, "bootstrap history is unread inbox mail, not a fresh wake backlog")
	regs, err := app.store.ListRegistrations(t.Context(), f.host, f.uid)
	require.NoError(t, err)
	require.Len(t, regs, 1)
	assert.Equal(t, tail, regs[0].HighWater)
	assert.False(t, regs[0].PendingHold)
	require.NoError(t, app.promptSvc.AddAnnotation(t.Context(), node.ID, "genuinely new", "sender", "worker"))
	freshTail, err := app.store.JournalTail(t.Context())
	require.NoError(t, err)
	require.Greater(t, freshTail, tail)
	for range 3 {
		require.NoError(t, f.scanner.Scan(t.Context()))
	}
	assert.Equal(t, []int64{freshTail}, f.sent, "default backoff leaves one fresh wake, exactly once")
}
