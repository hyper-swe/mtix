// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func permissionRegisterCurrentHost(t *testing.T) *oplocal.State {
	t.Helper()
	initTestApp(t)
	t.Setenv(transport.EnvDSN, "")
	_, err := cliBehaviorOutput(t, func() error {
		return runAgentRegister("permission-fixture", sessionOptions{Harness: "fixture", Session: "fixture-session"})
	})
	require.NoError(t, err)
	env, err := oplocal.FromProcess()
	require.NoError(t, err)
	return oplocal.New(env)
}

func permissionAssertUnconfirmedAlert(t *testing.T, state *oplocal.State) {
	t.Helper()
	alerts, err := service.NewAlerts(state, func() time.Time {
		return time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	}).Recent(context.Background(), 0)
	require.NoError(t, err)
	require.Len(t, alerts, 1, "a newly held implicit local id must raise a real operator alert")
	assert.Equal(t, "permission-fixture", alerts[0].Agent)
	assert.Equal(t, "wake.unconfirmed", alerts[0].Class)
	assert.Equal(t, uint64(1), alerts[0].Count)
	assert.Contains(t, alerts[0].Fix, "mtix agent allow permission-fixture")
}

func TestHubMarkFlip_LocalImplicitID_HeldAndRealAlert(t *testing.T) {
	state := permissionRegisterCurrentHost(t)
	status, err := service.NewHubMarkService(app.store).Inspect(context.Background(), service.HubEvidence{})
	require.NoError(t, err)
	require.False(t, status.Connected)
	var stderr bytes.Buffer
	noteHubConnection(context.Background(), &stderr)
	assert.Empty(t, stderr.String())
	permissionAssertUnconfirmedAlert(t, state)
	noteHubConnection(context.Background(), &stderr)
	permissionAssertUnconfirmedAlert(t, state)
}

func TestHubMarkFlip_ForeignPersistedMark_HeldAndRealAlert(t *testing.T) {
	state := permissionRegisterCurrentHost(t)
	flipped, err := app.store.MarkHubConnected(context.Background(), "foreign_event")
	require.NoError(t, err)
	require.True(t, flipped)
	require.NoError(t, reconcileHubMarkMirror())
	permissionAssertUnconfirmedAlert(t, state)
	require.NoError(t, reconcileHubMarkMirror())
	permissionAssertUnconfirmedAlert(t, state)
}
