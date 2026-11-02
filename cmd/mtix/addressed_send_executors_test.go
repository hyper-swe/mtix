// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
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
	_ = seedOneNode(t) // a pending event, so the send has something to push
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
// of a hub push; with no hub and a genuinely initialised relay the outcome is
// a success record.
func TestAddressedSend_RelayBoard_RequestsPublish(t *testing.T) {
	t.Setenv(transport.EnvDSN, "")
	initCommandRelay(t, false)
	spy := &spawnSpy{}
	app.detachedSpawn = spy.spawn
	got := cliAddressedPush(context.Background())
	assert.Equal(t, pushStateQueued, got.State, "a relay board is enrolled")
	require.Equal(t, 1, spy.count())

	var stderr bytes.Buffer
	require.NoError(t, runAddressedSendPush(context.Background(), &stderr, testRun(transport.Options{})))
	ok, class := waitAddressedPushRecord(t)
	assert.True(t, ok, class+" "+stderr.String())
}

// testRun is a run configuration with a fixed clock and no real waiting.
func testRun(o transport.Options) addressedRunOptions {
	return addressedRunOptions{Transport: o, Now: fixedPushClock, HeldTries: 3, Wait: func(time.Duration) {}}
}

func fixedPushClock() time.Time { return time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC) }

// brokenRelayBoard configures a relay directory that does not exist, so the
// relay stack cannot open.
func brokenRelayBoard(t *testing.T) {
	t.Helper()
	initTestApp(t)
	_, err := app.configSvc.Set("sync.relay.dir", filepath.Join(t.TempDir(), "no-such-relay"))
	require.NoError(t, err)
}

// A relay that cannot open or publish is a failed send, never a success
// record: it is persisted with the relay class, shown once, and doctor names
// the fix (ADR-007 D21, MTIX-112.20.2).
func TestAddressedSend_RelayBoard_PassFailure_PersistedAndShown(t *testing.T) {
	t.Setenv(transport.EnvDSN, "")
	brokenRelayBoard(t)
	var stderr bytes.Buffer
	err := runAddressedSendPush(context.Background(), &stderr, testRun(transport.Options{}))
	require.Error(t, err)
	require.ErrorIs(t, err, errRelayPass)
	ok, class := waitAddressedPushRecord(t)
	assert.False(t, ok)
	assert.Equal(t, relayFailedClass, class)

	src, srcErr := newHubContactSource(app.mtixDir)
	require.NoError(t, srcErr)
	row := addressedPushDoctorRow(src)
	assert.False(t, row.Pass)
	assert.Equal(t, fixAddressedRelay, row.Fix)
	var shown bytes.Buffer
	showAddressedPushFailure(&shown, app.mtixDir)
	assert.Contains(t, shown.String(), "relay_failed")
	assert.Contains(t, shown.String(), fixAddressedRelay)
}

// Each half of a board with both a hub and a relay reports its own failure,
// and neither hides the other.
func TestAddressedSend_HubAndRelay_FailuresNeverMask(t *testing.T) {
	tests := []struct {
		name      string
		brokenRel bool
		want      string
	}{
		{"hub fails, relay fine", false, "push_failed"},
		{"hub fails, relay fails", true, "push_failed," + relayFailedClass},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(transport.EnvDSN, fakeEnrolledDSN) // nothing listens there
			if tt.brokenRel {
				brokenRelayBoard(t)
			} else {
				initCommandRelay(t, false)
			}
			_ = seedOneNode(t) // a pending event for the hub push to carry
			err := runAddressedSendPush(context.Background(), &bytes.Buffer{},
				testRun(transport.Options{InsecureTLS: true}))
			require.Error(t, err)
			ok, class := waitAddressedPushRecord(t)
			assert.False(t, ok)
			assert.Equal(t, tt.want, class)
		})
	}
}

func TestAddressedPushClass_Table(t *testing.T) {
	hub, rel := errors.New("hub"), errors.New("relay")
	tests := []struct {
		name     string
		hub, rel error
		want     string
	}{
		{"none", nil, nil, ""},
		{"hub only", hub, nil, "push_failed"},
		{"relay only", nil, rel, relayFailedClass},
		{"both", hub, rel, "push_failed," + relayFailedClass},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, addressedPushClass(tt.hub, tt.rel))
		})
	}
}

// relayFileCount counts the files under a relay directory.
func relayFileCount(t *testing.T, dir string) int {
	t.Helper()
	n := 0
	require.NoError(t, filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		return err
	}))
	return n
}

// The relay pass of a send really publishes the pending events: the relay
// holds more files afterwards than before.
func TestAddressedSend_RelayBoard_PublishesPendingEvents(t *testing.T) {
	t.Setenv(transport.EnvDSN, "")
	dir := initCommandRelay(t, false)
	_ = seedOneNode(t) // a pending event the pass must frame onto the medium
	before := relayFileCount(t, dir)
	require.NoError(t, runAddressedSendPush(context.Background(), &bytes.Buffer{}, testRun(transport.Options{})))
	assert.Greater(t, relayFileCount(t, dir), before, "the send published nothing to the relay")
}
