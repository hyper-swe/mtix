// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
)

var schedT0 = time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)

func schedFixture(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	_ = newDaemonTestStore(t, dir)
	saved := app
	app = appContext{mtixDir: dir, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	t.Cleanup(func() { app = saved })
}

// driveSchedulerPasses runs n passes through the real pass loop with a
// controlled cadence and returns the contact flag each pass received.
func driveSchedulerPasses(t *testing.T, sched *service.ContactScheduler, n int) []bool {
	t.Helper()
	schedFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticks := make(chan time.Time)
	seen := make(chan bool, n+1)
	var contacts []bool
	plan := daemonLoopPlan{
		label: "test daemon", interval: 5, sched: sched,
		pass:     func(contact bool) { seen <- contact },
		controls: []daemonPassControl{{ticks: ticks}},
	}
	done := make(chan error, 1)
	go func() { done <- runDaemonPassLoop(ctx, io.Discard, io.Discard, plan) }()
	for i := 0; i < n; i++ {
		if i > 0 {
			ticks <- time.Time{}
		}
		contacts = append(contacts, <-seen)
	}
	cancel()
	require.NoError(t, <-done)
	return contacts
}

func fixedScheduler(in service.ContactInputs) *service.ContactScheduler {
	return service.NewContactScheduler(func() time.Time { return schedT0 },
		func() (service.ContactInputs, error) { return in, nil })
}

func TestDaemonPassLoop_ManualMode_NeverContactsHub(t *testing.T) {
	sched := fixedScheduler(service.ContactInputs{
		Mode: service.HubContactMode{Kind: service.HubContactManual}, LastActivity: schedT0})
	for i, c := range driveSchedulerPasses(t, sched, 4) {
		require.False(t, c, "pass %d", i)
	}
}

func TestDaemonPassLoop_ActivityMode_ContactsOnlyWhenScheduled(t *testing.T) {
	// Fake clock never advances, so after the first contact the 5s cadence is
	// not due again: exactly one contact, every pass still runs local work.
	sched := fixedScheduler(service.ContactInputs{
		Mode: service.HubContactMode{Kind: service.HubContactActivity}, LastActivity: schedT0})
	got := driveSchedulerPasses(t, sched, 4)
	require.Equal(t, []bool{true, false, false, false}, got)
}

func TestDaemonPassLoop_NoScheduler_ContactsEveryPass(t *testing.T) {
	for _, c := range driveSchedulerPasses(t, nil, 3) {
		require.True(t, c, "the legacy sync daemon keeps its fixed cadence")
	}
}

func TestDaemonPassLoop_SchedulerTimer_TakesCadenceFromScheduler(t *testing.T) {
	schedFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sched := fixedScheduler(service.ContactInputs{
		Mode: service.HubContactMode{Kind: service.HubContactActivity}, LastActivity: schedT0})
	passes := make(chan bool, 8)
	plan := daemonLoopPlan{label: "t", interval: 3600, sched: sched, localTick: 20 * time.Millisecond,
		pass: func(c bool) { passes <- c }}
	done := make(chan error, 1)
	go func() { done <- runDaemonPassLoop(ctx, io.Discard, io.Discard, plan) }()
	for i := 0; i < 3; i++ {
		select {
		case <-passes:
		case <-time.After(5 * time.Second):
			t.Fatal("scheduler timer did not drive passes")
		}
	}
	cancel()
	require.NoError(t, <-done)
}

func TestDaemonContactWarning_IntervalPrintsOnce(t *testing.T) {
	var out bytes.Buffer
	w := service.NewCostWarner()
	m := service.HubContactMode{Kind: service.HubContactInterval, Interval: 5 * time.Second}
	announceHubContact(&out, w, m, false)
	announceHubContact(&out, w, m, false)
	require.Equal(t, 1, bytes.Count(out.Bytes(), []byte("times an hour")))
}

func TestDaemonContactAnnounce_Grandfathered_NamesSwitchCommand(t *testing.T) {
	var out bytes.Buffer
	m := service.HubContactMode{Kind: service.HubContactInterval, Interval: 5 * time.Second}
	announceHubContact(&out, service.NewCostWarner(), m, true)
	require.Contains(t, out.String(), "mtix config set sync.hub_contact activity")
	require.Contains(t, out.String(), "grandfathered")
}

func TestDaemonPassLoop_LongLocalTick_SchedulerDelayShortensTimer(t *testing.T) {
	schedFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in := service.ContactInputs{Mode: service.HubContactMode{Kind: service.HubContactInterval, Interval: 150 * time.Millisecond}}
	sched := service.NewContactScheduler(func() time.Time { return time.Now().UTC() },
		func() (service.ContactInputs, error) { return in, nil })
	passes := make(chan bool, 8)
	plan := daemonLoopPlan{label: "t", interval: 3600, sched: sched, localTick: time.Hour,
		pass: func(c bool) { passes <- c }}
	done := make(chan error, 1)
	go func() { done <- runDaemonPassLoop(ctx, io.Discard, io.Discard, plan) }()
	for i := 0; i < 3; i++ {
		select {
		case <-passes:
		case <-time.After(5 * time.Second):
			t.Fatal("the loop ignored the scheduler delay and waited for the one-hour local tick")
		}
	}
	cancel()
	require.NoError(t, <-done)
}
