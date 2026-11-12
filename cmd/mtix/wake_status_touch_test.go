// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// A sender's status read must not look like the recipient reading its inbox: it must not
// refresh the recipient registration's last_seen (MTIX-112.40.12, ADR-007 section 3.3).
package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/procinfo"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func registrationLastSeen(t *testing.T, agent string) string {
	t.Helper()
	host, uid := seededWakeHost(t)
	regs, err := app.store.ListRegistrations(t.Context(), host, uid)
	require.NoError(t, err)
	for _, r := range regs {
		if r.AgentID == agent {
			return r.LastSeen
		}
	}
	t.Fatalf("no registration for %s", agent)
	return ""
}

func TestPendingWakeEvents_SessionTouchInContext_DoesNotRefreshLastSeen(t *testing.T) {
	wakeStatusApp(t)
	host, uid := seededWakeHost(t)
	const seeded = "2026-10-29T00:00:00Z"
	_, err := app.store.RegisterSessionAtomic(t.Context(), sqlite.RegistrationKey{AgentID: "worker", HostID: host, UID: uid},
		func(_ *sqlite.Registration, _ int64) (*sqlite.Registration, error) {
			return &sqlite.Registration{AgentID: "worker", HostID: host, UID: uid, Harness: procinfo.HarnessClaude, SessionHandle: "sess-1", Origin: "test",
				RegisteredAt: seeded, LastSeen: seeded, RegistrarVersion: "test", AttachProtocol: 1}, nil
		})
	require.NoError(t, err)
	ctx := sqlite.WithSessionTouch(t.Context(), sqlite.SessionTouch{HostID: host, UID: uid, Handle: "sess-1"})

	_, err = pendingWakeEvents(ctx, "worker")
	require.NoError(t, err)
	require.Equal(t, seeded, registrationLastSeen(t, "worker"), "a status read leaves the recipient's liveness alone")

	_, err = app.store.InboxList(ctx, "worker")
	require.NoError(t, err)
	require.NotEqual(t, seeded, registrationLastSeen(t, "worker"), "control: an ordinary inbox read does refresh it")
}

func TestHookRouteRegistration_ReflectsHarness(t *testing.T) {
	wakeStatusApp(t)
	seedWakeRegistration(t, "worker") // harness claude
	hook, err := hookRouteRegistration(t.Context(), "worker")
	require.NoError(t, err)
	require.True(t, hook)
	hook, err = hookRouteRegistration(t.Context(), "nobody")
	require.NoError(t, err)
	require.False(t, hook)
}

// A registration made through the real register path (a Claude Code session found by the
// ancestor walk) stores the production harness name and must be recognised as hook route
// (MTIX-112.40.12): the gate cannot compare against a literal no real registration carries.
func TestHookRouteRegistration_RegisteredThroughRealPath_IsRecognised(t *testing.T) {
	wakeStatusApp(t)
	t.Setenv("CLAUDE_CODE_MESSAGING_SOCKET", "/tmp/lane-hook")
	fakeSessionProcess(t, "claude")
	captureStdout(t, func() { require.NoError(t, runAgentRegister("lane", sessionOptions{})) })
	hook, err := hookRouteRegistration(t.Context(), "lane")
	require.NoError(t, err)
	require.True(t, hook, "a registration created by agent register is on the hook route")
}
