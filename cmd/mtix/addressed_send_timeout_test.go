// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// silentHub accepts TCP connections and never answers, like a hung hub. It
// returns the DSN that points at it.
func silentHub(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	var mu sync.Mutex
	var conns []net.Conn
	go func() {
		for {
			c, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
	})
	return fmt.Sprintf("postgres://u:p@%s/db?sslmode=disable", ln.Addr().String())
}

// A hub that accepts the connection and never answers ends the push at the
// deadline: the outcome is recorded with class timeout, not push_failed
// (ADR-007 D21, MTIX-112.20.2).
func TestRunAddressedSendPush_HungHub_RecordsTimeoutClass(t *testing.T) {
	t.Setenv(transport.EnvDSN, silentHub(t))
	initTestApp(t)
	_ = seedOneNode(t)
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	err := runAddressedSendPush(ctx, io.Discard, testRun(transport.Options{InsecureTLS: true}))
	require.Error(t, err)
	ok, class := waitAddressedPushRecord(t)
	assert.False(t, ok)
	assert.Equal(t, "timeout", class)
}

// The in-process job runs under the bound it is given, so a hung hub cannot
// hold the coalescing runner (and every later send in the process) forever.
func TestInProcessPushJob_RunsUnderDeadline(t *testing.T) {
	var got context.Context
	job := inProcessPushJob(time.Minute, func(ctx context.Context) { got = ctx })
	job()
	require.NotNil(t, got)
	deadline, ok := got.Deadline()
	require.True(t, ok, "the job context carries a deadline")
	assert.WithinDuration(t, time.Now().Add(time.Minute), deadline, 10*time.Second)
	assert.Error(t, got.Err(), "the job context is released when the job returns")
}

// The production bound is the documented five minutes.
func TestAddressedPushTimeout_IsFiveMinutes(t *testing.T) {
	assert.Equal(t, 5*time.Minute, addressedPushTimeout)
}

// Edge rows of the wait loop: a pending-read error is returned wrapped (never
// treated as nothing pending), and a context cancelled while a push is held
// stops the wait at once with push_held.
func TestPushWaitingForLock_EdgeRows(t *testing.T) {
	t.Run("pending read error is returned wrapped", func(t *testing.T) {
		t.Setenv(transport.EnvDSN, fakeEnrolledDSN)
		initTestApp(t)
		_ = seedOneNode(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		f := &fakePush{}
		o := testRun(transport.Options{})
		o.Push = f.run
		err := pushWaitingForLock(ctx, io.Discard, o)
		require.Error(t, err)
		assert.ErrorContains(t, err, "read pending events")
		assert.Zero(t, f.calls, "no push on an unreadable queue")
	})
	t.Run("cancelled context stops the held wait at once", func(t *testing.T) {
		t.Setenv(transport.EnvDSN, fakeEnrolledDSN)
		initTestApp(t)
		_ = seedOneNode(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		waits, calls := 0, 0
		o := testRun(transport.Options{})
		o.HeldTries = 50
		o.Wait = func(time.Duration) { waits++ }
		o.Push = func(c context.Context, _ io.Writer, _ transport.Options) error {
			calls++
			if probe, ok := c.Value(pushProbeKey{}).(*pushProbe); ok {
				probe.held.Store(true)
			}
			cancel() // shutdown arrives while the push is held
			return nil
		}
		err := pushWaitingForLock(ctx, io.Discard, o)
		require.ErrorIs(t, err, errPushHeld)
		assert.Equal(t, 1, calls)
		assert.Zero(t, waits, "no further wait after the cancel")
	})
}
