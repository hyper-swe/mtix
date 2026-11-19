// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Cleanup probe and mutation checks for MTIX-274.1.1 through MTIX-274.1.3.
package scripts_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// MTIX-274.1: KILL must end even a job still between fork and exec.
// HUP, INT and TERM ignores survive exec; readiness proves they are installed.
func TestRemoteBar_Cleanup_EndsABackgroundJobThatIgnoresTerm(t *testing.T) {
	script, err := filepath.Abs(scriptPath)
	require.NoError(t, err)
	assertCleanupProbe(t, script)
}

func assertCleanupProbe(t *testing.T, script string) {
	t.Helper()
	snippet := `source "$RB_SCRIPT"
mkfifo ready
(trap '' HUP INT TERM; echo ready > ready; exec sleep 300) &
p=$!
echo "pid=$p"
read -r ready < ready
rb_cleanup
while :; do
 stat=$(ps -o stat= -p "$p" 2>/dev/null) || {
  rc=$?
  [ "$rc" = 1 ] || exit "$rc"
  stat=""
 }
 stat=$(echo "$stat" | tr -d ' ')
 case "$stat" in ""|Z*) break ;; esac
 sleep 0.02
done
echo "ended=$p"`
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-c", snippet)
	cmd.Dir = t.TempDir()
	cmd.Env = cleanEnv("RB_SCRIPT=" + script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killCleanupGroup(cmd.Process.Pid) }
	// A killed wrapper may leave the sleep holding the output pipe open.
	cmd.WaitDelay = 100 * time.Millisecond
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { require.NoError(t, killCleanupGroup(cmd.Process.Pid)) })
	err := cmd.Wait()
	t.Log(out.String())
	require.NoError(t, err, "cleanup probe execution: %s", out.String())
	require.Contains(t, out.String(), "ended=", "cleanup never observed the job end")
}

// Run the assertion in a separate test process so expected failures remain
// observable. The outer watchdog also bounds the pre-fix orphan-pipe hang.
func TestRemoteBar_CleanupProbe_Mutations(t *testing.T) {
	if script := os.Getenv("MTIX_CLEANUP_PROBE_SCRIPT"); script != "" {
		assertCleanupProbe(t, script)
		return
	}
	cases := []struct {
		name, replacement, suffix string
		wantPass                  bool
	}{
		{"KILL", "-KILL \"$job\"", "", true},
		{"numeric_KILL", "-9 \"$job\"", "", true},
		{"HUP", "-HUP \"$job\"", "", false},
		{"INT", "-INT \"$job\"", "", false},
		{"TERM", "-TERM \"$job\"", "", false},
		{"wrapper_killed", "-KILL \"$$\"", "", false},
		{"cleanup_aborts", "-KILL \"$job\"", "\nrb_cleanup() { exit 17; }\n", false},
		{"delayed_observation", "-KILL \"$job\"", `
ps() {
 local n=0
 [ ! -f "$TMPDIR/ps-count" ] || read -r n < "$TMPDIR/ps-count"
 n=$((n + 1))
 echo "$n" > "$TMPDIR/ps-count"
 if [ "$n" -le 2 ]; then echo S; else command ps "$@"; fi
}
`, true},
	}
	source, err := os.ReadFile(scriptPath)
	require.NoError(t, err)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			copyCleanupDependencies(t, dir)
			script := filepath.Join(dir, "remote-bar.sh")
			require.Contains(t, string(source), `-KILL "$job"`)
			mutated := strings.Replace(string(source), `-KILL "$job"`, tc.replacement, 1) + tc.suffix
			require.NoError(t, os.WriteFile(script, []byte(mutated), 0o600))
			out, timedOut, err := runCleanupAssertion(t, script)
			require.False(t, timedOut, "probe hung after cleanup: %s", out)
			require.Equal(t, tc.wantPass, err == nil, "%v: %s", err, out)
			assertCleanupJobEnded(t, out)
		})
	}
}

func runCleanupAssertion(t *testing.T, script string) (string, bool, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRemoteBar_CleanupProbe_Mutations$", "-test.v")
	cmd.Env = append(os.Environ(), "MTIX_CLEANUP_PROBE_SCRIPT="+script, "TMPDIR="+filepath.Dir(script))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killCleanupGroup(cmd.Process.Pid) }
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.WaitDelay = time.Second
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { require.NoError(t, killCleanupGroup(cmd.Process.Pid)) })
	err := cmd.Wait()
	return out.String(), ctx.Err() != nil, err
}

func killCleanupGroup(pid int) error {
	err := syscall.Kill(-pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("kill cleanup probe group %d: %w", pid, err)
	}
	return nil
}

func copyCleanupDependencies(t *testing.T, dir string) {
	t.Helper()
	for _, name := range []string{"remote-bar-workflow.sh", "remote-bar-env.sh"} {
		data, err := os.ReadFile(name)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), data, 0o600))
	}
}

// Check after the child test's Cleanup hooks have run, including early exits.
func assertCleanupJobEnded(t *testing.T, out string) {
	t.Helper()
	var pid string
	for _, field := range strings.Fields(out) {
		if strings.HasPrefix(field, "pid=") {
			pid = strings.TrimPrefix(field, "pid=")
			break
		}
	}
	require.Regexp(t, `^[1-9][0-9]*$`, pid, out)
	require.Eventually(t, func() bool {
		state, err := exec.Command("ps", "-o", "stat=", "-p", pid).Output()
		if err != nil {
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
				t.Errorf("inspect cleanup probe job %s: %v", pid, err)
				return false
			}
		}
		stat := strings.TrimSpace(string(state))
		return stat == "" || strings.HasPrefix(stat, "Z")
	}, time.Second, 20*time.Millisecond, "probe left its job running: %s", pid)
}
