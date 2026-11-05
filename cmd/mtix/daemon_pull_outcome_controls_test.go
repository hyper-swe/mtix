// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

func TestBuildDaemonPass_FailedPull_LocalPhasesKeepOrder(t *testing.T) {
	var phases []string
	step := func(name string) func() { return func() { phases = append(phases, name) } }
	pass := buildDaemonPass(true, daemonPassSteps{
		drain: step("drain"), pull: func() daemonPullOutcome {
			phases = append(phases, "pull")
			return failedDaemonPull(errors.New("failure"))
		},
		scan: step("scan"), relay: step("relay"), dispatch: step("dispatch"),
	})
	result := pass(true)
	require.True(t, result.attempted)
	require.False(t, result.ok)
	require.Equal(t, []string{"drain", "pull", "scan", "relay", "dispatch"}, phases)
	phases = nil
	result = pass(false)
	require.False(t, result.attempted)
	require.Equal(t, []string{"drain", "relay", "dispatch"}, phases)
}

func TestRunScheduledPass_CancelledOrFailed_NoFabricatedContactOrBusyLoop(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "failed", true: "cancelled"}[cancelled], func(t *testing.T) {
			schedFixture(t)
			sched := fixedScheduler(service.ContactInputs{Mode: service.HubContactMode{Kind: service.HubContactActivity}, LastActivity: schedT0})
			calls, records := 0, 0
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if cancelled {
				cancel()
			}
			plan := daemonLoopPlan{sched: sched, pass: func(bool) daemonPullOutcome { calls++; return failedDaemonPull(errors.New("failure")) },
				onContact: func(o daemonPullOutcome) { records++; require.False(t, o.ok) }}
			runScheduledPass(ctx, io.Discard, plan, daemonPassControl{})
			if cancelled {
				require.Zero(t, calls)
				require.Zero(t, records)
				require.True(t, sched.Decide().Contact)
			} else {
				require.Equal(t, 1, calls)
				require.Equal(t, 1, records)
				require.False(t, sched.Decide().Contact, "failed attempted contact keeps existing cadence")
			}
		})
	}
}

func TestDaemonPullErrorClass_TypedCauses_OnlySafeLabels(t *testing.T) {
	for _, tc := range []struct {
		err   error
		class string
	}{
		{context.DeadlineExceeded, "timeout"}, {&net.DNSError{Err: "private-host", IsTimeout: true}, "timeout"},
		{&net.DNSError{Err: "private-host"}, "unreachable"}, {transport.ErrTLSWeakWithoutFlag, "refused"},
		{transport.ErrTLSWeakNonLoopback, "refused"}, {transport.ErrTLSUnverified, "refused"},
		{x509.UnknownAuthorityError{}, "refused"}, {x509.HostnameError{}, "refused"}, {x509.CertificateInvalidError{}, "refused"},
		{errors.New("postgres://private:secret@private.invalid/db"), "pull_failed"},
	} {
		require.Equal(t, tc.class, daemonPullErrorClass(tc.err))
	}
}

func TestRunOneDaemonPull_CancelledContext_ReportsFailure(t *testing.T) {
	_ = daemonOutcomeFixture(t)
	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	result := runOneDaemonPull(ctx, io.Discard, nil, transport.Options{})
	require.True(t, result.attempted)
	require.False(t, result.ok)
	require.Equal(t, "timeout", result.class)
}
