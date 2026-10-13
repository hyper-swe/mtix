// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests that the shipped wake script stops its lease renewer even when the stop
// signal is lost (MTIX-163), using fixtures only: no real waits are involved.
package docs

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// wakeLostSignalVariant makes the renewer ignore TERM, which is what a signal
// delivered before the renewer installs its trap amounts to.
func wakeLostSignalVariant(t *testing.T, source string) string {
	t.Helper()
	const trapLine = "trap 'STOPPING=1; stop_renewal_commands' TERM INT HUP"
	require.Equal(t, 1, strings.Count(source, trapLine), "renewer TERM trap")
	return strings.Replace(source, trapLine, "trap '' TERM INT HUP", 1)
}

func TestWakeExample_RenewerStop_SignalLost_StillExits(t *testing.T) {
	source := wakeLostSignalVariant(t, wakeSource(t, "examples/hooks/wake-agent.sh"))
	dir := t.TempDir()
	write := func(name, body string) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o700))
	}
	write("input", "pending work\n")
	write("mtix", "#!/bin/sh\nif [ \"${2:-}\" = lease ]; then\n case \"$3\" in acquire) printf '%s\\n' 0123456789abcdef0123456789abcdef ;; release) printf '%s\\n' released ;; *) exit 1 ;; esac\nelse\n cat \"$WAKE_DIR/input\"\nfi\n")
	write("claude", "#!/bin/sh\ncat > \"$WAKE_DIR/stdin\"\n")
	// A fixture sleep that returns at once: the renewer never really waits.
	write("sleep", "#!/bin/sh\nprintf s >> \"$WAKE_DIR/sleeps\"\n")
	write("wake.sh", source)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", filepath.Join(dir, "wake.sh"), "fixture-worker")
	cmd.Dir = dir
	cmd.WaitDelay = 2 * time.Second
	cmd.Env = append(os.Environ(), "PATH="+dir+":/usr/bin:/bin", "WAKE_DIR="+dir,
		"WAKE_AGENT_LOG="+filepath.Join(dir, "logs", "wake.log"),
		"MTIX_BIN="+filepath.Join(dir, "mtix"), "HARNESS_BIN="+filepath.Join(dir, "claude"))
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "wake script must stop its renewer without the signal: %s", output)
	require.NoError(t, ctx.Err())
	left, err := filepath.Glob(filepath.Join(dir, "logs", "*.stop*"))
	require.NoError(t, err)
	require.Empty(t, left, "stop marker removed")
}
