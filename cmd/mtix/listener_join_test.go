// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
)

const listenerHubConfig = "prefix: \"X\"\nsync:\n  hub_contact: \"activity\"\n  listen: \"live\"\n"

// joinFor builds one waiter's join over a shared board fixture. The fake
// listener process is the real daemon lifetime lock: spawn takes it and keeps
// it, exactly as the child would.
func joinFor(t *testing.T, src *hubContactSource, pid int, spawns *int, out *bytes.Buffer, cfg *service.ConfigService) *listenerJoin {
	t.Helper()
	return &listenerJoin{
		state: src.state, key: src.key(), agent: "agent", pid: pid, now: src.now, startTime: src.startTime, out: out,
		plan:    func() listenerPlan { return planListener(cfg, true, false, src.grandfatherInterval) },
		running: func() (bool, error) { return daemonLockHeld(src.mtixDir) },
		spawn: func() error {
			*spawns++
			owner, err := acquireDaemonOwnership(src.mtixDir)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, owner.release()) })
			return nil
		},
	}
}

func TestListener_TwoWaiters_OneProcess(t *testing.T) {
	src := hubSourceFixture(t, listenerHubConfig)
	cfg, err := service.NewConfigService(src.configPath())
	require.NoError(t, err)
	var out1, out2 bytes.Buffer
	spawns := 0

	leave1 := joinFor(t, src, 11, &spawns, &out1, cfg).Join(nil, nil)
	leave2 := joinFor(t, src, 12, &spawns, &out2, cfg).Join(nil, nil)

	require.Equal(t, 1, spawns, "the second waiter finds the lifetime lock held and starts no second listener")
	require.Contains(t, out1.String(), "hub listener started")
	require.Contains(t, out2.String(), "hub listener already running")
	leases, err := src.liveLeaseList()
	require.NoError(t, err)
	require.Len(t, leases, 2, "each waiter holds its own lease")

	leave1()
	leases, err = src.liveLeaseList()
	require.NoError(t, err)
	require.Len(t, leases, 1, "ending one waiter leaves the other's lease")
	leave2()
	leases, err = src.liveLeaseList()
	require.NoError(t, err)
	require.Empty(t, leases)
}

func TestListenerJoin_OptOuts_PrintOneLineAndStartNothing(t *testing.T) {
	tests := []struct {
		name, cfg string
		hub, rel  bool
		want      string
		quiet     bool
	}{
		{"listen off", "prefix: \"X\"\nsync:\n  listen: \"off\"\n", true, false, "sync.listen is off", false},
		{"manual", "prefix: \"X\"\nsync:\n  hub_contact: \"manual\"\n", true, false, "sync.hub_contact is manual", false},
		{"no hub or relay", listenerHubConfig, false, false, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := hubSourceFixture(t, tt.cfg)
			cfg, err := service.NewConfigService(src.configPath())
			require.NoError(t, err)
			var out bytes.Buffer
			spawns := 0
			j := joinFor(t, src, 21, &spawns, &out, cfg)
			j.plan = func() listenerPlan { return planListener(cfg, tt.hub, tt.rel, src.grandfatherInterval) }
			j.Join(nil, nil)()
			require.Zero(t, spawns)
			leases, err := src.state.ListListenLeases(src.key())
			require.NoError(t, err)
			require.Empty(t, leases, "an opted-out waiter arms no lease")
			if tt.quiet {
				require.Empty(t, out.String())
				return
			}
			require.Equal(t, 1, strings.Count(out.String(), "\n"), "exactly one line")
			require.Contains(t, out.String(), tt.want)
		})
	}
}

func TestListenerJoin_SpawnFailure_IsOneLineWithFixAndDoesNotFailTheWait(t *testing.T) {
	src := hubSourceFixture(t, listenerHubConfig)
	cfg, err := service.NewConfigService(src.configPath())
	require.NoError(t, err)
	var out bytes.Buffer
	spawns := 0
	j := joinFor(t, src, 31, &spawns, &out, cfg)
	j.spawn = func() error { return errors.New("cannot exec") }
	j.Join(nil, nil)()
	require.Contains(t, out.String(), "listener not started")
	require.Contains(t, out.String(), "fix: mtix daemon --listen")
}

func TestListenerLease_CrashedWaiter_ExpiresOnInjectedClock(t *testing.T) {
	src := hubSourceFixture(t, listenerHubConfig)
	st, _ := src.startTime(5)
	armed := schedT0
	require.NoError(t, src.state.WriteListenLease(src.key(), oplocal.ListenLease{PID: 5, StartTime: st, ArmedAt: armed}))

	src.now = func() time.Time { return armed.Add(oplocal.ListenLeaseTTL - time.Second) }
	live, err := src.liveLeases()
	require.NoError(t, err)
	require.True(t, live, "inside the TTL the lease of a running pid is live")

	src.now = func() time.Time { return armed.Add(oplocal.ListenLeaseTTL) }
	live, err = src.liveLeases()
	require.NoError(t, err)
	require.False(t, live, "a waiter that stopped refreshing expires even though its pid is alive")

	require.NoError(t, src.state.WriteListenLease(src.key(), oplocal.ListenLease{PID: 5, StartTime: st, ArmedAt: armed, RefreshedAt: armed.Add(10 * time.Minute)}))
	live, err = src.liveLeases()
	require.NoError(t, err)
	require.True(t, live, "a refresh keeps the lease alive")

	src.now = func() time.Time { return armed.Add(-time.Hour) }
	live, err = src.liveLeases()
	require.NoError(t, err)
	require.False(t, live, "a lease dated far ahead of the clock is stale, not live")
}

func TestListenerJoin_Refresh_RewritesLeaseOnTick(t *testing.T) {
	src := hubSourceFixture(t, listenerHubConfig)
	cfg, err := service.NewConfigService(src.configPath())
	require.NoError(t, err)
	var out bytes.Buffer
	spawns := 0
	now := schedT0
	j := joinFor(t, src, 41, &spawns, &out, cfg)
	j.now = func() time.Time { return now }
	ticks := make(chan time.Time)
	leave := j.Join(ticks, nil)
	now = schedT0.Add(5 * time.Minute)
	ticks <- now
	ticks <- now // the second send returns only after the first refresh was written
	leases := mustLeases(t, src)
	require.Len(t, leases, 1)
	require.True(t, now.Equal(leases[0].RefreshedAt), "the tick refreshed the lease on the injected clock")
	leave()
	require.Empty(t, mustLeases(t, src))
}

func mustLeases(t *testing.T, src *hubContactSource) []oplocal.ListenLease {
	t.Helper()
	l, err := src.state.ListListenLeases(src.key())
	require.NoError(t, err)
	return l
}

func TestJoinListener_NoBoardOrNoHub_ReturnsWorkingLeaveAndPrintsNothing(t *testing.T) {
	src := hubSourceFixture(t, "prefix: \"X\"\n")
	cfg, err := service.NewConfigService(src.configPath())
	require.NoError(t, err)
	saved := app
	t.Cleanup(func() { app = saved })
	for name, a := range map[string]appContext{
		"no board":     {},
		"board no hub": {mtixDir: src.mtixDir, configSvc: cfg},
	} {
		t.Run(name, func(t *testing.T) {
			app = a
			var out bytes.Buffer
			leave := joinListener("agent", &out)
			leave() // must return, not recurse into itself
			require.Empty(t, out.String())
		})
	}
}

func TestListenerJoin_ListenerExitsAfterProbe_RecheckRestartsIt(t *testing.T) {
	src := hubSourceFixture(t, listenerHubConfig)
	cfg, err := service.NewConfigService(src.configPath())
	require.NoError(t, err)
	var out bytes.Buffer
	spawns := 0
	var mu sync.Mutex
	probes := []bool{true, false, true} // mid-exit holder at arm, gone at the first recheck, a new listener after
	j := joinFor(t, src, 51, &spawns, &out, cfg)
	j.running = func() (bool, error) {
		mu.Lock()
		defer mu.Unlock()
		held := probes[0]
		if len(probes) > 1 {
			probes = probes[1:]
		}
		return held, nil
	}
	j.spawn = func() error { mu.Lock(); spawns++; mu.Unlock(); return nil }
	recheck := make(chan time.Time)
	leave := j.Join(nil, recheck)
	require.Contains(t, out.String(), "already running", "arm time: the exiting holder still held the lock")
	require.Zero(t, spawns)

	recheck <- schedT0
	recheck <- schedT0 // the second send returns only after the first recheck ran
	mu.Lock()
	require.Equal(t, 1, spawns, "the holder is gone at the recheck: the waiter starts a listener")
	mu.Unlock()
	leave()
	require.Contains(t, out.String(), "restarted")
}

func TestListenerJoin_RecheckSpawnFailure_ReportedOnce(t *testing.T) {
	src := hubSourceFixture(t, listenerHubConfig)
	cfg, err := service.NewConfigService(src.configPath())
	require.NoError(t, err)
	var out bytes.Buffer
	spawns := 0
	j := joinFor(t, src, 52, &spawns, &out, cfg)
	j.running = func() (bool, error) { return true, nil }
	recheck := make(chan time.Time)
	leave := j.Join(nil, recheck)
	j.running = func() (bool, error) { return false, nil } // read by the keep-alive goroutine only after the next send synchronizes
	j.spawn = func() error { return errors.New("cannot exec") }
	recheck <- schedT0
	recheck <- schedT0
	recheck <- schedT0
	leave()
	require.Equal(t, 1, strings.Count(out.String(), "listener not started"), "a failing respawn is reported once, not every tick")
}

func TestListenerJoin_IdleClamped_NoRespawnNoRestartLine_ActivityGivesOneSpawn(t *testing.T) {
	src := hubSourceFixture(t, listenerHubConfig)
	cfg, err := service.NewConfigService(src.configPath())
	require.NoError(t, err)
	var out bytes.Buffer
	var mu sync.Mutex
	spawns, clamped, held := 0, true, false
	j := joinFor(t, src, 61, &spawns, &out, cfg)
	j.idleClamped = func() bool { mu.Lock(); defer mu.Unlock(); return clamped }
	j.running = func() (bool, error) { mu.Lock(); defer mu.Unlock(); return held, nil }
	j.spawn = func() error { mu.Lock(); defer mu.Unlock(); spawns++; held = true; return nil }

	recheck := make(chan time.Time)
	leave := j.Join(nil, recheck)
	require.Zero(t, spawns, "parking past the clamp starts no listener")
	require.Contains(t, out.String(), "idle-clamped")
	armedLines := strings.Count(out.String(), "\n")

	for i := 0; i < 360; i++ { // one simulated hour of 10 s rechecks
		recheck <- schedT0.Add(time.Duration(i) * 10 * time.Second)
	}
	mu.Lock()
	require.Zero(t, spawns, "0 spawns over an hour past the clamp")
	clamped, held = false, false // real activity re-armed the hold; the board has no listener
	mu.Unlock()
	recheck <- schedT0.Add(time.Hour)
	recheck <- schedT0.Add(time.Hour + 10*time.Second)
	recheck <- schedT0.Add(time.Hour + 20*time.Second)
	mu.Lock()
	require.Equal(t, 1, spawns, "activity then gives exactly one spawn (the listener now holds the lock)")
	mu.Unlock()
	leave()
	require.Equal(t, 1, strings.Count(out.String(), "restarted"), "one restart line, and none while clamped")
	require.Equal(t, armedLines, strings.Count(out.String(), "\n")-1, "no other line was printed during the clamped hour")
}

func TestDaemonOwnershipFor_WaitsOutAProbe_AndGivesUpOnARealOwner(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".mtix")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	probe, err := acquireDaemonOwnership(dir)
	require.NoError(t, err)
	time.AfterFunc(150*time.Millisecond, func() { _ = probe.release() })
	owner, err := daemonOwnershipFor(io.Discard, dir)
	require.NoError(t, err)
	require.NotNil(t, owner, "a lock held only for an instant is waited out, not mistaken for another daemon")
	require.NoError(t, owner.release())

	holder, err := acquireDaemonOwnership(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, holder.release()) })
	var stderr bytes.Buffer
	owner, err = daemonOwnershipFor(&stderr, dir)
	require.NoError(t, err)
	require.Nil(t, owner, "a real owner makes the second start exit cleanly")
	require.Contains(t, stderr.String(), "already running")
}

func TestDaemonLockHeld_IsReadOnly_NeverWritesThePIDFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".mtix")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "data"), 0o755))
	pidPath := filepath.Join(dir, daemonPIDFilename)

	held, err := daemonLockHeld(dir)
	require.NoError(t, err)
	require.False(t, held)
	_, statErr := os.Stat(pidPath)
	require.ErrorIs(t, statErr, os.ErrNotExist, "probing a free board creates no PID file")
	_, statErr = os.Stat(filepath.Join(dir, "data", daemonLockFilename))
	require.ErrorIs(t, statErr, os.ErrNotExist, "and no lock file")

	require.NoError(t, os.WriteFile(pidPath, []byte("4242"), 0o600))
	owner, err := acquireDaemonOwnership(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, owner.release()) })
	before, err := os.ReadFile(pidPath)
	require.NoError(t, err)
	held, err = daemonLockHeld(dir)
	require.NoError(t, err)
	require.True(t, held)
	after, err := os.ReadFile(pidPath)
	require.NoError(t, err)
	require.Equal(t, string(before), string(after), "the probe leaves the owner's PID file alone")
}

func TestListenerJoin_HostileErrorText_RendersWithoutControlCharacters(t *testing.T) {
	src := hubSourceFixture(t, listenerHubConfig)
	cfg, err := service.NewConfigService(src.configPath())
	require.NoError(t, err)
	var out bytes.Buffer
	spawns := 0
	j := joinFor(t, src, 32, &spawns, &out, cfg)
	j.spawn = func() error { return errors.New("bad\x1b[2Jline\nforged: ok\a") }
	j.Join(nil, nil)()
	got := out.String()
	require.Contains(t, got, "listener not started")
	require.Equal(t, 1, strings.Count(got, "\n"), "the hostile newline must not start a second line")
	for _, r := range strings.TrimSuffix(got, "\n") {
		require.False(t, r < 0x20 || r == 0x7f, "control character %q in %q", r, got)
	}
}
