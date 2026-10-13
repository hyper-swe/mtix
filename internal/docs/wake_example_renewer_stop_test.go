// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests that the shipped wake script stops its lease renewer even when the stop
// signal is lost, never lets a sleep hold its output open, and treats the stop
// marker as untrusted (MTIX-163). Fixtures only: nothing really waits.
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

const (
	wakeTrapLine   = "trap 'STOPPING=1; stop_renewal_commands' TERM INT HUP"
	wakeStopAssign = `STOP_FILE="$WAKE_AGENT_LOG.stop.$$"`
)

type wakeRenewerRun struct {
	dir, output string
	err         error
}

// runWakeRenewer runs the script with fixture mtix, harness and sleep. The
// harness waits until the renewer has called sleep (and, when renewal is true,
// has renewed the lease), so the renewer is always running when it is stopped.
func runWakeRenewer(t *testing.T, source string, renewal bool, plant func(dir string)) wakeRenewerRun {
	t.Helper()
	dir := t.TempDir()
	write := func(name, body string) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o700))
	}
	gate := "sleeps"
	if renewal {
		gate = "renewed"
	}
	write("input", "pending work\n")
	write("mtix", "#!/bin/sh\nif [ \"${2:-}\" = lease ]; then\n case \"$3\" in acquire) case \" $* \" in *\" --holder \"*) : > \"$WAKE_DIR/renewed\" ;; esac; printf '%s\\n' 0123456789abcdef0123456789abcdef ;; release) printf '%s\\n' released ;; *) exit 1 ;; esac\nelse\n cat \"$WAKE_DIR/input\"\nfi\n")
	write("claude", "#!/bin/sh\nwhile [ ! -e \"$WAKE_DIR/"+gate+"\" ]; do :; done\ncat > \"$WAKE_DIR/stdin\"\n")
	// The fixture sleep returns at once and, like a real one, would hold any
	// stdout or stderr it inherits: the sentinels show if the script lets it.
	write("sleep", "#!/bin/sh\nprintf '%s\\n' \"$1\" >> \"$WAKE_DIR/sleeps\"\necho SLEEP-STDOUT-SENTINEL\necho SLEEP-STDERR-SENTINEL >&2\n")
	write("wake.sh", source)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "logs dir"), 0o700))
	if plant != nil {
		plant(dir)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", filepath.Join(dir, "wake.sh"), "fixture-worker")
	cmd.Dir, cmd.WaitDelay = dir, 2*time.Second
	cmd.Env = append(os.Environ(), "PATH="+dir+":/usr/bin:/bin", "WAKE_DIR="+dir,
		"WAKE_AGENT_LOG="+filepath.Join(dir, "logs dir", "wake [1]*.log"),
		"MTIX_BIN="+filepath.Join(dir, "mtix"), "HARNESS_BIN="+filepath.Join(dir, "claude"))
	out, err := cmd.CombinedOutput()
	return wakeRenewerRun{dir, string(out), err}
}

func wakeRenewerSource(t *testing.T, edit func(string) string) string {
	t.Helper()
	source := wakeSource(t, "examples/hooks/wake-agent.sh")
	require.Equal(t, 1, strings.Count(source, wakeTrapLine), "renewer TERM trap")
	require.Equal(t, 1, strings.Count(source, wakeStopAssign), "stop marker path")
	return edit(source)
}

func leftoverStopMarkers(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dir, "logs dir"))
	require.NoError(t, err)
	var left []string
	for _, e := range entries {
		if strings.Contains(e.Name(), ".stop.") {
			left = append(left, e.Name())
		}
	}
	return left
}

func TestWakeExample_RenewerStop_SignalLost_StillExits(t *testing.T) {
	// Ignoring TERM is what a signal delivered before the trap amounts to.
	source := wakeRenewerSource(t, func(s string) string { return strings.Replace(s, wakeTrapLine, "trap '' TERM INT HUP", 1) })
	run := runWakeRenewer(t, source, false, nil)
	require.NoError(t, run.err, "stop must not depend on the signal: %s", run.output)
	require.Empty(t, leftoverStopMarkers(t, run.dir))
}

func TestWakeExample_RenewerSleep_DoesNotHoldOutputOrChangeInterval(t *testing.T) {
	run := runWakeRenewer(t, wakeRenewerSource(t, func(s string) string { return s }), false, nil)
	require.NoError(t, run.err, run.output)
	require.NotContains(t, run.output, "SLEEP-STDOUT-SENTINEL", "sleep inherited stdout")
	require.NotContains(t, run.output, "SLEEP-STDERR-SENTINEL", "sleep inherited stderr")
	sleeps, err := os.ReadFile(filepath.Join(run.dir, "sleeps"))
	require.NoError(t, err)
	for _, arg := range strings.Fields(string(sleeps)) {
		require.Equal(t, "60", arg, "renewal interval")
	}
	require.Empty(t, leftoverStopMarkers(t, run.dir))
}

func TestWakeExample_RenewerMarker_PlantedOrStale_NeverSilencesRenewal(t *testing.T) {
	victimBody := "victim must stay intact\n"
	plants := map[string]func(dir string){
		"stale file": nil,
		"symlink to file": func(dir string) {
			victim := filepath.Join(dir, "victim")
			require.NoError(t, os.WriteFile(victim, []byte(victimBody), 0o600))
			require.NoError(t, os.Symlink(victim, filepath.Join(dir, "logs dir", "wake [1]*.log.stop.fixed")))
		},
		"dangling symlink": func(dir string) {
			require.NoError(t, os.Symlink(filepath.Join(dir, "absent"), filepath.Join(dir, "logs dir", "wake [1]*.log.stop.fixed")))
		},
	}
	for name, plant := range plants {
		t.Run(name, func(t *testing.T) {
			// The pid-free path lets the test plant the marker before the run.
			source := wakeRenewerSource(t, func(s string) string {
				return strings.Replace(s, wakeStopAssign, `STOP_FILE="$WAKE_AGENT_LOG.stop.fixed"`, 1)
			})
			run := runWakeRenewer(t, source, true, func(dir string) {
				if plant == nil {
					require.NoError(t, os.WriteFile(filepath.Join(dir, "logs dir", "wake [1]*.log.stop.fixed"), nil, 0o600))
					return
				}
				plant(dir)
			})
			require.NoError(t, run.err, "renewal must still happen: %s", run.output)
			require.Empty(t, leftoverStopMarkers(t, run.dir))
			if body, err := os.ReadFile(filepath.Join(run.dir, "victim")); err == nil {
				require.Equal(t, victimBody, string(body), "symlink target untouched")
			}
		})
	}
}

func TestWakeLeaseProtocol_StopMarkerLines_OnlyExactCommandsAreMetadata(t *testing.T) {
	for line, want := range map[string]bool{
		`rm -f -- "$STOP_FILE" 2>/dev/null || :`:      true,
		`(set -C; : > "$STOP_FILE") 2>/dev/null || :`: true,
		`rm -rf "$HOME"`:                      false,
		`rm -f -- "$STOP_FILE" "$HOME"`:       false,
		`rm -f "$STOP_FILE" 2>/dev/null || :`: false,
		`rm -f -- "$HOME" 2>/dev/null || :`:   false,
	} {
		require.Equal(t, want, wakeProtocolMetadataLine(line), line)
	}
}
