//go:build !windows

// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests that the shipped wake script stops its lease renewer promptly even when
// the stop signal is lost or ignored, never lets a sleep hold its output open,
// and treats the stop marker as untrusted (MTIX-163). Fixtures only.
package docs

import (
	"bytes"
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

// wakePidRm starts the renewer's pid-file write, the step the handshake probe holds back.
const wakePidRm = "        rm -f -- \"$SLEEP_FILE\" 2>/dev/null || :\n        (set -C; printf"

// wakeReadLine is where the parent reads the stored sleep pid.
const wakeReadLine = "    { read -r SLEEP_RECORDED < \"$SLEEP_FILE\"; } 2>/dev/null || :\n"

const (
	wakeTrapLine   = "trap 'STOPPING=1; stop_renewal_commands' TERM INT HUP"
	wakeStopAssign = `STOP_FILE="$WAKE_AGENT_LOG.stop.$$"`
	// wakeMarkerEnv is the fixed marker path fixtures plant at (see fixedMarker).
	wakeMarkerEnv = "WAKE_MARKER"
)

// wakeWait is a fixture wait that always ends: after about 15 s, or as soon as
// the test directory is gone (the test, or the script under test, has finished).
// A wait that runs out its 15 s writes "wait-expired", which fails the test: an
// expired wait is never a pass.
func wakeWait(cond string) string {
	return "WAKE_WN=0; while " + cond + " && [ -d \"$WAKE_DIR\" ] && [ \"$WAKE_WN\" -lt 1500 ]; do /bin/sleep 0.01; WAKE_WN=$((WAKE_WN + 1)); done\n" +
		"if [ \"$WAKE_WN\" -ge 1500 ]; then : > \"$WAKE_DIR/wait-expired\"; fi\n"
}

type wakeRenewerRun struct {
	dir, output string
	err         error
	cleanup     time.Duration // harness exit until the script finished: the cleanup only
	exitSeen    bool          // the harness marked its exit, so cleanup is a real measurement
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
	lostLease    bool   // a renewal answers exit 6 (ownership lost)
	plant        func(dir, marker string)
	env          []string      // extra environment for the script and its fixtures
	timeout      time.Duration // hang backstop for the whole run; 12 s when zero
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

// writeWakeFixtures writes the fake mtix, harness, sleep and rm next to the
// script under test.
func writeWakeFixtures(t *testing.T, dir string, o wakeRenewerOpts) {
	t.Helper()
	write := func(name, body string) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o700))
	}
	write("input", "pending work\n")
	release := "printf '%s\\n' released"
	if o.relaunch {
		release = "if [ -e \"$WAKE_DIR/relaunched\" ]; then printf '%s\\n' released; else : > \"$WAKE_DIR/relaunched\"; printf '%s\\n' relaunch; fi"
	}
	renewal := `printf x >> "$WAKE_DIR/renewals"; [ ! -e "$WAKE_DIR/relaunched" ] || : > "$WAKE_DIR/renewed-after-relaunch"`
	if o.lostLease {
		renewal = `printf x >> "$WAKE_DIR/renewals"; exit 6`
	}
	write("mtix", "#!/bin/sh\nif [ \"${2:-}\" = lease ]; then\n case \"$3\" in acquire) case \" $* \" in *\" --holder \"*) "+renewal+" ;; esac; printf '%s\\n' 0123456789abcdef0123456789abcdef ;; release) "+o.releaseExtra+"\n"+release+" ;; *) exit 1 ;; esac\nelse\n cat \"$WAKE_DIR/input\"\nfi\n")
	wait := wakeWait("[ ! -e \"$WAKE_DIR/" + o.gate + "\" ]")
	if o.gate == "-" {
		wait = ""
	}
	write("claude", "#!/bin/sh\n"+wait+o.harnessExtra+"\ncat > \"$WAKE_DIR/stdin\"\n: > \"$WAKE_DIR/harness-exit\"\n")
	write("sleep", "#!/bin/sh\nprintf '%s\\n' \"$1\" >> \"$WAKE_DIR/sleeps\"\n"+o.sleepBody)
	// An rm that records how many arguments it got: the renewer's own
	// pid-file cleanup has three, the parent's marker cleanup four.
	write("rm", "#!/bin/sh\nprintf '%s\\n' \"$#\" >> \"$WAKE_DIR/rm-args\"\nexec /bin/rm \"$@\"\n")
	write("wake.sh", o.source)
}

// wakeScriptCommand builds the script's command in its own process group, so a
// hung run cannot leave fixture processes behind.
func wakeScriptCommand(ctx context.Context, dir, log string, o wakeRenewerOpts) *exec.Cmd {
	args := []string{"/bin/sh", filepath.Join(dir, "wake.sh"), "fixture-worker"}
	if o.ignoreTerm {
		args = []string{"/bin/sh", "-c", `trap '' TERM; exec /bin/sh "$0" "$@"`, filepath.Join(dir, "wake.sh"), "fixture-worker"}
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir, cmd.WaitDelay = dir, 2*time.Second
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.Env = append(append(os.Environ(), o.env...), "PATH="+dir+":/usr/bin:/bin", "WAKE_DIR="+dir, "WAKE_AGENT_LOG="+log,
		wakeMarkerEnv+"="+log+".stop.fixed", "MTIX_BIN="+filepath.Join(dir, "mtix"), "HARNESS_BIN="+filepath.Join(dir, "claude"))
	return cmd
}

func runWakeRenewer(t *testing.T, o wakeRenewerOpts) wakeRenewerRun {
	t.Helper()
	dir := t.TempDir()
	if o.gate == "" {
		o.gate = "sleeps"
	}
	if o.sleepBody == "" {
		o.sleepBody = "echo SLEEP-STDOUT-SENTINEL\necho SLEEP-STDERR-SENTINEL >&2\n"
	}
	writeWakeFixtures(t, dir, o)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "logs dir"), 0o700))
	log := filepath.Join(dir, "logs dir", "wake [1]*.log")
	if o.plant != nil {
		o.plant(dir, log+".stop.fixed")
	}
	if o.timeout == 0 {
		o.timeout = 12 * time.Second
	}
	ctx, cancel := context.WithTimeout(t.Context(), o.timeout)
	defer cancel()
	cmd := wakeScriptCommand(ctx, dir, log, o)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	require.NoError(t, cmd.Start())
	pgid := cmd.Process.Pid
	t.Cleanup(func() { // only needed if the group did not end by itself
		if syscall.Kill(-pgid, 0) == nil {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		}
	})
	err := cmd.Wait()
	end := time.Now()
	run := wakeRenewerRun{dir: dir, output: out.String(), err: err}
	// The harness's exit file is stamped by the kernel when cleanup starts, so
	// the measurement does not depend on how often the test looks.
	if info, statErr := os.Stat(filepath.Join(dir, "harness-exit")); statErr == nil {
		run.cleanup, run.exitSeen = max(end.Sub(info.ModTime()), 0), true
	}
	require.Eventually(t, func() bool { return syscall.Kill(-pgid, 0) != nil }, 2*time.Second, 20*time.Millisecond,
		"fixture processes survived the run")
	require.NoFileExists(t, filepath.Join(dir, "wait-expired"), "a fixture wait ran out: the run proved nothing")
	return run
}

// requireCleanupWithin asserts the measured cleanup (harness exit to script end).
func requireCleanupWithin(t *testing.T, run wakeRenewerRun, bound time.Duration) {
	t.Helper()
	require.True(t, run.exitSeen, "the harness never marked its exit: cleanup was not measured")
	require.GreaterOrEqual(t, run.cleanup, time.Duration(0))
	require.Less(t, run.cleanup, bound, "cleanup")
	t.Logf("cleanup took %s", run.cleanup)
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
		wait := "trap '' TERM INT HUP\n    WAKE_WN=0; while [ ! -e \"$STOP_FILE\" ] && [ \"$WAKE_WN\" -lt 3000000 ]; do WAKE_WN=$((WAKE_WN + 1)); done\n    if [ \"$WAKE_WN\" -ge 3000000 ]; then : > \"$WAKE_DIR/wait-expired\"; fi\n    WAKE_I=0; while [ \"$WAKE_I\" -lt 30000 ]; do WAKE_I=$((WAKE_I + 1)); done\n    " + wakeTrapLine
		return strings.Replace(s, wakeTrapLine, wait, 1)
	})
	run := runWakeRenewer(t, wakeRenewerOpts{source: source, gate: "-"})
	require.NoError(t, run.err, run.output)
	requireCleanupWithin(t, run, 10*time.Second)
	raw, err := os.ReadFile(filepath.Join(run.dir, "rm-args"))
	require.NoError(t, err)
	threeArg := 0 // the renewer ends with one rm of its pid file; each sleep cycle adds more
	for _, n := range strings.Fields(string(raw)) {
		if n == "3" {
			threeArg++
		}
	}
	require.Equal(t, 1, threeArg, "renewer launched a sleep although stop was already requested")
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
	sleepBody := wakeWait("[ -z \"$(ls \"$WAKE_DIR/logs dir\" | grep '\\.stop\\.[0-9]*$')\" ]")
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
	requireCleanupWithin(t, run, 10*time.Second)
	require.NotContains(t, run.output, "Killed", "job notice leaked")
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
			renewals, err := os.ReadFile(filepath.Join(run.dir, "renewals"))
			require.NoError(t, err, "the lease was never renewed")
			require.NotEmpty(t, renewals)
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
		harnessExtra: "if [ -e \"$WAKE_DIR/relaunched\" ]; then " + wakeWait("[ ! -e \"$WAKE_DIR/renewed-after-relaunch\" ]") + "fi",
	})
	require.NoError(t, run.err, "second cycle must renew: %s", run.output)
	require.FileExists(t, filepath.Join(run.dir, "renewed-after-relaunch"), "no renewal after the relaunch")
}

func TestWakeExample_RenewerStop_StoredPidNotRenewerChild_NeverKilled(t *testing.T) {
	// The harness overwrites the stored pid with a decoy that is not the
	// renewer's child, while the renewer stays in one cycle: cleanup must leave
	// the decoy alone (and still end the real sleep).
	decoy := exec.Command("/bin/sleep", "300")
	require.NoError(t, decoy.Start())
	gone := make(chan struct{}) // closed once the decoy has died and been reaped
	go func() { _ = decoy.Wait(); close(gone) }()
	t.Cleanup(func() { _ = decoy.Process.Kill(); <-gone })
	harness := wakeWait(`[ ! -s "$`+wakeMarkerEnv+`.sleep" ]`) + `printf '%s\n' "$WAKE_DECOY" > "$` + wakeMarkerEnv + `.sleep"
`
	run := runWakeRenewer(t, wakeRenewerOpts{
		source: wakeRenewerSource(t, wakeFixedMarker), harnessExtra: harness, sleepBody: "exec /bin/sleep 60\n",
		env: []string{"WAKE_DECOY=" + strconv.Itoa(decoy.Process.Pid)},
	})
	require.NoError(t, run.err, run.output)
	select {
	case <-gone:
		t.Fatal("decoy process was killed through the stored pid")
	default:
	}
}

func TestWakeExample_RenewerExit_RemovesStoredPid(t *testing.T) {
	// An outside signal ends the renewer's sleep and so the renewer: the stored
	// pid must not outlive it, whatever path it left by.
	harness := wakeWait(`[ ! -s "$`+wakeMarkerEnv+`.sleep" ]`) + `kill "$(cat "$` + wakeMarkerEnv + `.sleep")"
` + wakeWait(`[ -e "$`+wakeMarkerEnv+`.sleep" ]`) + `if [ -e "$` + wakeMarkerEnv + `.sleep" ]; then echo stuck; else echo gone; fi > "$WAKE_DIR/pid-file-state"
`
	run := runWakeRenewer(t, wakeRenewerOpts{
		source: wakeRenewerSource(t, wakeFixedMarker), harnessExtra: harness,
		sleepBody: "exec /bin/sleep 60\n",
	})
	require.NoError(t, run.err, run.output)
	state, err := os.ReadFile(filepath.Join(run.dir, "pid-file-state"))
	require.NoError(t, err)
	require.Equal(t, "gone\n", string(state), "stored pid left behind by an exited renewer")
}

func TestWakeExample_RenewerStop_LostLease_NoLeakedNoise(t *testing.T) {
	harness := wakeWait("true")
	run := runWakeRenewer(t, wakeRenewerOpts{
		source: wakeRenewerSource(t, nil), gate: "renewals", lostLease: true, harnessExtra: harness,
		sleepBody: "exec /bin/sleep 0.01\n",
	})
	require.Error(t, run.err)
	require.Contains(t, run.output, "lease no longer owned")
	require.NotContains(t, run.output, "No such file")
	require.NotContains(t, run.output, "Killed")
	require.NotContains(t, run.output, "logs dir", "log path leaked")
}

// wakeHandshakeSource ignores TERM in the renewer and lets the test order the
// two sides of the stop handshake with files instead of timing.
func wakeHandshakeSource(t *testing.T, edit func(string) string) string {
	t.Helper()
	const recheck = "        if [ -n \"$STOPPING\" ] || [ -e \"$STOP_FILE\" ]; then stop_renewal_commands; return 0; fi\n"
	return wakeRenewerSource(t, func(s string) string {
		for _, part := range []string{wakeReadLine, wakePidRm, recheck} {
			require.Equal(t, 1, strings.Count(s, part), part)
		}
		s = strings.Replace(s, wakeTrapLine, "trap '' TERM INT HUP", 1)
		return edit(s)
	})
}

func TestWakeExample_RenewerStop_HandshakeOrder_PinnedByFiles(t *testing.T) {
	// The renewer holds back its pid write until the parent has read the (still
	// absent) pid, so only the renewer's re-check of the marker can end the sleep.
	// That fails if the re-check is dropped or runs before the pid write. Then the
	// parent allows a passing re-check up to a second to show itself: with the
	// marker created before the pid is read, the re-check can never pass, and a
	// swapped order lets it pass and leaves the sleep running.
	source := wakeHandshakeSource(t, func(s string) string {
		s = strings.Replace(s, wakeReadLine, wakeReadLine+"    : > \"$WAKE_DIR/parent-read\"\n    "+
			wakeWait("[ ! -e \"$WAKE_DIR/rechecked\" ] && [ \"$WAKE_WN\" -lt 100 ]"), 1)
		s = strings.Replace(s, wakePidRm, "        "+wakeWait("[ ! -e \"$WAKE_DIR/parent-read\" ]")+wakePidRm, 1)
		recheck := "        if [ -n \"$STOPPING\" ] || [ -e \"$STOP_FILE\" ]; then stop_renewal_commands; return 0; fi\n"
		return strings.Replace(s, recheck, recheck+"        : > \"$WAKE_DIR/rechecked\"\n", 1)
	})
	run := runWakeRenewer(t, wakeRenewerOpts{source: source, timeout: 25 * time.Second, sleepBody: "printf '%s\\n' \"$$\" > \"$WAKE_DIR/sleep-pid\"\nexec /bin/sleep 60\n"})
	require.NoError(t, run.err, "the renewer must end its own sleep: %s", run.output)
	raw, err := os.ReadFile(filepath.Join(run.dir, "sleep-pid"))
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	require.NoError(t, err)
	require.Eventually(t, func() bool { return syscall.Kill(pid, 0) != nil }, 2*time.Second, 20*time.Millisecond, "recorded sleep still running")
	_, err = os.Stat(filepath.Join(run.dir, "rechecked"))
	require.ErrorIs(t, err, os.ErrNotExist, "re-check passed although the marker was already created")
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
