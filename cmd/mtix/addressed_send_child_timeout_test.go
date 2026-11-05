// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/sync/pushlock"
)

// The shared runner used by the detached CLI child must supply its own bound,
// even when its caller supplies an unbounded background context (FR-20.8).
func TestAddressedSendChild_DefaultRunnerSuppliesFiveMinuteDeadline(t *testing.T) {
	t.Setenv(transport.EnvDSN, fakeEnrolledDSN)
	initTestApp(t)
	_ = seedOneNode(t)
	o := testRun(transport.Options{})
	var observed context.Context
	start := time.Now()
	o.Push = func(ctx context.Context, _ io.Writer, _ transport.Options) error {
		observed = ctx
		return nil
	}
	require.NoError(t, runAddressedSendPush(context.Background(), io.Discard, o))
	require.NotNil(t, observed)
	deadline, ok := observed.Deadline()
	require.True(t, ok, "the detached child runner must impose a deadline without its caller doing so")
	assert.WithinDuration(t, start.Add(5*time.Minute), deadline, time.Second)
	assert.ErrorIs(t, observed.Err(), context.Canceled, "returning must release the deadline timer")
}

func TestAddressedSendChild_ProductionCommandHonorsEarlierDeadline(t *testing.T) {
	dsn, accepted := childTimeoutHub(t)
	t.Setenv(transport.EnvDSN, dsn)
	initTestApp(t)
	_ = seedOneNode(t)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	cmd := newSyncPushCmd()
	cmd.SetArgs([]string{"--on-addressed-send", "--insecure-tls"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	require.Error(t, cmd.ExecuteContext(ctx))
	assert.ErrorIs(t, ctx.Err(), context.DeadlineExceeded)
	select {
	case <-accepted:
	default:
		t.Fatal("the production command did not reach the hung hub")
	}
	ok, class := waitAddressedPushRecord(t)
	assert.False(t, ok)
	assert.Equal(t, "timeout", class)
	lock, err := pushlock.Acquire(app.mtixDir)
	require.NoError(t, err)
	require.NoError(t, lock.Release())
}

// A real CLI command must interrupt the transport's blocked read, record the
// timeout and free its native lock. A second send must be able to push again.
func TestAddressedSendChild_HungHub_TimeoutReleasesLockForNextSend(t *testing.T) {
	dsn, accepted := childTimeoutHub(t)
	t.Setenv(transport.EnvDSN, dsn)
	initTestApp(t)
	_ = seedOneNode(t)
	for attempt := 0; attempt < 2; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		held := false
		ctx = context.WithValue(ctx, pushBeforeReleaseKey{}, func() error {
			lock, err := pushlock.Acquire(app.mtixDir)
			assert.ErrorIs(t, err, pushlock.ErrLockHeld, "the real push must own the lock")
			if lock != nil {
				require.NoError(t, lock.Release())
			}
			held = true
			return nil
		})
		cmd := newSyncPushCmdWithTimeout(500 * time.Millisecond)
		cmd.SetArgs([]string{"--on-addressed-send", "--insecure-tls"})
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		require.Error(t, cmd.ExecuteContext(ctx))
		assert.NoError(t, ctx.Err(), "the child's bound, not the watchdog, must stop the read")
		cancel()
		require.True(t, held, "attempt %d never ran a real locked push", attempt)
		select {
		case <-accepted:
		default:
			t.Fatal("the hub never accepted a TCP connection")
		}
		ok, class := waitAddressedPushRecord(t)
		assert.False(t, ok)
		assert.Equal(t, "timeout", class)
		lock, err := pushlock.Acquire(app.mtixDir)
		require.NoError(t, err, "timeout stranded the native push lock")
		require.NoError(t, lock.Release())
	}
}

// childTimeoutHub accepts connections but never reads or answers PostgreSQL.
// Cleanup joins the accept loop before closing the connections it collected.
func childTimeoutHub(t *testing.T) (string, <-chan struct{}) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	accepted := make(chan struct{}, 32)
	done := make(chan struct{})
	var conns []net.Conn
	go func() {
		defer close(done)
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			conns = append(conns, conn)
			select {
			case accepted <- struct{}{}:
			default:
			}
		}
	}()
	t.Cleanup(func() {
		require.NoError(t, ln.Close())
		<-done
		for _, conn := range conns {
			require.NoError(t, conn.Close())
		}
	})
	return fmt.Sprintf("postgres://u:p@%s/db?sslmode=disable", ln.Addr().String()), accepted
}

func TestAddressedSendChild_RespectsParentContext(t *testing.T) {
	tests := []struct {
		name      string
		cancelled bool
	}{
		{"earlier deadline", false},
		{"cancelled caller", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(transport.EnvDSN, fakeEnrolledDSN)
			initTestApp(t)
			_ = seedOneNode(t)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if tt.cancelled {
				cancel()
			}
			o := testRun(transport.Options{})
			o.Push = func(got context.Context, _ io.Writer, _ transport.Options) error {
				deadline, _ := got.Deadline()
				parentDeadline, _ := ctx.Deadline()
				assert.Equal(t, parentDeadline, deadline)
				return nil
			}
			err := runAddressedSendPush(ctx, io.Discard, o)
			if tt.cancelled {
				assert.ErrorIs(t, err, context.Canceled)
				src, readErr := newHubContactSource(app.mtixDir)
				require.NoError(t, readErr)
				_, present, readErr := src.state.ReadAddressedPush(src.key())
				require.NoError(t, readErr)
				assert.False(t, present, "shutdown must not record a failed send")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestAddressedSendChild_ManualPushDoesNotUseAddressedBound(t *testing.T) {
	dsn, accepted := childTimeoutHub(t)
	t.Setenv(transport.EnvDSN, dsn)
	initTestApp(t)
	_ = seedOneNode(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	timer := time.AfterFunc(500*time.Millisecond, cancel)
	defer timer.Stop()
	cmd := newSyncPushCmdWithTimeout(10 * time.Millisecond)
	cmd.SetArgs([]string{"--insecure-tls"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	require.Error(t, cmd.ExecuteContext(ctx))
	assert.ErrorIs(t, ctx.Err(), context.Canceled, "manual push must run until caller cancellation")
	select {
	case <-accepted:
	default:
		t.Fatal("manual push never reached the real hub")
	}
	src, err := newHubContactSource(app.mtixDir)
	require.NoError(t, err)
	_, present, err := src.state.ReadAddressedPush(src.key())
	require.NoError(t, err)
	assert.False(t, present, "manual push must not record an addressed-send failure")
	lock, err := pushlock.Acquire(app.mtixDir)
	require.NoError(t, err)
	require.NoError(t, lock.Release())
}
