// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// waitAddressedPushRecord polls the operator-local record until the push job
// has written it, so a test never ends under a running push goroutine.
func waitAddressedPushRecord(t *testing.T) (ok bool, class string) {
	t.Helper()
	src, err := newHubContactSource(app.mtixDir)
	require.NoError(t, err)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		rec, present, readErr := src.state.ReadAddressedPush(src.key())
		require.NoError(t, readErr)
		if present {
			return rec.OK, rec.ErrorClass
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the push job never recorded an outcome")
	return false, ""
}

// The MCP server pushes in its own process, returns first, and records the
// outcome (ADR-007 D21 executor order, first step).
func TestAddressedSend_MCPExecutor_PushesInProcessAndRecords(t *testing.T) {
	t.Setenv(transport.EnvDSN, fakeEnrolledDSN) // nothing listens there: the push fails
	initTestApp(t)
	hook := mcpAddressedSendHook()
	got := hook(context.Background(), "agent-b")
	assert.Equal(t, pushStateQueued, got)
	ok, class := waitAddressedPushRecord(t)
	assert.False(t, ok)
	assert.NotEmpty(t, class)
}

func TestAddressedSend_MCPExecutor_OffBoardReportsReason(t *testing.T) {
	t.Setenv(transport.EnvDSN, "")
	initTestApp(t)
	got := mcpAddressedSendHook()(context.Background(), "agent-b")
	assert.Contains(t, got, pushStateOff)
	assert.Contains(t, got, "no hub or relay")
}

// A relay board counts as enrolled and its child runs the relay pass instead
// of a hub push; with no hub the outcome is a success record.
func TestAddressedSend_RelayBoard_RequestsPublish(t *testing.T) {
	t.Setenv(transport.EnvDSN, "")
	initTestApp(t)
	_, err := app.configSvc.Set("sync.relay.dir", t.TempDir())
	require.NoError(t, err)
	spy := &spawnSpy{}
	app.detachedSpawn = spy.spawn
	got := cliAddressedPush(context.Background())
	assert.Equal(t, pushStateQueued, got.State, "a relay board is enrolled")
	require.Equal(t, 1, spy.count())

	var stderr bytes.Buffer
	require.NoError(t, runAddressedSendPush(context.Background(), &stderr,
		transport.Options{}, func() time.Time { return time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC) }))
	ok, class := waitAddressedPushRecord(t)
	assert.True(t, ok, class)
}
