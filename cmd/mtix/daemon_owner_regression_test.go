// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

func daemonOwnerStart(t *testing.T, dir, spelling string) *daemonOwnerChild {
	t.Helper()
	owner := startDaemonOwnerChild(t, dir, "session")
	owner.event(t, 0, "initialized")
	owner.command(t, "prepare 1 "+spelling)
	owner.event(t, 1, "ready")
	owner.command(t, "go")
	owner.event(t, 1, "running")
	return owner
}
func daemonOwnerMarker(t *testing.T, dir string) []byte {
	t.Helper()
	marker, err := os.ReadFile(filepath.Join(dir, daemonPIDFilename))
	require.NoError(t, err, "live owner's PID marker must remain present")
	return marker
}
func TestDaemonOwner_ProcessNonholderCleanupPreservesMarker(t *testing.T) {
	for _, spelling := range []string{"daemon", "syncdaemon"} {
		t.Run(spelling, func(t *testing.T) {
			dir := t.TempDir()
			owner := daemonOwnerStart(t, dir, spelling)
			marker := daemonOwnerMarker(t, dir)
			require.Equal(t, strconv.Itoa(owner.cmd.Process.Pid), string(marker))
			nonholder := startDaemonOwnerChild(t, dir, "remove")
			nonholder.event(t, 0, "removed")
			nonholder.join(t)
			require.Equal(t, marker, daemonOwnerMarker(t, dir), "non-holder cleanup must not delete live daemon marker")
		})
	}
}
func TestDaemonOwner_ProcessExitPreservesForeignMarker(t *testing.T) {
	for _, spelling := range []string{"daemon", "syncdaemon"} {
		t.Run(spelling, func(t *testing.T) {
			dir := t.TempDir()
			owner := daemonOwnerStart(t, dir, spelling)
			foreign := startDaemonOwnerChild(t, dir, "foreign")
			foreign.event(t, 0, "foreign")
			marker := daemonOwnerMarker(t, dir)
			require.Equal(t, strconv.Itoa(foreign.cmd.Process.Pid), string(marker))
			owner.command(t, "release")
			owner.event(t, 1, "idle")
			owner.command(t, "quit")
			owner.join(t)
			require.Equal(t, marker, daemonOwnerMarker(t, dir), "native daemon exit must preserve a mismatched foreign PID")
			// The live foreign writer is deliberately NOT a lifetime-lock holder.
		})
	}
}
func TestDaemonOwner_ProcessConcurrentStarts(t *testing.T) {
	dir := t.TempDir()
	first := startDaemonOwnerChild(t, dir, "session")
	first.event(t, 0, "initialized")
	second := startDaemonOwnerChild(t, dir, "session")
	second.event(t, 0, "initialized")
	for round := 1; round <= 200; round++ {
		daemonOwnerConcurrentRound(t, dir, round, first, second)
	}
	for _, child := range []*daemonOwnerChild{first, second} {
		child.command(t, "quit")
		child.join(t)
	}
}
func daemonOwnerConcurrentRound(t *testing.T, dir string, round int, first, second *daemonOwnerChild) {
	t.Helper()
	spellings := []string{"daemon", "syncdaemon"}
	children := []*daemonOwnerChild{first, second}
	for i, child := range children {
		child.command(t, fmt.Sprintf("prepare %d %s", round, spellings[(round+i)%2]))
	}
	for _, child := range children {
		child.event(t, round, "ready")
	}
	for _, child := range children {
		child.command(t, "go")
	}
	outcomes := []daemonOwnerEvent{first.event(t, round, "outcome"), second.event(t, round, "outcome")}
	marker, markerErr := os.ReadFile(filepath.Join(dir, daemonPIDFilename))
	t.Logf("round%d outcomes=%+v marker=%q markerErr=%v", round, outcomes, marker, markerErr)
	running, contended := 0, 0
	for _, outcome := range outcomes {
		switch outcome.Kind {
		case "running":
			running++
			if running == 1 {
				if string(marker) != strconv.Itoa(outcome.PID) {
					t.Errorf("round%d marker%q does not identify running PID%d", round, marker, outcome.PID)
				}
			}
		case "contended":
			contended++
		default:
			t.Errorf("round%d unexpected native startup outcome: %+v", round, outcome)
		}
	}
	// Both acquisition outcomes must be known before either winner is released.
	for _, child := range children {
		child.command(t, "release")
	}
	for _, child := range children {
		idle := child.event(t, round, "idle")
		t.Logf("round%d joined cleanup=%+v", round, idle)
	}
	if running != 1 || contended != 1 {
		t.Errorf("round%d expected1running/1contended; running=%d contended=%d", round, running, contended)
	}
}

// Serial held-owner rounds verify the persistent native protocol independently
// of the concurrent200-round behavioral test, which root runs under -race.
func TestDaemonOwner_ProcessProtocol(t *testing.T) {
	dir := t.TempDir()
	children := []*daemonOwnerChild{startDaemonOwnerChild(t, dir, "session")}
	children[0].event(t, 0, "initialized")
	children = append(children, startDaemonOwnerChild(t, dir, "session"))
	children[1].event(t, 0, "initialized")
	for round := 1; round <= 2; round++ {
		daemonOwnerSerialRound(t, dir, round, children)
	}
	for _, child := range children {
		child.command(t, "quit")
		child.join(t)
	}
}
func daemonOwnerSerialRound(t *testing.T, dir string, round int, children []*daemonOwnerChild) {
	t.Helper()
	spellings := []string{"daemon", "syncdaemon"}
	for i, child := range children {
		child.command(t, fmt.Sprintf("prepare %d %s", round, spellings[i]))
	}
	for _, child := range children {
		child.event(t, round, "ready")
	}
	owner, loser := children[round%2], children[(round+1)%2]
	owner.command(t, "go")
	owner.event(t, round, "running")
	marker := daemonOwnerMarker(t, dir)
	require.Equal(t, strconv.Itoa(owner.cmd.Process.Pid), string(marker))
	loser.command(t, "go")
	outcome := loser.event(t, round, "contended")
	require.Contains(t, outcome.Native, "already running")
	require.Equal(t, marker, daemonOwnerMarker(t, dir))
	for _, child := range children {
		child.command(t, "release")
	}
	for _, child := range children {
		child.event(t, round, "idle")
	}
	_, err := os.ReadFile(filepath.Join(dir, daemonPIDFilename))
	require.ErrorIs(t, err, os.ErrNotExist, "matching native owner cleanup removes its marker")
}

// startUnrelatedLiveProcess starts a long-running non-mtix process and returns
// its pid. It stands in for the program that inherits a recorded daemon pid
// after a reboot.
func startUnrelatedLiveProcess(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sleep", "300")
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		_ = cmd.Process.Kill() //nolint:errcheck // test cleanup of a child we own
		_ = cmd.Wait()         //nolint:errcheck // exit status of a killed child is expected
	})
	return cmd.Process.Pid
}

// TestStartDaemonOwnership_ReusedLivePID_FreeLock_StartsAndOwnsPIDFile pins
// MTIX-237: the 0.5.4 daemons gated startup on "is the recorded pid alive", so
// a pid reused by an unrelated live process made every start print "already
// running" and exit 0, and a keep-alive service respawned it forever. The OS
// lock is the only authority: with the lock free the daemon must start,
// whatever live process the diagnostic pid file names.
func TestStartDaemonOwnership_ReusedLivePID_FreeLock_StartsAndOwnsPIDFile(t *testing.T) {
	for _, label := range []string{"mtix daemon", "mtix sync daemon"} {
		t.Run(label, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), ".mtix")
			require.NoError(t, os.MkdirAll(filepath.Join(dir, "data"), 0o755))
			reused := startUnrelatedLiveProcess(t)
			pidPath := filepath.Join(dir, daemonPIDFilename)
			require.NoError(t, os.WriteFile(pidPath, []byte(strconv.Itoa(reused)), 0o600))

			var stderr bytes.Buffer
			owner, err := startDaemonOwnership(&stderr, label, dir)
			require.NoError(t, err)
			require.NotNil(t, owner, "a free lock means this process owns the board")
			t.Cleanup(func() { require.NoError(t, owner.release()) })

			require.NotContains(t, stderr.String(), "already running")
			require.Empty(t, stderr.String())
			body, err := os.ReadFile(pidPath)
			require.NoError(t, err)
			require.Equal(t, strconv.Itoa(os.Getpid()), string(body),
				"the new owner republishes its own pid over the reused one")
		})
	}
}
