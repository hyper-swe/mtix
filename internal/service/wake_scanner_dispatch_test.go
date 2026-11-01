// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/hooks"
	"github.com/hyper-swe/mtix/internal/service"
)

type countingScan struct {
	calls atomic.Int32
	err   error
}

func (c *countingScan) Scan(context.Context) error {
	c.calls.Add(1)
	return c.err
}

// The wake pass is not tied to the hook ledger: it runs even when no hook is configured
// and even when the host's exec-dispatch policy defers the hook pass to the daemon.
func TestHooksDispatch_WakePass_RunsUnderDaemonExecPolicy(t *testing.T) {
	f := newScanFixture(t)
	dir := t.TempDir()
	require.NoError(t, hooks.SaveExecDispatchMode(dir, hooks.ExecDispatchDaemon))
	scan := &countingScan{}
	d := service.NewHooksDispatcher(f.store, dir, slog.Default()).WithWakeScanner(scan)

	d.Dispatch(t.Context())

	require.EqualValues(t, 1, scan.calls.Load(), "a non-daemon trigger still runs the wake pass")
}

func TestHooksDispatch_WakePassError_IsLoggedNotPropagated(t *testing.T) {
	f := newScanFixture(t)
	scan := &countingScan{err: errors.New("boom")}
	d := service.NewHooksDispatcher(f.store, t.TempDir(), slog.Default()).WithWakeScanner(scan)
	require.NotPanics(t, func() { d.Dispatch(t.Context()) })
	require.EqualValues(t, 1, scan.calls.Load())
}

func TestHooksDispatch_NoWakeScanner_DispatchUnchanged(t *testing.T) {
	f := newScanFixture(t)
	d := service.NewHooksDispatcher(f.store, t.TempDir(), slog.Default())
	require.NotPanics(t, func() { d.Dispatch(t.Context()) })
	require.Nil(t, (*service.HooksDispatcher)(nil).WithWakeScanner(&countingScan{}))
}

// A commit on the board triggers the post-command callback, which wakes the recipient once.
func TestHooksDispatch_OnCommit_WakesRecipientOnce(t *testing.T) {
	f := newScanFixture(t)
	seq := f.addEvent("sender")
	d := service.NewHooksDispatcher(f.store, t.TempDir(), slog.Default()).WithWakeScanner(f.scanner(always))
	trigger := d.OnCommitDispatch()

	trigger()
	trigger()

	require.Equal(t, []int64{seq}, f.adapter.seqs())
	require.Equal(t, 1, f.ledgerRows())
}
