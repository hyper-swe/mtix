// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Hook exec-policy tests verify asynchronous spawn ordering and host policy
// with a process barrier, rather than a mutation latency threshold.
package service_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/hooks"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// MTIX-56.9 (detached exec) + MTIX-56.10 (exec dispatch-host policy).

func recCount(path string) int {
	body, err := os.ReadFile(path) //nolint:gosec // test-owned temp path
	if err != nil {
		return 0
	}
	n := 0
	for _, b := range body {
		if b == '\n' {
			n++
		}
	}
	return n
}

// TestExecDispatch_DetachedSpawn_DoesNotBlockMutationPath: a slow exec hook
// must not stall Dispatch (the FR-19 async contract, MTIX-56.9). The ledger
// outcome is 'delivered' at spawn.
func TestExecDispatch_DetachedSpawn_DoesNotBlockMutationPath(t *testing.T) {
	svc, store, _ := newTestNodeService(t)
	ctx := context.Background()
	gate := newExecHookBarrier(t)
	makeDoneEvent(t, svc)
	dispatch := service.NewHooksDispatcher(store, gate.dir, slog.Default())
	gate.dispatchBeforeRelease(t, func() { dispatch.Dispatch(ctx) })

	// Spawn success is recorded while the hook is still parked. Completion
	// is impossible until this test releases the hook after Dispatch returns.
	require.Equal(t, 1, deliveredCount(t, store, "wake-worker"),
		"delivered == spawned, recorded before the hook completes")
	require.Zero(t, recCount(gate.record), "the parked hook has not completed")
	gate.complete(t)
	require.Equal(t, 1, recCount(gate.record), "the detached hook completes exactly once")
}

// TestExecDispatch_SpawnFailureIsError: a spawn that cannot start (missing
// script) records outcome=error — terminal, never auto-retried.
func TestExecDispatch_SpawnFailureIsError(t *testing.T) {
	svc, store, _ := newTestNodeService(t)
	ctx := context.Background()
	dir := t.TempDir()
	writeHooks(t, dir, `
hooks:
  - name: wake-worker
    match: { events: [status.changed], status-to: [done] }
    deliver: [exec]
    exec: { command: ["`+filepath.Join(dir, "does-not-exist.sh")+`"], timeout-seconds: 5 }
`)
	require.NoError(t, hooks.SaveTrust(dir, hooks.ConfigHash(dir)))

	makeDoneEvent(t, svc)
	d := service.NewHooksDispatcher(store, dir, slog.Default())
	d.Dispatch(ctx)
	d.Dispatch(ctx) // no retry of the terminal error

	errors := 0
	entries, err := store.ReadHookLog(ctx, 100)
	require.NoError(t, err)
	for _, e := range entries {
		if e.Hook == "wake-worker" && e.Outcome == "error" {
			errors++
		}
	}
	require.Equal(t, 1, errors, "spawn failure is terminal error, never auto-retried")
}

// TestExecPolicy_DaemonMode_NonDaemonTriggerDefersEntirely: under
// exec-dispatch=daemon a CLI/server dispatcher claims NOTHING and moves
// NOTHING — the daemon-marked dispatcher then fires (MTIX-56.10). This is what
// prevents a non-daemon trigger from consuming a wake the daemon should run.
func TestExecPolicy_DaemonMode_NonDaemonTriggerDefersEntirely(t *testing.T) {
	svc, store, _ := newTestNodeService(t)
	ctx := context.Background()
	gate := newExecHookBarrier(t)
	dir := gate.dir
	require.NoError(t, hooks.SaveExecDispatchMode(dir, hooks.ExecDispatchDaemon))

	makeDoneEvent(t, svc)

	// CLI-shaped trigger: full no-op — no ledger rows, floor unchanged.
	service.NewHooksDispatcher(store, dir, slog.Default()).Dispatch(ctx)
	floor, err := store.HookCursor(ctx)
	require.NoError(t, err)
	assert.Zero(t, floor, "a non-daemon trigger must not advance the shared floor under daemon policy")
	var rows int
	require.NoError(t, store.ReadDB().QueryRow(`SELECT COUNT(*) FROM hook_dispatch_ledger`).Scan(&rows))
	assert.Zero(t, rows, "…and must claim nothing the daemon should fire")

	// The daemon-marked dispatcher fires it.
	daemon := service.NewHooksDispatcher(store, dir, slog.Default())
	daemon.MarkDaemon()
	gate.dispatchBeforeRelease(t, func() { daemon.Dispatch(ctx) })
	daemon.Dispatch(ctx) // The same journal entry must not spawn another hook.
	require.Equal(t, 1, deliveredCount(t, store, "wake-worker"))
	require.Zero(t, recCount(gate.record), "the hook remains parked until released")
	gate.complete(t)
	require.Equal(t, 1, recCount(gate.record), "the daemon trigger fires the wake exactly once")
}

// TestExecPolicy_OffMode_SkipsExecKeepsOtherAdapters: exec-dispatch=off makes
// this host never launch anything; inbox delivery still happens; the outcome
// is the terminal skipped-policy.
func TestExecPolicy_OffMode_SkipsExecKeepsOtherAdapters(t *testing.T) {
	svc, store, _ := newTestNodeService(t)
	ctx := context.Background()
	dir := t.TempDir()
	rec := filepath.Join(dir, "rec")
	script := filepath.Join(dir, "wake.sh")
	require.NoError(t, os.WriteFile(script,
		[]byte("#!/bin/sh\nprintf 'fired\\n' >> \""+rec+"\"\n"), 0o700)) //nolint:gosec
	writeHooks(t, dir, `
hooks:
  - name: wake-worker
    match: { events: [status.changed], status-to: [done], to-agent: worker }
    deliver: [inbox, exec]
    exec: { command: ["`+script+`"], timeout-seconds: 5 }
`)
	require.NoError(t, hooks.SaveTrust(dir, hooks.ConfigHash(dir)))
	require.NoError(t, hooks.SaveExecDispatchMode(dir, hooks.ExecDispatchOff))

	makeDoneEvent(t, svc)
	d := service.NewHooksDispatcher(store, dir, slog.Default())
	d.Dispatch(ctx)
	d.Dispatch(ctx) // skipped-policy is terminal — no exec on later passes either

	inbox, err := store.InboxList(ctx, "worker")
	require.NoError(t, err)
	require.Len(t, inbox, 1, "inbox delivery is unaffected by the exec policy")
	assert.Zero(t, recCount(rec), "this host never launches anything under off")
	skipped := 0
	entries, err := store.ReadHookLog(ctx, 100)
	require.NoError(t, err)
	for _, e := range entries {
		if e.Hook == "wake-worker" && e.Outcome == sqlite.OutcomeSkippedPolicy {
			skipped++
		}
	}
	assert.Equal(t, 1, skipped, "exec skipped once with the terminal skipped-policy outcome")
}

// execHookBarrier coordinates an actual detached hook process over loopback.
// Its deadlines detect hangs; no elapsed-time threshold determines correctness.
type execHookBarrier struct {
	dir      string
	record   string
	listener *net.TCPListener
	conn     net.Conn
	returned chan struct{}
}

func newExecHookBarrier(t *testing.T) *execHookBarrier {
	t.Helper()
	addr, err := net.ResolveTCPAddr("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	listener, err := net.ListenTCP("tcp4", addr)
	require.NoError(t, err)
	gate := &execHookBarrier{dir: t.TempDir(), listener: listener}
	gate.record = filepath.Join(gate.dir, "completed")
	t.Cleanup(func() { gate.close(t) })
	binary, err := os.Executable()
	require.NoError(t, err)
	command, err := json.Marshal([]string{binary, "-test.run=^TestExecHookBarrierHelper$",
		"--", "exec-hook-barrier", listener.Addr().String(), gate.record})
	require.NoError(t, err)
	writeHooks(t, gate.dir, `
hooks:
  - name: wake-worker
    match: { events: [status.changed], status-to: [done] }
    deliver: [exec]
    exec: { command: `+string(command)+`, timeout-seconds: 180 }
`)
	require.NoError(t, hooks.SaveTrust(gate.dir, hooks.ConfigHash(gate.dir)))
	return gate
}

func (g *execHookBarrier) dispatchBeforeRelease(t *testing.T, dispatch func()) {
	t.Helper()
	g.returned = make(chan struct{})
	// The child has a longer watchdog than this async-order hang guard.
	// A synchronous dispatcher must fail here, before the child can time out.
	guard := time.NewTimer(60 * time.Second)
	defer guard.Stop()
	go func() {
		dispatch()
		close(g.returned)
	}()
	require.NoError(t, g.listener.SetDeadline(time.Now().Add(60*time.Second)))
	conn, err := g.listener.Accept()
	require.NoError(t, err, "the detached hook must start")
	g.conn = conn
	require.NoError(t, conn.SetDeadline(time.Now().Add(60*time.Second)))
	var ready [1]byte
	_, err = io.ReadFull(conn, ready[:])
	require.NoError(t, err, "the child signals it is parked before completion")
	require.Equal(t, byte('R'), ready[0])
	select {
	case <-g.returned:
	case <-guard.C:
		t.Fatal("dispatch did not return before releasing the parked hook (async contract)")
	}
}

func (g *execHookBarrier) complete(t *testing.T) {
	t.Helper()
	_, err := g.conn.Write([]byte{'G'})
	require.NoError(t, err, "release the hook only after dispatch returned")
	var done [1]byte
	_, err = io.ReadFull(g.conn, done[:])
	require.NoError(t, err, "the detached hook signals body completion")
	require.Equal(t, byte('D'), done[0])
}

func (g *execHookBarrier) close(t *testing.T) {
	t.Helper()
	// Closing the connection also releases a synchronous mutant on failure.
	if g.conn != nil {
		assert.NoError(t, g.conn.Close())
	}
	assert.NoError(t, g.listener.Close())
	if g.returned != nil {
		select {
		case <-g.returned:
		case <-time.After(60 * time.Second):
			t.Error("dispatch did not stop after the barrier was closed")
		}
	}
}

// TestExecHookBarrierHelper is the hook body, selected alone when the trusted
// fixture re-executes this test binary. Normal test runs never enter it.
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
	var release [1]byte
	_, err = io.ReadFull(conn, release[:])
	require.NoError(t, err)
	require.Equal(t, byte('G'), release[0])
	f, err := os.OpenFile(os.Args[len(os.Args)-1], os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec // test-owned completion record
	require.NoError(t, err)
	_, writeErr := f.WriteString("fired\n")
	closeErr := f.Close()
	require.NoError(t, writeErr)
	require.NoError(t, closeErr)
	_, err = conn.Write([]byte{'D'})
	require.NoError(t, err)
}
