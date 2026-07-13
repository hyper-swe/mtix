// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// MTIX-56.2: `mtix daemon` — the host's first-class event dispatcher (FR-20).
// PG-free tests: construction, project guard, single-instance lock, and the
// hub-less local-tail dispatch mode. The cross-machine pull+dispatch chain is
// covered by the e2e suite (TestE2E_FR20_*).
package main

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func TestDaemonCmd_Construction(t *testing.T) {
	cmd := newDaemonCmd()
	require.Equal(t, "daemon [DSN]", cmd.Use)
	for _, name := range []string{"insecure-tls", "interval", "install"} {
		require.NotNilf(t, cmd.Flags().Lookup(name), "%s flag declared", name)
	}
	assert.Equal(t, strconv.Itoa(daemonDispatchDefaultIntervalSec),
		cmd.Flags().Lookup("interval").DefValue,
		"dispatch cadence defaults to the FR-20 §4.3 range, not the old 30s pull default")
}

func TestDaemonCmd_RegisteredOnRoot(t *testing.T) {
	root := newRootCmd()
	sub, _, err := root.Find([]string{"daemon"})
	require.NoError(t, err)
	require.Equal(t, "daemon [DSN]", sub.Use, "mtix daemon is a first-class root command, not a sync subcommand")
}

func TestRunDaemon_RefusesOutsideMtixProject(t *testing.T) {
	saved := app
	app = appContext{}
	t.Cleanup(func() { app = saved })

	var stdout, stderr bytes.Buffer
	err := runDaemon(context.Background(), &stdout, &stderr,
		nil, transport.Options{}, 5)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not in an mtix project")
}

func TestRunDaemon_SecondInstanceExitsCleanly(t *testing.T) {
	dir := t.TempDir()
	saved := app
	app = appContext{mtixDir: dir}
	t.Cleanup(func() { app = saved })

	// A live daemon (this test process) already owns the PID file — the same
	// lock `mtix sync daemon` uses, so the two commands also exclude each other.
	require.NoError(t, os.WriteFile(filepath.Join(dir, daemonPIDFilename),
		[]byte(strconv.Itoa(os.Getpid())), 0o600))

	// The store guard sits behind the PID check; give it a store so we get
	// past the nil check deterministically regardless of ordering.
	store := newDaemonTestStore(t, dir)
	app.store = store

	var stdout, stderr bytes.Buffer
	err := runDaemon(context.Background(), &stdout, &stderr,
		nil, transport.Options{}, 5)
	require.NoError(t, err, "a second instance is a clean no-op, not an error")
	require.Contains(t, stderr.String(), "already running")
}

// newDaemonTestStore opens a store rooted in dir (as .mtix) for daemon tests.
func newDaemonTestStore(t *testing.T, dir string) *sqlite.Store {
	t.Helper()
	s, err := sqlite.New(filepath.Join(dir, "mtix.db"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	return s
}

// TestRunDaemon_NoHub_LocalTailDispatch: with no hub configured the daemon
// still runs — it degrades to the local journal tail, so hooks fire for
// cross-process writes into the same .mtix (FR-20 §12 "no hub configured").
func TestRunDaemon_NoHub_LocalTailDispatch(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(transport.EnvDSN, "") // no hub via env; temp dir has no secrets file

	store := newDaemonTestStore(t, dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hooks.yaml"), []byte(`
hooks:
  - name: wake-worker
    match:
      events: [comment.addressed]
      to-agent: worker
    deliver: [inbox]
`), 0o600))

	saved := app
	app = appContext{
		mtixDir:   dir,
		store:     store,
		logger:    slog.Default(),
		hooksDisp: service.NewHooksDispatcher(store, dir, slog.Default()),
	}
	t.Cleanup(func() { app = saved })

	// A journaled event written by "another process" — no CLI command, no MCP
	// server, nothing dispatched it yet.
	_, err := store.WriteDB().Exec(`
		INSERT INTO sync_events
		  (event_id, project_prefix, node_id, op_type, payload,
		   wall_clock_ts, lamport_clock, vector_clock,
		   author_id, author_machine_hash, sync_status, created_at)
		VALUES ('evt-x', 'PROJ', 'PROJ-1', 'comment', '{"to":"worker","text":"go"}',
		        1, 1, '{}', 'poster', '0123456789abcdef',
		        'pending', '2026-07-12T00:00:00Z')`)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	var stdout, stderr bytes.Buffer
	require.NoError(t, runDaemon(ctx, &stdout, &stderr, nil, transport.Options{}, 3600))

	require.Contains(t, stdout.String(), "no hub configured",
		"the daemon says it is in local-tail mode")
	require.Contains(t, stdout.String(), "shutting down", "clean shutdown on ctx cancel")

	entries, err := store.ReadHookLog(context.Background(), 100)
	require.NoError(t, err)
	fired := 0
	for _, e := range entries {
		if e.Hook == "wake-worker" && e.Outcome == "delivered" {
			fired++
		}
	}
	require.Equal(t, 1, fired, "the cross-process event was dispatched by the daemon's local tail")
}

// TestRunDaemon_SurfacesFailClosedDSNError: a DSN error other than
// "not configured" (here: a secrets file with too-loose permissions) must
// abort loudly, not silently degrade to local-only.
func TestRunDaemon_SurfacesFailClosedDSNError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(transport.EnvDSN, "")
	// 0644 on .mtix/secrets violates the 0600 requirement → ErrSecretsFileMode.
	require.NoError(t, os.WriteFile(filepath.Join(dir, transport.SecretsFilename),
		[]byte("postgres://u:p@h/d"), 0o644))

	store := newDaemonTestStore(t, dir)
	saved := app
	app = appContext{mtixDir: dir, store: store, logger: slog.Default()}
	t.Cleanup(func() { app = saved })

	// Safety net: if the daemon wrongly enters its loop, the ctx unblocks it
	// and the test fails on the missing error instead of hanging.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var stdout, stderr bytes.Buffer
	err := runDaemon(ctx, &stdout, &stderr, nil, transport.Options{}, 5)
	require.Error(t, err, "a fail-closed DSN refusal aborts the daemon; only ErrDSNNotConfigured means local-only")
	require.ErrorIs(t, err, transport.ErrSecretsFileMode)
}

// TestSyncDaemonCmd_DeprecationNotice: the old spelling keeps working for one
// release but points at the successor.
func TestSyncDaemonCmd_DeprecationNotice(t *testing.T) {
	cmd := newSyncDaemonCmd()
	require.Contains(t, cmd.Long, "mtix daemon",
		"sync daemon help points to the first-class daemon (deprecated alias, FR-20 §13)")
}
