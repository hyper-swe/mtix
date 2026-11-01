// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/attach"
	"github.com/hyper-swe/mtix/internal/oplocal"
)

func armLease(t *testing.T, src *hubContactSource, pid int) {
	t.Helper()
	st, _ := src.startTime(pid)
	require.NoError(t, src.state.WriteListenLease(src.key(),
		oplocal.ListenLease{PID: pid, StartTime: st, Agent: "dev", ArmedAt: schedT0}))
}

// recordFreshActivity makes the listen hold armed on the fixture clock.
func recordFreshActivity(t *testing.T, src *hubContactSource) {
	t.Helper()
	require.NoError(t, src.state.RecordHubActivity(src.key(), oplocal.HubActivity{At: schedT0, Kind: oplocal.ActivityTurn}))
}

func lockHeldFn(held bool, err error) func(string) (bool, error) {
	return func(string) (bool, error) { return held, err }
}

func TestListenerDoctor_LeaseWithoutListener_FailsWithFix(t *testing.T) {
	src := hubSourceFixture(t, listenerHubConfig)
	recordFreshActivity(t, src)
	armLease(t, src, 7)

	row := listenerDoctorRow(src, true, false, lockHeldFn(false, nil))
	require.Equal(t, "wake.listener.no-hub-listener", row.Name)
	require.False(t, row.Pass)
	require.Equal(t, "mtix daemon --listen", row.Fix)
	require.Contains(t, row.Detail, "dev (pid 7)")

	raw, err := json.Marshal(row)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"fix":"mtix daemon --listen"`, "--json carries the exact fix")
}

func TestListenerDoctor_Table(t *testing.T) {
	tests := []struct {
		name     string
		cfg      string
		hub, rel bool
		lease    bool
		held     bool
		lockErr  error
		active   bool
		wantPass bool
		wantText string
	}{
		{"listener running", listenerHubConfig, true, false, true, true, nil, true, true, "listener running"},
		{"no lease", listenerHubConfig, true, false, false, false, nil, true, true, "no live waiter lease"},
		{"relay board, lease, no listener, no hub activity", listenerHubConfig, false, true, true, false, nil, false, false, "no listener"},
		{"no hub or relay", listenerHubConfig, false, false, true, false, nil, true, true, "no hub or relay"},
		{"listen off", "prefix: \"X\"\nsync:\n  listen: \"off\"\n", true, false, true, false, nil, true, true, "sync.listen is off"},
		{"manual", "prefix: \"X\"\nsync:\n  hub_contact: \"manual\"\n", true, false, true, false, nil, true, true, "manual"},
		{"lock check fails", listenerHubConfig, true, false, true, false, errors.New("disk"), true, false, "cannot check"},
		{"hub, no recent activity: idle-clamped, no listener demanded", listenerHubConfig, true, false, true, false, nil, false, true, "idle-clamped"},
		{"hub, active, lease, no listener: fails", listenerHubConfig, true, false, true, false, nil, true, false, "no listener"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := hubSourceFixture(t, tt.cfg)
			if tt.lease {
				armLease(t, src, 8)
			}
			if tt.active {
				recordFreshActivity(t, src)
			}
			row := listenerDoctorRow(src, tt.hub, tt.rel, lockHeldFn(tt.held, tt.lockErr))
			require.Equal(t, tt.wantPass, row.Pass)
			require.Contains(t, row.Detail, tt.wantText)
			if !row.Pass {
				require.NotEmpty(t, row.Fix, "every failure names a fix")
			}
		})
	}
}

func TestDaemonLockHeld_ReflectsTheLifetimeLock(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".mtix")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	held, err := daemonLockHeld(dir)
	require.NoError(t, err)
	require.False(t, held)
	owner, err := acquireDaemonOwnership(dir)
	require.NoError(t, err)
	held, err = daemonLockHeld(dir)
	require.NoError(t, err)
	require.True(t, held)
	require.NoError(t, owner.release())
	held, err = daemonLockHeld(dir)
	require.NoError(t, err)
	require.False(t, held, "the probe releases the lock it briefly takes")
}

// credentialPatterns name material that must never appear in text a hook
// runs: a DSN, a password, a secrets path or a credential variable.
var credentialPatterns = regexp.MustCompile(`(?i)postgres(ql)?://|password|passwd|\.mtix/secrets|MTIX_SYNC_DSN|MTIX_PG_|PGPASSWORD|sslmode=|api[_-]?key|token=`)

func TestHookTemplates_NoCredentialMaterial(t *testing.T) {
	shim, err := attach.ShimScript(attach.ShimRecord{Target: "/usr/local/bin/mtix", UID: 501, Protocol: 1})
	require.NoError(t, err)
	require.NotRegexp(t, credentialPatterns, string(shim), "the attach shim names no credential")

	require.Equal(t, []string{"daemon", "--listen"}, listenerArgv(), "the spawned listener has a fixed argv")
	require.NotRegexp(t, credentialPatterns, strings.Join(listenerArgv(), " "))

	// Every hook command line shipped in docs and plugins: the lines that run
	// the waiter or the listener must not carry credential material.
	roots := []string{"../../internal/docs/templates", "../../.claude-plugin", "../../.codex-plugin"}
	hookLine := regexp.MustCompile(`mtix (inbox --wait|daemon --listen|attach )`)
	scanned := 0
	for _, root := range roots {
		require.NoError(t, filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			body, readErr := os.ReadFile(p)
			require.NoError(t, readErr)
			for i, line := range strings.Split(string(body), "\n") {
				if hookLine.MatchString(line) && credentialPatterns.MatchString(line) {
					t.Errorf("%s:%d: hook command line names credential material", p, i+1)
				}
				scanned++
			}
			return nil
		}))
	}
	require.Positive(t, scanned, "the scan must read the shipped templates")
}

func TestListenerDoctor_PastIdleClamp_PassesAndFixWouldBeInert(t *testing.T) {
	src := hubSourceFixture(t, listenerHubConfig)
	require.NoError(t, src.state.RecordHubActivity(src.key(), oplocal.HubActivity{At: schedT0.Add(-9 * time.Hour), Kind: oplocal.ActivityTurn}))
	armLease(t, src, 9)
	row := listenerDoctorRow(src, true, false, lockHeldFn(false, nil))
	require.True(t, row.Pass, "the clamp has passed: a listener would exit at once, so none is demanded")
	require.Empty(t, row.Fix)
	require.Contains(t, row.Detail, "idle-clamped")

	// New activity re-arms the hold; with a parked waiter and no listener the row fails again.
	recordFreshActivity(t, src)
	row = listenerDoctorRow(src, true, false, lockHeldFn(false, nil))
	require.False(t, row.Pass)
	require.Equal(t, "mtix daemon --listen", row.Fix)
}
