// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/relay/tick"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/sync/pushlock"
)

// A relay board still attempts its real relay pass after the hub exhausts
// the child deadline, and records both failures without extending the bound.
func TestAddressedSendChild_HungHub_RelayUsesExpiredDeadline(t *testing.T) {
	dsn, accepted := childTimeoutHub(t)
	t.Setenv(transport.EnvDSN, dsn)
	initCommandRelay(t, false)
	_ = seedOneNode(t)
	stack, err := openRelayStack()
	require.NoError(t, err)
	spy := &deadlineRelayStack{t: t, stack: stack}
	app.relayOpen = func() (relayStack, error) { return spy, nil }
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := newSyncPushCmdWithTimeout(500 * time.Millisecond)
	cmd.SetArgs([]string{"--on-addressed-send", "--insecure-tls"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err = cmd.ExecuteContext(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.ErrorIs(t, err, errRelayPass)
	assert.NoError(t, ctx.Err(), "the child bound must end the relay before the watchdog")
	assert.Equal(t, 1, spy.calls, "the relay phase must run after the hub deadline")
	select {
	case <-accepted:
	default:
		t.Fatal("hub never accepted a connection")
	}
	ok, class := waitAddressedPushRecord(t)
	assert.False(t, ok)
	assert.Equal(t, "timeout,"+relayFailedClass, class)
	lock, err := pushlock.Acquire(app.mtixDir)
	require.NoError(t, err)
	require.NoError(t, lock.Release())
}

type deadlineRelayStack struct {
	t     *testing.T
	stack relayStack
	calls int
}

func (s *deadlineRelayStack) Pass(ctx context.Context) (tick.Result, error) {
	s.calls++
	assert.ErrorIs(s.t, ctx.Err(), context.DeadlineExceeded, "relay must share the expired child deadline")
	return s.stack.Pass(ctx)
}

// Cancellation observed while a push reports a held lock must stop retries
// immediately, without spending the retry budget or pausing again.
func TestAddressedSendChild_HeldLock_CancellationStopsRetry(t *testing.T) {
	t.Setenv(transport.EnvDSN, fakeEnrolledDSN)
	initTestApp(t)
	_ = seedOneNode(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls, pauses := 0, 0
	o := testRun(transport.Options{})
	o.HeldTries = 50
	o.Wait = func(time.Duration) { pauses++ }
	o.Push = func(ctx context.Context, _ io.Writer, _ transport.Options) error {
		calls++
		probe, ok := ctx.Value(pushProbeKey{}).(*pushProbe)
		require.True(t, ok)
		probe.held.Store(true)
		cancel()
		return nil
	}
	err := pushWaitingForLock(ctx, io.Discard, o)
	require.ErrorIs(t, err, errPushHeld)
	assert.Equal(t, 1, calls)
	assert.Zero(t, pauses, "no retry pause after caller cancellation")
}

// Timeout must leave the whole pending prefix, including the addressed
// comment and its node creation, available to the next push.
func TestAddressedSendChild_HungHub_KeepsPendingPrefix(t *testing.T) {
	dsn, accepted := childTimeoutHub(t)
	t.Setenv(transport.EnvDSN, dsn)
	initTestApp(t)
	id := seedOneNode(t)
	_, err := app.promptSvc.AddAnnotationRequest(context.Background(), service.AnnotationRequest{
		NodeID: id, Text: "please review", Author: app.authorID, Addressee: "agent-b",
	})
	require.NoError(t, err)
	before, err := readPendingBatch(context.Background(), app.store, 100)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(before), 2, "the pending prefix includes creation and addressed comment")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := newSyncPushCmdWithTimeout(500 * time.Millisecond)
	cmd.SetArgs([]string{"--on-addressed-send", "--insecure-tls"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	require.Error(t, cmd.ExecuteContext(ctx))
	require.NoError(t, ctx.Err(), "the child's deadline must end the hung push")
	select {
	case <-accepted:
	default:
		t.Fatal("hub never accepted a connection")
	}
	after, err := readPendingBatch(context.Background(), app.store, 100)
	require.NoError(t, err)
	assert.Equal(t, before, after, "timeout must not acknowledge or remove any pending event")
}
