// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Hook policy fixtures account for each real exec delivery and coordinate every
// observed child through readiness, release and completion events.
package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/hooks"
	"github.com/hyper-swe/mtix/internal/service"
)

// execHookBarrier observes deliveries synchronously and all connected children.
// Deadlines detect hangs; no quiet window or latency bound decides correctness.
type execHookBarrier struct {
	dir, record  string
	listener     *net.TCPListener
	returned     chan struct{}
	ready        chan struct{}
	release      chan struct{}
	acceptDone   chan struct{}
	childrenDone chan struct{}
	stopOnce     sync.Once
	children     sync.WaitGroup
	deliveries   atomic.Int64
	completed    atomic.Int64
	mu           sync.Mutex
	errors       []error
	connections  []net.Conn
}

func newExecHookBarrier(t *testing.T) *execHookBarrier {
	t.Helper()
	addr, err := net.ResolveTCPAddr("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	listener, err := net.ListenTCP("tcp4", addr)
	require.NoError(t, err)
	g := &execHookBarrier{dir: t.TempDir(), listener: listener, ready: make(chan struct{}),
		release: make(chan struct{}), acceptDone: make(chan struct{}), childrenDone: make(chan struct{})}
	g.record = filepath.Join(g.dir, "completed")
	t.Cleanup(func() { g.close(t) })
	go g.acceptChildren()
	go func() {
		<-g.acceptDone // No Add can race with Wait after the accept loop ends.
		g.children.Wait()
		close(g.childrenDone)
	}()
	binary, err := os.Executable()
	require.NoError(t, err)
	command, err := json.Marshal([]string{binary, "-test.run=^TestExecHookBarrierHelper$",
		"--", "exec-hook-barrier", listener.Addr().String(), g.record})
	require.NoError(t, err)
	writeHooks(t, g.dir, `
hooks:
  - name: wake-worker
    match: { events: [status.changed], status-to: [done] }
    deliver: [exec]
    exec: { command: `+string(command)+`, timeout-seconds: 180 }
`)
	require.NoError(t, hooks.SaveTrust(g.dir, hooks.ConfigHash(g.dir)))
	return g
}

func (g *execHookBarrier) observeDispatch(t *testing.T, d *service.HooksDispatcher) {
	t.Helper()
	require.NoError(t, service.WrapExecAdapterForTest(d, func(delegate hooks.Adapter) hooks.Adapter {
		return &observedExecAdapter{delegate: delegate, gate: g}
	}))
}

type observedExecAdapter struct {
	delegate hooks.Adapter
	gate     *execHookBarrier
}

func (a *observedExecAdapter) Name() string { return a.delegate.Name() }

func (a *observedExecAdapter) Deliver(ctx context.Context, delivery hooks.Delivery) error {
	a.gate.deliveries.Add(1) // Count before delegation: duplicate calls cannot hide behind startup scheduling.
	if err := a.delegate.Deliver(ctx, delivery); err != nil {
		return fmt.Errorf("observed exec delivery: %w", err)
	}
	guard := time.NewTimer(120 * time.Second)
	defer guard.Stop()
	select {
	case <-a.gate.ready:
		return nil // Wait for startup only, never the child's completion.
	case <-a.gate.release:
		return fmt.Errorf("hook fixture closed before readiness")
	case <-guard.C:
		return fmt.Errorf("hook fixture child did not report readiness")
	}
}

func (g *execHookBarrier) acceptChildren() {
	defer close(g.acceptDone)
	for {
		conn, err := g.listener.Accept()
		if errors.Is(err, net.ErrClosed) {
			return
		}
		if err != nil {
			g.recordError(fmt.Errorf("accept hook child: %w", err))
			return
		}
		g.mu.Lock()
		g.connections = append(g.connections, conn)
		g.mu.Unlock()
		g.children.Add(1)
		go func() {
			defer g.children.Done()
			if err := g.handleChild(conn); err != nil {
				g.recordError(err)
			}
		}()
	}
}

func (g *execHookBarrier) handleChild(conn net.Conn) (err error) {
	defer func() {
		if closeErr := conn.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close hook child: %w", closeErr))
		}
	}()
	if err := conn.SetDeadline(time.Now().Add(120 * time.Second)); err != nil {
		return fmt.Errorf("hook child deadline: %w", err)
	}
	if err := readHookSignal(conn, 'R'); err != nil {
		return err
	}
	select {
	case g.ready <- struct{}{}:
	case <-g.release: // Cleanup also releases extra invocations with no readiness consumer.
	}
	<-g.release
	if _, err := conn.Write([]byte{'G'}); err != nil {
		return fmt.Errorf("release hook child: %w", err)
	}
	if err := readHookSignal(conn, 'D'); err != nil {
		return err
	}
	g.completed.Add(1)
	return nil
}

func readHookSignal(conn net.Conn, want byte) error {
	var signal [1]byte
	if _, err := io.ReadFull(conn, signal[:]); err != nil {
		return fmt.Errorf("read hook signal %c: %w", want, err)
	}
	if signal[0] != want {
		return fmt.Errorf("hook signal %c, want %c", signal[0], want)
	}
	return nil
}

func (g *execHookBarrier) recordError(err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.errors = append(g.errors, err)
}

func (g *execHookBarrier) dispatchBeforeRelease(t *testing.T, dispatch func()) {
	t.Helper()
	g.returned = make(chan struct{})
	// The child's watchdog exceeds this async-order guard, so a synchronous
	// dispatcher fails here before the hook can time out by itself.
	guard := time.NewTimer(60 * time.Second)
	defer guard.Stop()
	go func() { dispatch(); close(g.returned) }()
	select {
	case <-g.returned:
	case <-guard.C:
		t.Fatal("dispatch did not return before releasing the parked hook (async contract)")
	}
	require.Equal(t, int64(1), g.deliveries.Load(),
		"each matching journal event invokes the exec adapter exactly once")
}

func (g *execHookBarrier) stop() {
	g.stopOnce.Do(func() {
		close(g.release) // Release every observed child, even on assertion failure.
		if err := g.listener.Close(); err != nil {
			g.recordError(fmt.Errorf("close hook listener: %w", err))
		}
	})
}

func (g *execHookBarrier) complete(t *testing.T) {
	t.Helper()
	g.stop()
	select {
	case <-g.childrenDone:
	case <-time.After(60 * time.Second):
		t.Fatal("hook children did not complete after release")
	}
	g.mu.Lock()
	err := errors.Join(g.errors...)
	g.mu.Unlock()
	require.NoError(t, err)
	require.Equal(t, g.deliveries.Load(), g.completed.Load(), "every observed invocation completed")
}

func (g *execHookBarrier) close(t *testing.T) {
	t.Helper()
	g.stop()
	select {
	case <-g.childrenDone:
	case <-time.After(60 * time.Second):
		t.Error("hook children did not stop after the barrier was released")
		g.closeConnections()
		select {
		case <-g.childrenDone:
		case <-time.After(60 * time.Second):
			t.Error("hook handlers did not stop after their connections were closed")
		}
	}
	if g.returned != nil {
		select {
		case <-g.returned:
		case <-time.After(60 * time.Second):
			t.Error("dispatch did not stop after the barrier was released")
		}
	}
	t.Logf("hook fixture cleanup: deliveries=%d completed=%d", g.deliveries.Load(), g.completed.Load())
	g.mu.Lock()
	defer g.mu.Unlock()
	assert.NoError(t, errors.Join(g.errors...), "every observed child is released and joined")
}

// TestExecHookBarrierHelper is selected alone by the trusted hook fixture.
// Normal test runs never enter the hook body.
func TestExecHookBarrierHelper(t *testing.T) {
	if len(os.Args) < 4 || os.Args[len(os.Args)-3] != "exec-hook-barrier" {
		return
	}
	conn, err := net.DialTimeout("tcp4", os.Args[len(os.Args)-2], 60*time.Second)
	require.NoError(t, err)
	defer func() { require.NoError(t, conn.Close()) }()
	require.NoError(t, conn.SetDeadline(time.Now().Add(120*time.Second)))
	_, err = conn.Write([]byte{'R'})
	require.NoError(t, err)
	require.NoError(t, readHookSignal(conn, 'G'))
	f, err := os.OpenFile(os.Args[len(os.Args)-1], os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec // test-owned completion record
	require.NoError(t, err)
	_, writeErr := f.WriteString("fired\n")
	closeErr := f.Close()
	require.NoError(t, writeErr)
	require.NoError(t, closeErr)
	_, err = conn.Write([]byte{'D'})
	require.NoError(t, err)
}

// closeConnections is a final failure-path backstop for a child that fails to
// acknowledge release. Closing the socket wakes both its reader and handler.
func (g *execHookBarrier) closeConnections() {
	<-g.acceptDone // Seal the accepted set before closing its connections.
	g.mu.Lock()
	connections := append([]net.Conn(nil), g.connections...)
	g.mu.Unlock()
	for _, conn := range connections {
		if err := conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			g.recordError(fmt.Errorf("close parked hook connection: %w", err))
		}
	}
}
