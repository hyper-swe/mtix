// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/attach"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/sync/pushlock"
)

func hubSourceFixture(t *testing.T, cfg string) *hubContactSource {
	t.Helper()
	state := identityOperatorState(t)
	dir := filepath.Join(t.TempDir(), ".mtix")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	if cfg != "" {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o600))
	}
	return &hubContactSource{mtixDir: dir, state: state, grandfatherInterval: 5 * time.Second,
		now:       func() time.Time { return schedT0 },
		startTime: func(pid int) (string, error) { return "st-" + strings.Repeat("x", pid%3), nil }}
}

func TestHubContactSource_Snapshot_ReadsConfigActivityAndLiveLease(t *testing.T) {
	src := hubSourceFixture(t, "prefix: \"X\"\nsync:\n  hub_contact: \"manual\"\n  listen: \"off\"\n")
	at := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)
	require.NoError(t, src.state.RecordHubActivity(src.key(), oplocal.HubActivity{At: at, Kind: oplocal.ActivityTurn}))
	live, _ := src.startTime(3)
	require.NoError(t, src.state.WriteListenLease(src.key(), oplocal.ListenLease{PID: 3, StartTime: live, ArmedAt: at.Add(time.Minute)}))
	require.NoError(t, src.state.WriteListenLease(src.key(), oplocal.ListenLease{PID: 4, StartTime: "reused-pid", ArmedAt: at}))

	in, err := src.snapshot()
	require.NoError(t, err)
	require.Equal(t, service.HubContactManual, in.Mode.Kind)
	require.False(t, in.Listen.Hold)
	require.True(t, at.Equal(in.LastActivity))
	require.True(t, in.LiveLease, "a lease whose pid start time differs is not live, the matching one is")
}

func TestHubContactSource_Snapshot_DeadLeaseAndNoActivity(t *testing.T) {
	src := hubSourceFixture(t, "")
	src.startTime = func(int) (string, error) { return "", errors.New("no such process") }
	require.NoError(t, src.state.WriteListenLease(src.key(), oplocal.ListenLease{PID: 9, StartTime: "s", ArmedAt: time.Now()}))
	in, err := src.snapshot()
	require.NoError(t, err)
	require.False(t, in.LiveLease)
	require.True(t, in.LastActivity.IsZero(), "arming a lease is never activity")
}

func TestHubContactSource_Snapshot_BadConfig_FailsSafe(t *testing.T) {
	src := hubSourceFixture(t, "sync:\n  hub_contact: \"sometimes\"\n")
	// An invalid stored value is ignored by Set validation but may be hand-edited.
	_, err := src.snapshot()
	require.ErrorIs(t, err, service.ErrContactInputs)
}

func TestRecordHubActivityAt_WritesOperatorLocalRecord_AtInjectedTime(t *testing.T) {
	src := hubSourceFixture(t, "")
	require.NoError(t, recordHubActivityAt(src.mtixDir, oplocal.ActivityAddressedSend, src.now))
	got, present, err := src.state.ReadHubActivity(src.key())
	require.NoError(t, err)
	require.True(t, present)
	require.Equal(t, oplocal.ActivityAddressedSend, got.Kind)
	require.True(t, schedT0.Equal(got.At))
	entries, err := os.ReadDir(src.mtixDir)
	require.NoError(t, err)
	require.Empty(t, entries, "nothing is written into the board directory")
}

func TestCommandActivity_KindPerCommand(t *testing.T) {
	root := &cobra.Command{Use: "mtix"}
	agent := &cobra.Command{Use: "agent"}
	heartbeat := &cobra.Command{Use: "heartbeat"}
	agent.AddCommand(heartbeat)
	comment := &cobra.Command{Use: "comment"}
	comment.Flags().String("to", "", "")
	other := &cobra.Command{Use: "heartbeat"} // a top-level command of the same name is not the agent heartbeat
	for _, c := range []*cobra.Command{agent, comment, other, {Use: "update"}, {Use: "inbox"}, {Use: "show"}, {Use: "wait"}} {
		root.AddCommand(c)
	}
	tests := []struct {
		name string
		cmd  *cobra.Command
		want oplocal.ActivityKind
		ok   bool
	}{
		{"update is a mutation", findCmd(root, "update"), oplocal.ActivityMutation, true},
		{"comment without --to is a mutation", comment, oplocal.ActivityMutation, true},
		{"agent heartbeat is a turn", heartbeat, oplocal.ActivityTurn, true},
		{"inbox (and its wait) records nothing", findCmd(root, "inbox"), "", false},
		{"show is a read", findCmd(root, "show"), "", false},
		{"wait records nothing", findCmd(root, "wait"), "", false},
		{"a top-level heartbeat is not the agent heartbeat", other, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kind, ok := commandActivity(tt.cmd)
			require.Equal(t, tt.ok, ok)
			require.Equal(t, tt.want, kind)
		})
	}
	require.NoError(t, comment.Flags().Set("to", "worker"))
	kind, ok := commandActivity(comment)
	require.True(t, ok)
	require.Equal(t, oplocal.ActivityAddressedSend, kind)
}

func findCmd(root *cobra.Command, name string) *cobra.Command {
	for _, c := range root.Commands() {
		if c.Name() == name {
			return c
		}
	}
	return nil
}

func TestAttachVerbActivity_GlueOnlyForSessionStartAndPrompt(t *testing.T) {
	for verb, want := range map[string]bool{
		attach.VerbSessionStart: true, attach.VerbPromptSubmit: true,
		attach.VerbSessionEnd: false, attach.VerbWait: false, "unknown": false,
	} {
		kind, ok := attachVerbActivity(verb)
		require.Equal(t, want, ok, verb)
		if ok {
			require.Equal(t, oplocal.ActivityGlue, kind)
		}
	}
}

func TestRecordGlueActivity_RecordsForStartAndNothingForWait(t *testing.T) {
	src := hubSourceFixture(t, "")
	t.Chdir(filepath.Dir(src.mtixDir))
	recordGlueActivity(attach.VerbWait)
	_, present, err := src.state.ReadHubActivity(src.key())
	require.NoError(t, err)
	require.False(t, present, "the wait verb must record nothing")
	recordGlueActivity(attach.VerbSessionStart)
	got, present, err := src.state.ReadHubActivity(src.key())
	require.NoError(t, err)
	require.True(t, present)
	require.Equal(t, oplocal.ActivityGlue, got.Kind)
}

func TestActivityDebouncer_RecordsAtMostOncePerInterval(t *testing.T) {
	now := schedT0
	calls := 0
	d := &activityDebouncer{now: func() time.Time { return now }, interval: time.Minute, record: func() { calls++ }}
	d.Trigger()
	d.Trigger()
	now = now.Add(59 * time.Second)
	d.Trigger()
	require.Equal(t, 1, calls)
	now = now.Add(time.Second)
	d.Trigger()
	require.Equal(t, 2, calls)
}

func TestHubContactSource_Snapshot_InvalidListenInterval_FailsSafe(t *testing.T) {
	src := hubSourceFixture(t, "sync:\n  hub_contact: \"activity\"\n  listen_interval: \"0s\"\n")
	_, err := src.snapshot()
	require.ErrorIs(t, err, service.ErrContactInputs)
}

func TestHubContactSource_Snapshot_GrandfatheredKeepsDaemonInterval(t *testing.T) {
	src := hubSourceFixture(t, "prefix: \"X\"\n")
	src.grandfatherInterval = 60 * time.Second
	in, err := src.snapshot()
	require.NoError(t, err)
	require.Equal(t, service.HubContactInterval, in.Mode.Kind)
	require.Equal(t, 60*time.Second, in.Mode.Interval)
}

func TestBuildDaemonPass_ContactFalse_NoPullButLocalWorkRuns(t *testing.T) {
	for _, tt := range []struct {
		name         string
		hub, contact bool
		wantPulls    int
	}{
		{"hub and contact pulls", true, true, 1},
		{"hub, scheduler says no: zero pulls", true, false, 0},
		{"contact but no hub: zero pulls", false, true, 0},
		{"neither", false, false, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var drains, pulls, relays, dispatches int
			pass := buildDaemonPass(tt.hub, daemonPassSteps{
				drain: func() { drains++ }, pull: func() { pulls++ },
				relay: func() { relays++ }, dispatch: func() { dispatches++ },
			})
			pass(tt.contact)
			require.Equal(t, tt.wantPulls, pulls)
			require.Equal(t, []int{1, 1, 1}, []int{drains, relays, dispatches}, "local work runs on every pass")
		})
	}
}

func TestRunScheduledPass_LockNotAcquired_DoesNotMarkContact(t *testing.T) {
	dir, _ := dispatchLockFixture(t)
	lock, err := pushlock.AcquireNamed(dir, "dispatch.lock")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, lock.Release()) })
	sched := fixedScheduler(service.ContactInputs{
		Mode: service.HubContactMode{Kind: service.HubContactActivity}, LastActivity: schedT0})
	contacted, ran := 0, 0
	plan := daemonLoopPlan{sched: sched, pass: func(bool) { ran++ }, onContact: func() { contacted++ }}
	runScheduledPass(context.Background(), io.Discard, plan, daemonPassControl{})
	require.Zero(t, ran, "the contended pass does no work")
	require.Zero(t, contacted, "a pass that did not run must not count as a contact")
	require.True(t, sched.Decide().Contact, "the contact is still due on the next tick")
}

func TestNewDaemonScheduler_NoHub_NilAndHubGetsDaemonInterval(t *testing.T) {
	src := hubSourceFixture(t, "prefix: \"X\"\n")
	saved := app
	app = appContext{mtixDir: src.mtixDir, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	t.Cleanup(func() { app = saved })

	sched, onContact, err := newDaemonScheduler(io.Discard, io.Discard, false, 5)
	require.NoError(t, err)
	require.Nil(t, sched)
	require.Nil(t, onContact)

	var out bytes.Buffer
	sched, onContact, err = newDaemonScheduler(&out, io.Discard, true, 60)
	require.NoError(t, err)
	require.NotNil(t, sched)
	require.NotNil(t, onContact)
	require.Contains(t, out.String(), "interval:1m0s", "grandfathered at the daemon's own --interval")
}
func TestHubContactDoctor_Rows(t *testing.T) {
	now := schedT0.Add(time.Hour)
	src := hubSourceFixture(t, "prefix: \"X\"\n") // legacy: no sync.hub_contact
	rows := hubContactDoctorChecks(src, true, now)
	require.Len(t, rows, 2)
	require.Equal(t, "sync.hub-contact", rows[0].Name)
	require.True(t, rows[0].Pass)
	require.True(t, rows[0].Warn, "grandfathered host with a hub gets a notice")
	require.Equal(t, "mtix config set sync.hub_contact activity", rows[0].Fix)
	require.Equal(t, "sync.listen", rows[1].Name)

	noHub := hubContactDoctorChecks(src, false, now)
	require.False(t, noHub[0].Warn, "no grandfather notice without a hub")
	require.Contains(t, noHub[0].Detail, "no hub configured")
	require.Contains(t, rows[0].Detail, "grandfathered: the daemon's own --interval")

	require.NoError(t, src.state.RecordHubActivity(src.key(), oplocal.HubActivity{At: now.Add(-5 * time.Minute), Kind: oplocal.ActivityMutation}))
	require.NoError(t, src.state.RecordHubContact(src.key(), oplocal.HubContact{At: now.Add(-time.Minute), Route: "poll", OK: true}))
	live, _ := src.startTime(3)
	require.NoError(t, src.state.WriteListenLease(src.key(), oplocal.ListenLease{PID: 3, StartTime: live, ArmedAt: now.Add(-time.Hour)}))
	rows = hubContactDoctorChecks(src, true, now)
	require.Contains(t, rows[0].Detail, "last contact")
	require.Contains(t, rows[0].Detail, "hub contact is scheduled")
	require.Contains(t, rows[1].Detail, "route poll")
	require.Contains(t, rows[1].Detail, "hold ends")
}

func TestHubContactDoctor_ReportsWhenNoContactWillHappen(t *testing.T) {
	now := schedT0.Add(10 * time.Hour)
	src := hubSourceFixture(t, "sync:\n  hub_contact: \"activity\"\n")
	rows := hubContactDoctorChecks(src, true, now)
	require.Contains(t, rows[0].Detail, "no hub contact will happen until new activity")

	manual := hubSourceFixture(t, "sync:\n  hub_contact: \"manual\"\n")
	rows = hubContactDoctorChecks(manual, true, now)
	require.Contains(t, rows[0].Detail, "mode is manual")
}

func TestHubContactDoctor_InvalidKey_FixNamesKeyAndDefault(t *testing.T) {
	tests := []struct{ cfg, key, fix string }{
		{"sync:\n  hub_contact: \"sometimes\"\n", "sync.hub_contact", "mtix config set sync.hub_contact activity"},
		{"sync:\n  listen: \"loud\"\n", "sync.listen", "mtix config set sync.listen " + service.DefaultListenValue()},
		{"sync:\n  listen_interval: \"0s\"\n", "sync.listen_interval", "mtix config set sync.listen_interval 10s"},
		{"sync:\n  listen_max_idle: \"999h\"\n", "sync.listen_max_idle", "mtix config set sync.listen_max_idle 8h"},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			src := hubSourceFixture(t, tt.cfg)
			rows := hubContactDoctorChecks(src, true, schedT0)
			for _, r := range rows {
				require.False(t, r.Pass)
				require.Equal(t, tt.fix, r.Fix)
				require.Contains(t, r.Detail, tt.key)
			}
			require.Contains(t, rows[0].Detail, "no hub contact will happen")
		})
	}
}

func TestHubContactDoctor_ManualWithListen_SaysInactive(t *testing.T) {
	src := hubSourceFixture(t, "sync:\n  hub_contact: \"manual\"\n  listen: \"live\"\n")
	rows := hubContactDoctorChecks(src, true, schedT0)
	require.Contains(t, rows[1].Detail, "inactive")
}

func TestHubContactDoctor_ListenOff_SaysSo(t *testing.T) {
	src := hubSourceFixture(t, "sync:\n  hub_contact: \"activity\"\n  listen: \"off\"\n")
	rows := hubContactDoctorChecks(src, true, schedT0)
	require.True(t, rows[1].Pass)
	require.Contains(t, rows[1].Detail, "listen off")
}

func TestHubContactSource_SymlinkedBoardPath_SharesOneActivityKey(t *testing.T) {
	identityOperatorState(t)
	projDir := filepath.Join(t.TempDir(), "proj")
	require.NoError(t, os.MkdirAll(filepath.Join(projDir, ".mtix"), 0o755))
	link := filepath.Join(t.TempDir(), "alias")
	require.NoError(t, os.Symlink(projDir, link))

	require.NoError(t, recordHubActivityAt(filepath.Join(link, ".mtix"), oplocal.ActivityMutation, func() time.Time { return schedT0 }))

	daemonSide, err := newHubContactSource(filepath.Join(projDir, ".mtix"))
	require.NoError(t, err)
	got, present, err := daemonSide.state.ReadHubActivity(daemonSide.key())
	require.NoError(t, err)
	require.True(t, present, "activity written through a symlinked path must be visible through the real path")
	require.True(t, schedT0.Equal(got.At))
}

func TestNewHubContactSource_MissingBoardDir_ReturnsWrappedError(t *testing.T) {
	identityOperatorState(t)
	_, err := newHubContactSource(filepath.Join(t.TempDir(), "absent", ".mtix"))
	require.ErrorContains(t, err, "resolve board path")
}

func TestHubContactSource_Liveness_ReusedPidLeaseIsNotLive(t *testing.T) {
	src := hubSourceFixture(t, "")
	require.NoError(t, src.state.WriteListenLease(src.key(), oplocal.ListenLease{PID: 4, StartTime: "reused-pid", ArmedAt: schedT0}))
	in, err := src.snapshot()
	require.NoError(t, err)
	require.False(t, in.LiveLease, "same pid with a different start time is a reused pid, not a live listener")
}

func TestHubContactDoctor_UnreadableInputs_FailsWithFix(t *testing.T) {
	src := hubSourceFixture(t, "sync:\n  hub_contact: \"activity\"\n")
	require.NoError(t, src.state.WriteJSON("hubact-"+src.key(), map[string]any{"at": schedT0, "kind": "parked_wait"}))
	rows := hubContactDoctorChecks(src, true, schedT0.Add(time.Minute))
	require.False(t, rows[0].Pass, "the daemon makes no hub contact on unreadable inputs, so the row must not pass")
	require.Contains(t, rows[0].Detail, "inputs unreadable")
	require.NotEmpty(t, rows[0].Fix)
}
