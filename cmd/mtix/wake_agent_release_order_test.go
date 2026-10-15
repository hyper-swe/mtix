// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const releaseOrderSleep = `#!/bin/sh
if [ -f "$WAKE_DIR/renew-done" ]; then exec /bin/sleep 60; fi
: > "$WAKE_DIR/sleeping"
while [ ! -f "$WAKE_DIR/renew-go" ]; do /bin/sleep 0.01; done
exit 0
`

// Both branches run the real CLI; markers establish ordering without elapsed-time assertions.
const releaseOrderCLI = `#!/bin/sh
set -eu
if [ "${1:-}" = inbox ] && [ "${2:-}" = lease ] && [ "${3:-}" = acquire ]; then
 case " $* " in
 *" --holder "*)
   trap ': > "$WAKE_DIR/quiesce-go"' TERM INT HUP
   : > "$WAKE_DIR/renew-started"
   while [ ! -f "$WAKE_DIR/release-done" ] && [ ! -f "$WAKE_DIR/quiesce-go" ]; do /bin/sleep 0.01; done
   "$REAL_MTIX" "$@" > "$WAKE_DIR/renew-token"
   : > "$WAKE_DIR/renew-done"
   cat "$WAKE_DIR/renew-token"
   exit 0 ;;
 esac
fi
if [ "${1:-}" = inbox ] && [ "${2:-}" = lease ] && [ "${3:-}" = release ]; then
 if [ ! -f "$WAKE_DIR/renew-done" ]; then : > "$WAKE_DIR/release-before-renew"; fi
 OUTCOME=$("$REAL_MTIX" "$@")
 if [ "$OUTCOME" = released ]; then
   : > "$WAKE_DIR/release-done"
   while [ ! -f "$WAKE_DIR/renew-done" ]; do /bin/sleep 0.01; done
 fi
 printf '%s\n' "$OUTCOME"
 exit 0
fi
exec "$REAL_MTIX" "$@"
`

func TestWakeAgentScript_FinalReleaseWaitsForRenewal(t *testing.T) {
	f := newWakeScriptFixture(t)
	f.event(t, "Controlled event")
	require.NoError(t, os.WriteFile(filepath.Join(f.root, "sleep"), []byte(releaseOrderSleep), 0700))
	wrapper := filepath.Join(f.root, "mtix-wrapper")
	require.NoError(t, os.WriteFile(wrapper, []byte(releaseOrderCLI), 0700))
	p := f.start(t, "MTIX_BIN="+wrapper, "REAL_MTIX="+f.binary, "WAKE_HARNESS_GATE=1")
	p.waitFile(t, "started")
	p.waitFile(t, "sleeping")
	require.NoError(t, os.WriteFile(filepath.Join(f.root, "renew-go"), []byte("ready"), 0600))
	p.waitFile(t, "renew-started")
	require.NoError(t, os.WriteFile(filepath.Join(f.root, "continue"), []byte("ready"), 0600))
	require.NoError(t, p.wait(t), "wake: %s", p.output)
	assertWakeHandled(t, f, 1, 1)
	cmd := exec.CommandContext(t.Context(), f.binary, "inbox", "lease", "acquire", "--agent", "TEST-agent")
	cmd.Dir, cmd.Env = f.project, f.env
	output, err := cmd.CombinedOutput()
	assert.NoError(t, err, "fresh acquisition after final exit: %s", output)
	assert.Equal(t, 0, cmd.ProcessState.ExitCode())
	_, markerErr := os.Stat(filepath.Join(f.root, "release-before-renew"))
	assert.ErrorIs(t, markerErr, os.ErrNotExist, "final release must follow completed renewal")
	releaseOrderCleanup(t, f, output, err)
	t.Logf("owned wake pid=%d fresh acquisition pid=%d exit=%d", p.cmd.Process.Pid, cmd.Process.Pid, cmd.ProcessState.ExitCode())
}

func releaseOrderCleanup(t *testing.T, f wakeScriptFixture, fresh []byte, acquireErr error) {
	t.Helper()
	token := fresh
	if acquireErr != nil {
		var err error
		token, err = os.ReadFile(filepath.Join(f.root, "renew-token"))
		require.NoError(t, err)
	}
	f.cli(t, "inbox", "lease", "release", "--agent", "TEST-agent", "--holder", strings.TrimSpace(string(token)), "--abandon")
}
