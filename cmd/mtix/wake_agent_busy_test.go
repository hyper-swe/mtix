// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

// Busy-store fixtures exercise actual process diagnostics without acknowledging work.

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func holdWakeDatabase(t *testing.T, f wakeScriptFixture) func() {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(f.project, ".mtix", "data", "mtix.db"))
	require.NoError(t, err)
	conn, err := db.Conn(t.Context())
	require.NoError(t, err)
	_, err = conn.ExecContext(t.Context(), "BEGIN IMMEDIATE")
	require.NoError(t, err)
	released := false
	release := func() {
		if released {
			return
		}
		released = true
		_, err := conn.ExecContext(t.Context(), "ROLLBACK")
		require.NoError(t, err)
		require.NoError(t, conn.Close())
		require.NoError(t, db.Close())
	}
	t.Cleanup(release)
	return release
}

func TestWakeAgentScript_BusyStore_FailsLoudlyAndKeepsPending(t *testing.T) {
	f := newWakeScriptFixture(t)
	f.event(t, "Pending event")
	unlock := holdWakeDatabase(t, f)
	p := f.start(t)
	require.Error(t, p.wait(t), "wake: %s", p.output)
	assert.Equal(t, 1, p.cmd.ProcessState.ExitCode())
	assert.Contains(t, p.output.String(), "lease acquisition failed")
	log, err := os.ReadFile(filepath.Join(f.root, "logs", "wake.log"))
	require.NoError(t, err)
	assert.Regexp(t, `database is locked|SQLITE_BUSY|another mtix operation`, string(log))
	unlock()
	assert.Contains(t, string(f.cli(t, "inbox", "--agent", "TEST-agent", "--json")), "Pending event")
}

func TestWakeAgentScript_RenewalFails_LogsAndRechecksAfterRecovery(t *testing.T) {
	f := newWakeScriptFixture(t)
	f.event(t, "First event")
	text := "#!/bin/sh\n: > \"$WAKE_DIR/sleeping\"\nwhile [ ! -f \"$WAKE_DIR/renew-now\" ]; do /bin/sleep 0.01; done\nexec /bin/sleep 0.1\n"
	require.NoError(t, os.WriteFile(filepath.Join(f.root, "sleep"), []byte(text), 0700))
	p := f.start(t, "WAKE_HARNESS_GATE=1")
	waitWakeFile(t, filepath.Join(f.root, "started"))
	waitWakeFile(t, filepath.Join(f.root, "sleeping"))
	unlock := holdWakeDatabase(t, f)
	require.NoError(t, os.WriteFile(filepath.Join(f.root, "renew-now"), []byte("ready"), 0600))
	waitWakeFile(t, filepath.Join(f.root, "logs", "wake.log"))
	unlock()
	require.NoError(t, os.WriteFile(filepath.Join(f.root, "continue"), []byte("continue"), 0600))
	require.NoError(t, p.wait(t), "wake: %s", p.output)
	assert.Contains(t, p.output.String(), "lease renewal failed")
	assertWakeHandled(t, f, 1, 1)
	f.cli(t, "inbox", "lease", "acquire", "--agent", "TEST-agent")
}
