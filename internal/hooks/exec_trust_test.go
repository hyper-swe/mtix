// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hooks

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestExecAdapter_EventOnStdinAndEnv: the event reaches the command via stdin
// AND MTIX_EVENT, and never via the command line (MTIX-47.5 / FR-19.3).
func TestExecAdapter_EventOnStdinAndEnv(t *testing.T) {
	dir := t.TempDir()
	fromStdin := filepath.Join(dir, "stdin.txt")
	fromEnv := filepath.Join(dir, "env.txt")
	a := NewExecAdapter()
	d := Delivery{
		Hook: Hook{Name: "h", Exec: &ExecConfig{
			Command:        []string{"sh", "-c", "cat > " + fromStdin + `; printf %s "$MTIX_EVENT" > ` + fromEnv},
			TimeoutSeconds: 10,
		}},
		EventJSON: []byte(`{"event":"status.changed","node_id":"HP-1"}`),
	}
	require.NoError(t, a.Deliver(context.Background(), d))

	// MTIX-56.9: delivery is a detached spawn — the command completes after
	// Deliver returns, so observe its outputs eventually.
	want := `{"event":"status.changed","node_id":"HP-1"}`
	require.Eventually(t, func() bool {
		gotStdin, _ := os.ReadFile(fromStdin)
		gotEnv, _ := os.ReadFile(fromEnv)
		return string(gotStdin) == want && string(gotEnv) == want
	}, 10*time.Second, 20*time.Millisecond, "the detached command receives the event on stdin AND env")
}

func TestExecAdapter_FailureAndMissingCommand(t *testing.T) {
	a := NewExecAdapter()
	// MTIX-56.9: outcome is decided at SPAWN. A command that starts and exits
	// non-zero is a successful delivery (scripts self-report; inbox ack is the
	// fabric's success signal) — only a failed spawn is an error.
	require.NoError(t, a.Deliver(context.Background(), Delivery{Hook: Hook{Name: "h", Exec: &ExecConfig{Command: []string{"false"}}}}),
		"a spawned command's non-zero exit is the script's own concern")
	require.Error(t, a.Deliver(context.Background(), Delivery{Hook: Hook{Name: "h", Exec: &ExecConfig{Command: []string{"/nonexistent/definitely-missing"}}}}),
		"a spawn that cannot start is an error")
	require.Error(t, a.Deliver(context.Background(), Delivery{Hook: Hook{Name: "h"}}),
		"a hook with no command is an error")
}

// TestExecTrust_ContentHashPinning: trust binds to the bytes; any edit voids it.
func TestExecTrust_ContentHashPinning(t *testing.T) {
	dir := t.TempDir()
	require.False(t, ExecTrusted(dir), "no hooks.yaml is not trusted")

	write := func(s string) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "hooks.yaml"), []byte(s), 0o600))
	}
	write("hooks: []\n")
	require.False(t, ExecTrusted(dir), "present but not yet trusted")

	require.NoError(t, SaveTrust(dir, ConfigHash(dir)))
	require.True(t, ExecTrusted(dir), "trusted after pinning the current hash")

	write("hooks: []\n# a teammate edited this\n")
	require.False(t, ExecTrusted(dir), "any edit voids trust until re-trusted")

	require.NoError(t, SaveTrust(dir, ConfigHash(dir)))
	require.True(t, ExecTrusted(dir), "re-trust restores it")
}
