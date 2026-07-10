// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hooks

import (
	"context"
	"os"
	"path/filepath"
	"testing"

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

	gotStdin, _ := os.ReadFile(fromStdin)
	gotEnv, _ := os.ReadFile(fromEnv)
	require.Equal(t, `{"event":"status.changed","node_id":"HP-1"}`, string(gotStdin))
	require.Equal(t, `{"event":"status.changed","node_id":"HP-1"}`, string(gotEnv))
}

func TestExecAdapter_FailureAndMissingCommand(t *testing.T) {
	a := NewExecAdapter()
	require.Error(t, a.Deliver(context.Background(), Delivery{Hook: Hook{Name: "h", Exec: &ExecConfig{Command: []string{"false"}}}}),
		"non-zero exit surfaces an error for the dispatcher to log")
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
