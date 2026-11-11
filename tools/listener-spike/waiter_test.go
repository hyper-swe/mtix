// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const waiterArmCount = 8

// waiterEnv is a scratch directory holding a lock path, a log and a
// release file that a held waiter command waits for.
type waiterEnv struct {
	lock, log, release string
}

func newWaiterEnv(t *testing.T) waiterEnv {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the waiter guard is a POSIX shell script")
	}
	dir := t.TempDir()
	return waiterEnv{
		lock:    filepath.Join(dir, "lock"),
		log:     filepath.Join(dir, "log"),
		release: filepath.Join(dir, "release"),
	}
}

// loopScript is the wrapped command: it records its own pid, then waits for
// the release file and gives up by itself after about a minute, so a test
// that dies early can never leave it running for long.
const loopScript = `echo $$ >"$3"; i=0; while [ ! -e "$1" ] && [ "$i" -lt 3000 ]; do sleep 0.02; i=$((i+1)); done; exit "$2"`

// loopPIDFile is where the wrapped command of the waiter named label
// records its pid.
func (e waiterEnv) loopPIDFile(label string) string {
	return filepath.Join(filepath.Dir(e.lock), "loop-"+label+".pid")
}

// arm starts one waiter whose wrapped command blocks until the release
// file exists and then exits with code. The waiter runs in its own process
// group, which is killed when the test ends, pass or fail.
func (e waiterEnv) arm(t *testing.T, label string, code int) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sh", "waiter.sh", e.lock, e.log, label,
		"sh", "-c", loopScript, "sh", e.release, strconv.Itoa(code), e.loopPIDFile(label))
	startInGroup(t, cmd)
	return cmd
}

// armWithEnv is arm with extra environment entries for the script.
func (e waiterEnv) armWithEnv(t *testing.T, label string, extra ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sh", "waiter.sh", e.lock, e.log, label,
		"sh", "-c", loopScript, "sh", e.release, "0", e.loopPIDFile(label))
	cmd.Env = append(os.Environ(), extra...)
	startInGroup(t, cmd)
	return cmd
}

func (e waiterEnv) events(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(e.log)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	require.NoError(t, err)
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if f := strings.Fields(line); len(f) >= 3 {
			out = append(out, f[2])
		}
	}
	return out
}

func count(events []string, want string) int {
	n := 0
	for _, e := range events {
		if e == want {
			n++
		}
	}
	return n
}

// armConcurrently starts waiterArmCount waiters and waits until each has
// logged its decision (armed or guard-skip).
func armConcurrently(t *testing.T, e waiterEnv) []*exec.Cmd {
	t.Helper()
	cmds := make([]*exec.Cmd, 0, waiterArmCount)
	for i := 0; i < waiterArmCount; i++ {
		cmds = append(cmds, e.arm(t, "w"+strconv.Itoa(i), 0))
	}
	require.Eventually(t, func() bool {
		ev := e.events(t)
		return count(ev, "armed")+count(ev, "guard-skip") == waiterArmCount
	}, 15*time.Second, 20*time.Millisecond, "every waiter must log a decision")
	return cmds
}

func releaseAndWait(t *testing.T, e waiterEnv, cmds []*exec.Cmd) {
	t.Helper()
	require.NoError(t, os.WriteFile(e.release, nil, 0o600))
	for _, c := range cmds {
		require.NoError(t, c.Wait())
	}
}

func TestWaiterGuard_ConcurrentArms_YieldExactlyOneHolder(t *testing.T) {
	e := newWaiterEnv(t)
	cmds := armConcurrently(t, e)
	ev := e.events(t)
	require.Equal(t, 1, count(ev, "armed"), "events: %v", ev)
	require.Equal(t, waiterArmCount-1, count(ev, "guard-skip"), "events: %v", ev)
	require.Zero(t, count(ev, "stale-takeover"))
	releaseAndWait(t, e, cmds)
	_, err := os.Lstat(e.lock)
	require.ErrorIs(t, err, os.ErrNotExist, "the holder must release the lock on exit")
}

func TestWaiterGuard_StaleLocks_ConcurrentArmsTakeOverExactlyOnce(t *testing.T) {
	dead := exec.Command("sh", "-c", "exit 0")
	require.NoError(t, dead.Run())
	tests := []struct {
		name   string
		target string
	}{
		{"holder pid is dead", "pid:" + strconv.Itoa(dead.Process.Pid)},
		{"lock target is garbage", "pid:not-a-number"},
		{"lock target has no pid prefix", "12345"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newWaiterEnv(t)
			require.NoError(t, os.Symlink(tt.target, e.lock))
			cmds := armConcurrently(t, e)
			ev := e.events(t)
			// A late arm can win the freed lock before the taker links it, so
			// "took over" may be logged by zero or one arm; the invariant is
			// exactly one holder.
			require.Equal(t, 1, count(ev, "armed"), "events: %v", ev)
			require.LessOrEqual(t, count(ev, "stale-takeover"), 1, "events: %v", ev)
			require.Equal(t, waiterArmCount-1, count(ev, "guard-skip"), "events: %v", ev)
			releaseAndWait(t, e, cmds)
		})
	}
}

func TestWaiterGuard_SingleArmOnStaleLock_LogsTakeoverThenArmed(t *testing.T) {
	e := newWaiterEnv(t)
	require.NoError(t, os.Symlink("pid:not-a-number", e.lock))
	cmd := e.arm(t, "w", 0)
	require.Eventually(t, func() bool { return count(e.events(t), "armed") == 1 }, 10*time.Second, 20*time.Millisecond)
	require.Equal(t, []string{"stale-takeover", "armed"}, e.events(t))
	require.NoError(t, os.WriteFile(e.release, nil, 0o600))
	require.NoError(t, cmd.Wait())
}

func TestWaiterGuard_LiveHolderLock_IsNeverStolen(t *testing.T) {
	e := newWaiterEnv(t)
	holder := exec.Command("sleep", "30")
	require.NoError(t, holder.Start())
	t.Cleanup(func() { _ = holder.Process.Kill(); _ = holder.Wait() })
	require.NoError(t, os.Symlink("pid:"+strconv.Itoa(holder.Process.Pid), e.lock))
	cmds := armConcurrently(t, e)
	ev := e.events(t)
	require.Zero(t, count(ev, "armed"), "events: %v", ev)
	require.Equal(t, waiterArmCount, count(ev, "guard-skip"))
	releaseAndWait(t, e, cmds)
	target, err := os.Readlink(e.lock)
	require.NoError(t, err)
	require.Equal(t, "pid:"+strconv.Itoa(holder.Process.Pid), target, "a live holder's lock stays untouched")
}

func TestWaiterGuard_WrappedCommandExit_IsPropagatedAndLockReleased(t *testing.T) {
	e := newWaiterEnv(t)
	cmd := e.arm(t, "w", 2)
	require.Eventually(t, func() bool { return count(e.events(t), "armed") == 1 }, 10*time.Second, 20*time.Millisecond)
	require.NoError(t, os.WriteFile(e.release, nil, 0o600))
	err := cmd.Wait()
	var exitErr *exec.ExitError
	require.ErrorAs(t, err, &exitErr)
	require.Equal(t, 2, exitErr.ExitCode(), "exit code 2 is the wake signal and must pass through")
	_, lerr := os.Lstat(e.lock)
	require.ErrorIs(t, lerr, os.ErrNotExist)
	require.Equal(t, 1, count(e.events(t), "exit-2"))
}

func TestWaiterGuard_Script_IsExecutable(t *testing.T) {
	info, err := os.Stat("waiter.sh")
	require.NoError(t, err)
	if runtime.GOOS != "windows" {
		require.NotZero(t, info.Mode()&0o111, "waiter.sh must keep its executable bit")
	}
}

// deadPID returns the pid of a process that has exited.
func deadPID(t *testing.T) int {
	t.Helper()
	dead := exec.Command("sh", "-c", "exit 0")
	require.NoError(t, dead.Run())
	return dead.Process.Pid
}

// TestWaiterGuard_LiveHolderPlantedUnderSerialiser_IsNeverStolen drives the
// race the serialiser re-read exists for: an arm that saw a stale lock gets
// the serialiser after another arm has already installed a live holder.
func TestWaiterGuard_LiveHolderPlantedUnderSerialiser_IsNeverStolen(t *testing.T) {
	e := newWaiterEnv(t)
	require.NoError(t, os.Symlink("pid:"+strconv.Itoa(deadPID(t)), e.lock))
	hold := filepath.Join(filepath.Dir(e.lock), "hold")
	require.NoError(t, os.WriteFile(hold, nil, 0o600))
	cmd := e.armWithEnv(t, "late", "WAITER_TEST_HOLD="+hold)
	require.Eventually(t, func() bool {
		_, err := os.Stat(e.lock + ".t")
		return err == nil
	}, 10*time.Second, 10*time.Millisecond, "the arm must own the serialiser")
	live := exec.Command("sleep", "30")
	require.NoError(t, live.Start())
	t.Cleanup(func() { _ = live.Process.Kill(); _ = live.Wait() })
	require.NoError(t, os.Remove(e.lock))
	require.NoError(t, os.Symlink("pid:"+strconv.Itoa(live.Process.Pid), e.lock))
	require.NoError(t, os.Remove(hold))
	// A wrongly armed waiter must exit rather than hang the suite.
	require.NoError(t, os.WriteFile(e.release, nil, 0o600))
	require.NoError(t, cmd.Wait())
	require.Equal(t, []string{"guard-skip"}, e.events(t))
	target, err := os.Readlink(e.lock)
	require.NoError(t, err)
	require.Equal(t, "pid:"+strconv.Itoa(live.Process.Pid), target, "the live holder's lock stays untouched")
	_, err = os.Stat(e.lock + ".t")
	require.ErrorIs(t, err, os.ErrNotExist, "the serialiser is released")
}

// TestWaiterGuard_StuckSerialiser_LogsDistinctTakeoverBusy covers a taker
// killed between mkdir and rmdir: the stuck serialiser must not look like a
// live holder in the evidence.
func TestWaiterGuard_StuckSerialiser_LogsDistinctTakeoverBusy(t *testing.T) {
	e := newWaiterEnv(t)
	stale := "pid:" + strconv.Itoa(deadPID(t))
	require.NoError(t, os.Symlink(stale, e.lock))
	require.NoError(t, os.Mkdir(e.lock+".t", 0o700))
	cmd := e.arm(t, "late", 0)
	require.NoError(t, cmd.Wait())
	require.Equal(t, []string{"guard-skip-takeover-busy"}, e.events(t))
	target, err := os.Readlink(e.lock)
	require.NoError(t, err)
	require.Equal(t, stale, target, "the stale lock is left as found")
}
