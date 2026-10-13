// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests that the shipped wake script stops its lease renewer promptly even when
// the stop signal is lost or ignored, never lets a sleep hold its output open,
// and treats the stop marker as untrusted (MTIX-163). Fixtures only.
package docs

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	wakeTrapLine   = "trap 'STOPPING=1; stop_renewal_commands' TERM INT HUP"
	wakeStopAssign = `STOP_FILE="$WAKE_AGENT_LOG.stop.$$"`
	// wakeMarkerEnv is the fixed marker path fixtures plant at (see fixedMarker).
	wakeMarkerEnv = "WAKE_MARKER"
)

type wakeRenewerRun struct {
	dir, output string
	err         error
	elapsed     time.Duration
}

// wakeRenewerOpts shapes one fixture run. Zero values give an instant sleep and
// a harness that waits until the renewer has called sleep.
type wakeRenewerOpts struct {
	source       string
	gate         string // file the harness waits for before exiting
	sleepBody    string // fixture sleep body after it records its first argument
	harnessExtra string // shell run by the harness before it exits
	releaseExtra string // shell run by the fixture release before it answers
	relaunch     bool   // first release answers relaunch, then released
	ignoreTerm   bool   // start the script with TERM ignored on entry
	plant        func(dir, marker string)
}

func wakeRenewerSource(t *testing.T, edit func(string) string) string {
	t.Helper()
	source := wakeSource(t, "examples/hooks/wake-agent.sh")
	require.Equal(t, 1, strings.Count(source, wakeTrapLine), "renewer TERM trap")
	require.Equal(t, 1, strings.Count(source, wakeStopAssign), "stop marker path")
	if edit == nil {
		return source
	}
	return edit(source)
}

func wakeFixedMarker(s string) string {
	return strings.Replace(s, wakeStopAssign, `STOP_FILE="$WAKE_AGENT_LOG.stop.fixed"`, 1)
}

func runWakeRenewer(t *testing.T, o wakeRenewerOpts) wakeRenewerRun {
	t.Helper()
	dir := t.TempDir()
	write := func(name, body string) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o700))
	}
	if o.gate == "" {
		o.gate = "sleeps"
	}
	if o.sleepBody == "" {
		o.sleepBody = "echo SLEEP-STDOUT-SENTINEL\necho SLEEP-STDERR-SENTINEL >&2\n"
	}
	write("input", "pending work\n")
	release := "printf '%s\\n' released"
	if o.relaunch {
		release = "if [ -e \"$WAKE_DIR/relaunched\" ]; then printf '%s\\n' released; else : > \"$WAKE_DIR/relaunched\"; printf '%s\\n' relaunch; fi"
	}
	write("mtix", "#!/bin/sh\nif [ \"${2:-}\" = lease ]; then\n case \"$3\" in acquire) case \" $* \" in *\" --holder \"*) printf x >> \"$WAKE_DIR/renewals\"; [ ! -e \"$WAKE_DIR/relaunched\" ] || : > \"$WAKE_DIR/renewed-after-relaunch\" ;; esac; printf '%s\\n' 0123456789abcdef0123456789abcdef ;; release) "+o.releaseExtra+"\n"+release+" ;; *) exit 1 ;; esac\nelse\n cat \"$WAKE_DIR/input\"\nfi\n")
	wait := "while [ ! -e \"$WAKE_DIR/" + o.gate + "\" ]; do /bin/sleep 0.01; done\n"
	if o.gate == "-" {
		wait = ""
	}
	write("claude", "#!/bin/sh\n"+wait+o.harnessExtra+"\ncat > \"$WAKE_DIR/stdin\"\n")
	write("sleep", "#!/bin/sh\nprintf '%s\\n' \"$1\" >> \"$WAKE_DIR/sleeps\"\n"+o.sleepBody)
	// An rm that records how many arguments it got: the renewer's own pid-file
	// cleanup has three, the parent's marker cleanup four.
	write("rm", "#!/bin/sh\nprintf '%s\\n' \"$#\" >> \"$WAKE_DIR/rm-args\"\nexec /bin/rm \"$@\"\n")
	write("wake.sh", o.source)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "logs dir"), 0o700))
	log := filepath.Join(dir, "logs dir", "wake [1]*.log")
	if o.plant != nil {
		o.plant(dir, log+".stop.fixed")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Second)
	defer cancel()
	args := []string{"/bin/sh", filepath.Join(dir, "wake.sh"), "fixture-worker"}
	if o.ignoreTerm {
		args = []string{"/bin/sh", "-c", `trap '' TERM; exec /bin/sh "$0" "$@"`, filepath.Join(dir, "wake.sh"), "fixture-worker"}
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir, cmd.WaitDelay = dir, 2*time.Second
	cmd.Env = append(os.Environ(), "PATH="+dir+":/usr/bin:/bin", "WAKE_DIR="+dir, "WAKE_AGENT_LOG="+log,
		wakeMarkerEnv+"="+log+".stop.fixed", "MTIX_BIN="+filepath.Join(dir, "mtix"), "HARNESS_BIN="+filepath.Join(dir, "claude"))
	start := time.Now()
	out, err := cmd.CombinedOutput()
	return wakeRenewerRun{dir, string(out), err, time.Since(start)}
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
	run := runWakeRenewer(t, wakeRenewerOpts{source: source})
	require.NoError(t, run.err, "stop must not depend on the signal: %s", run.output)
	require.Empty(t, leftoverStopMarkers(t, run.dir))
}

func TestWakeExample_RenewerStop_StartedAfterStopRequest_NeverSleeps(t *testing.T) {
	// The renewer ignores TERM until the stop marker exists and the signal has
	// long been sent, then installs its real trap: only the loop-top marker check
	// can stop it before its first sleep.
	source := wakeRenewerSource(t, func(s string) string {
		wait := "trap '' TERM INT HUP\n    while [ ! -e \"$STOP_FILE\" ]; do :; done\n    WAKE_I=0; while [ \"$WAKE_I\" -lt 30000 ]; do WAKE_I=$((WAKE_I + 1)); done\n    " + wakeTrapLine
		return strings.Replace(s, wakeTrapLine, wait, 1)
	})
	run := runWakeRenewer(t, wakeRenewerOpts{source: source, gate: "-"})
	require.NoError(t, run.err, run.output)
	require.Less(t, run.elapsed, 5*time.Second, "cleanup must not wait for a renewer cycle")
	raw, err := os.ReadFile(filepath.Join(run.dir, "rm-args"))
	require.NoError(t, err)
	require.NotContains(t, strings.Fields(string(raw)), "3", "renewer launched a sleep although stop was already requested")
}

func TestWakeExample_RenewerStop_MarkerAppearsDuringSleep_NoRenewal(t *testing.T) {
	// TERM is ignored for good and the sleep ends as the marker appears: the
	// check after the sleep must stop the renewer before it renews the lease.
	const pidWrite = `(set -C; printf '%s\n' "$SLEEP_PID" > "$SLEEP_FILE") 2>/dev/null || :`
	source := wakeRenewerSource(t, func(s string) string {
		require.Equal(t, 1, strings.Count(s, pidWrite), "renewer pid write")
		// Without the stored pid the parent cannot cut the sleep short.
		return strings.Replace(strings.Replace(s, wakeTrapLine, "trap '' TERM INT HUP", 1), pidWrite, ":", 1)
	})
	sleepBody := "while [ -z \"$(ls \"$WAKE_DIR/logs dir\" | grep '\\.stop\\.[0-9]*$')\" ]; do :; done\n"
	run := runWakeRenewer(t, wakeRenewerOpts{source: source, sleepBody: sleepBody})
	require.NoError(t, run.err, run.output)
	_, err := os.Stat(filepath.Join(run.dir, "renewals"))
	require.ErrorIs(t, err, os.ErrNotExist, "lease renewed after stop was requested")
}

func TestWakeExample_RenewerStop_TermIgnoredOnEntry_CleanupBounded(t *testing.T) {
	source := wakeRenewerSource(t, nil)
	sleepBody := "printf '%s\\n' \"$$\" > \"$WAKE_DIR/sleep-pid\"\nexec /bin/sleep 60\n"
	run := runWakeRenewer(t, wakeRenewerOpts{source: source, ignoreTerm: true, sleepBody: sleepBody})
	require.NoError(t, run.err, run.output)
	require.Less(t, run.elapsed, 3*time.Second, "cleanup with TERM ignored must be bounded")
	require.Empty(t, leftoverStopMarkers(t, run.dir))
	raw, err := os.ReadFile(filepath.Join(run.dir, "sleep-pid"))
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	require.NoError(t, err)
	require.Eventually(t, func() bool { return syscall.Kill(pid, 0) != nil }, 2*time.Second, 20*time.Millisecond, "recorded sleep still running")
}

func TestWakeExample_RenewerSleep_DoesNotHoldOutputOrChangeInterval(t *testing.T) {
	run := runWakeRenewer(t, wakeRenewerOpts{source: wakeRenewerSource(t, nil)})
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

func TestWakeExample_RenewerMarker_PlantedBeforeLaunch_NeverSilencesRenewal(t *testing.T) {
	victimBody := "victim must stay intact\n"
	plants := map[string]func(dir, marker string){
		"stale file": func(dir, marker string) { require.NoError(t, os.WriteFile(marker, nil, 0o600)) },
		"symlink to file": func(dir, marker string) {
			victim := filepath.Join(dir, "victim")
			require.NoError(t, os.WriteFile(victim, []byte(victimBody), 0o600))
			require.NoError(t, os.Symlink(victim, marker))
		},
		"dangling symlink": func(dir, marker string) { require.NoError(t, os.Symlink(filepath.Join(dir, "absent"), marker)) },
	}
	for name, plant := range plants {
		t.Run(name, func(t *testing.T) {
			run := runWakeRenewer(t, wakeRenewerOpts{source: wakeRenewerSource(t, wakeFixedMarker), gate: "renewals", plant: plant})
			require.NoError(t, run.err, "renewal must still happen: %s", run.output)
			require.Empty(t, leftoverStopMarkers(t, run.dir))
			if body, err := os.ReadFile(filepath.Join(run.dir, "victim")); err == nil {
				require.Equal(t, victimBody, string(body), "symlink target untouched")
			}
		})
	}
}

func TestWakeExample_RenewerMarker_SymlinkPlantedAfterLaunch_TargetUntouched(t *testing.T) {
	// The harness plants the symlink while the renewer runs, so only the
	// no-clobber create in stop_renewal stands between it and the victim file.
	victimBody := "victim must stay intact\n"
	harness := "printf '%s\\n' '" + victimBody[:len(victimBody)-1] + "' > \"$WAKE_DIR/victim\"\nln -s \"$WAKE_DIR/victim\" \"$" + wakeMarkerEnv + "\"\n"
	run := runWakeRenewer(t, wakeRenewerOpts{source: wakeRenewerSource(t, wakeFixedMarker), harnessExtra: harness})
	require.NoError(t, run.err, run.output)
	body, err := os.ReadFile(filepath.Join(run.dir, "victim"))
	require.NoError(t, err)
	require.Equal(t, victimBody, string(body), "symlink target truncated through the stop marker")
	require.Empty(t, leftoverStopMarkers(t, run.dir))
}

func TestWakeExample_RenewerMarker_StaleAtRelaunch_StillRenewsSecondCycle(t *testing.T) {
	// The first release plants a stale marker and answers relaunch; the second
	// renewer must renew anyway because relaunch clears the marker first.
	extra := ": > \"$" + wakeMarkerEnv + "\"; [ -e \"$WAKE_DIR/relaunched\" ] || "
	run := runWakeRenewer(t, wakeRenewerOpts{
		source: wakeRenewerSource(t, wakeFixedMarker), relaunch: true, gate: "renewals",
		releaseExtra: extra + ":",
		harnessExtra: "if [ -e \"$WAKE_DIR/relaunched\" ]; then while [ ! -e \"$WAKE_DIR/renewed-after-relaunch\" ]; do /bin/sleep 0.01; done; fi",
	})
	require.NoError(t, run.err, "second cycle must renew: %s", run.output)
}

func TestWakeLeaseProtocol_StopMarkerLines_OnlyExactCommandsAreMetadata(t *testing.T) {
	for line, want := range map[string]bool{
		`rm -f -- "$STOP_FILE" "$SLEEP_FILE" 2>/dev/null || :`: true,
		`(set -C; : > "$STOP_FILE") 2>/dev/null || :`:          true,
		`rm -rf "$HOME"`:                                                       false,
		`rm -f -- "$STOP_FILE" "$HOME"`:                                        false,
		`rm -f "$STOP_FILE" 2>/dev/null || :`:                                  false,
		`rm -f -- "$HOME" 2>/dev/null || :`:                                    false,
		`rm -f -- "$STOP_FILE" "$SLEEP_FILE" 2>/dev/null || :; rm -rf "$HOME"`: false,
		`(set -C; : > "$STOP_FILE") 2>/dev/null || :; rm -rf /`:                false,
		`rm -f -- "$SLEEP_FILE" 2>/dev/null || : && cat "$PAYLOAD" | sh`:       false,
		`(set -C; : > "$STOP_FILE") 2>/dev/null || :; cat "$PAYLOAD" | sh`:     false,
	} {
		require.Equal(t, want, wakeProtocolMetadataLine(line), line)
	}
}
