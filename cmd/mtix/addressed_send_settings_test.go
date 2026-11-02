// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/mcp"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/relay/tick"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// setBoardConfig changes a setting the way another process (mtix config set)
// would: through a fresh config service on the board's config file, leaving
// the running process's own snapshot untouched.
func setBoardConfig(t *testing.T, key, value string) {
	t.Helper()
	other, err := service.NewConfigService(filepath.Join(app.mtixDir, "config.yaml"))
	require.NoError(t, err)
	_, err = other.Set(key, value)
	require.NoError(t, err)
}

// The long-lived executors read the settings at each send: a later switch to
// manual (or the key set to false) from another process turns the next send
// off, with no restart (ADR-007 D21, MTIX-112.20.2).
func TestAddressedSend_SettingChangedByAnotherProcess_NextSendOff(t *testing.T) {
	tests := []struct {
		name, key, value string
	}{
		{"hub_contact manual", "sync.hub_contact", "manual"},
		{"switch false", service.PushOnAddressedSendKey, "false"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(transport.EnvDSN, fakeEnrolledDSN) // nothing listens there
			initTestApp(t)
			_ = seedOneNode(t)
			hook := inProcessAddressedSendHook()
			assert.Equal(t, pushStateQueued, hook(context.Background(), "agent-b"))
			waitAddressedPushRecord(t) // the first push has finished before the setting changes

			setBoardConfig(t, tt.key, tt.value)
			got := hook(context.Background(), "agent-b")
			assert.True(t, strings.HasPrefix(got, pushStateOff), got)
		})
	}
}

// An in-process push that reaches its deadline records a timeout; a cancelled
// one (shutdown) records nothing.
func TestRunAddressedSendPush_DeadlineRecordsTimeout_CancelRecordsNothing(t *testing.T) {
	t.Run("deadline", func(t *testing.T) {
		t.Setenv(transport.EnvDSN, fakeEnrolledDSN)
		initTestApp(t)
		_ = seedOneNode(t)
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		err := runAddressedSendPush(ctx, io.Discard, testRun(transport.Options{InsecureTLS: true}))
		require.Error(t, err)
		ok, class := waitAddressedPushRecord(t)
		assert.False(t, ok)
		assert.Equal(t, "timeout", class)
	})
	t.Run("cancel", func(t *testing.T) {
		t.Setenv(transport.EnvDSN, fakeEnrolledDSN)
		initTestApp(t)
		_ = seedOneNode(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_ = runAddressedSendPush(ctx, io.Discard, testRun(transport.Options{InsecureTLS: true}))
		src, err := newHubContactSource(app.mtixDir)
		require.NoError(t, err)
		_, present, err := src.state.ReadAddressedPush(src.key())
		require.NoError(t, err)
		assert.False(t, present, "an interrupted run records nothing")
	})
}

type failingPassStack struct{}

func (failingPassStack) Pass(context.Context) (tick.Result, error) {
	return tick.Result{}, errors.New("outbox not writable")
}

// A relay that opens but whose pass fails is a failed send, not a success.
func TestAddressedSend_RelayPassErrorAfterOpen_RecordsRelayFailed(t *testing.T) {
	t.Setenv(transport.EnvDSN, "")
	initCommandRelay(t, false)
	app.relayOpen = func() (relayStack, error) { return failingPassStack{}, nil }
	err := runAddressedSendPush(context.Background(), io.Discard, testRun(transport.Options{}))
	require.ErrorIs(t, err, errRelayPass)
	ok, class := waitAddressedPushRecord(t)
	assert.False(t, ok)
	assert.Equal(t, relayFailedClass, class)
	src, srcErr := newHubContactSource(app.mtixDir)
	require.NoError(t, srcErr)
	assert.False(t, addressedPushDoctorRow(src).Pass)
}

// fakePush stands in for one push; calls records how often it ran.
type fakePush struct {
	calls     int
	held      bool
	undrained bool
}

func (f *fakePush) run(ctx context.Context, _ io.Writer, _ transport.Options) error {
	f.calls++
	if probe, ok := ctx.Value(pushProbeKey{}).(*pushProbe); ok {
		probe.held.Store(f.held)
		probe.undrained.Store(f.undrained)
	}
	return nil
}

// A push that ran but did not drain the queue is not a success.
func TestAddressedSend_UndrainedPush_RecordsIncompleteAndKeepsFailure(t *testing.T) {
	t.Setenv(transport.EnvDSN, fakeEnrolledDSN)
	initTestApp(t)
	_ = seedOneNode(t)
	f := &fakePush{undrained: true}
	o := testRun(transport.Options{})
	o.Push = f.run
	err := runAddressedSendPush(context.Background(), io.Discard, o)
	require.ErrorIs(t, err, errPushIncomplete)
	ok, class := waitAddressedPushRecord(t)
	assert.False(t, ok)
	assert.Equal(t, "push_incomplete", class)
	src, srcErr := newHubContactSource(app.mtixDir)
	require.NoError(t, srcErr)
	assert.Equal(t, fixAddressedIncomplete, addressedPushDoctorRow(src).Fix)
}

// afterPushPass clears a recorded failure only when the pass drained.
func TestAfterPushPass_ClearsOnlyWhenDrained(t *testing.T) {
	tests := []struct {
		name    string
		drained bool
		err     error
		wantOK  bool
		// wantUndrained: the pass ended without error and without draining.
		wantUndrained bool
	}{
		{"drained", true, nil, true, false},
		{"undrained", false, nil, false, true},
		{"failed", true, errors.New("boom"), false, false},
		{"failed and undrained", false, errors.New("boom"), false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			src, err := newHubContactSource(app.mtixDir)
			require.NoError(t, err)
			require.NoError(t, src.state.RecordAddressedPush(src.key(),
				oplocal.AddressedPush{At: fixedPushClock(), ErrorClass: "push_failed"}))
			probe := &pushProbe{}
			ctx := context.WithValue(context.Background(), pushProbeKey{}, probe)
			afterPushPass(ctx, io.Discard, tt.drained, tt.err)
			rec, _, err := src.state.ReadAddressedPush(src.key())
			require.NoError(t, err)
			assert.Equal(t, tt.wantOK, rec.OK)
			assert.Equal(t, tt.wantUndrained, probe.undrained.Load(), "the probe tells the send whether the pass drained")
		})
	}
}

// Nothing pending (an earlier push already sent it): no push, no hub contact.
func TestAddressedSend_NothingPending_NoPushNoHubContact(t *testing.T) {
	t.Setenv(transport.EnvDSN, fakeEnrolledDSN)
	initTestApp(t)
	f := &fakePush{}
	o := testRun(transport.Options{})
	o.Push = f.run
	require.NoError(t, runAddressedSendPush(context.Background(), io.Discard, o))
	assert.Zero(t, f.calls)
	ok, _ := waitAddressedPushRecord(t)
	assert.True(t, ok)
}

// Wiring: the MCP server, mtix serve and the next command each carry the
// feature; removing any one of them must fail a test.
func TestAddressedSend_MCPWiring_AnnotateToRequestsPush(t *testing.T) {
	t.Setenv(transport.EnvDSN, fakeEnrolledDSN)
	initTestApp(t)
	id := seedOneNode(t)
	reg := mcp.NewToolRegistry()
	registerMCPTools(reg)
	res, err := reg.Call(context.Background(), "mtix_annotate",
		[]byte(`{"id":"`+id+`","text":"ping","author":"a","to":"agent-b"}`))
	require.NoError(t, err)
	assert.Contains(t, res.Content[0].Text, "push: queued")
	waitAddressedPushRecord(t)
}

func TestAddressedSend_ServeWiring_CommentToRequestsPush(t *testing.T) {
	t.Setenv(transport.EnvDSN, fakeEnrolledDSN)
	initTestApp(t)
	id := seedOneNode(t)
	srv := newServeServer(serveServices(), serveConfig("127.0.0.1", 0), func() time.Time { return fixedPushClock() })
	req := httptest.NewRequest(http.MethodPost, "/api/v1/nodes/"+id+"/comment",
		strings.NewReader(`{"text":"ping","to":"agent-b"}`))
	req.Host = "127.0.0.1:8377"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Requested-With", "mtix")
	w := httptest.NewRecorder()
	srv.Router().ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"push":"queued"`)
	waitAddressedPushRecord(t)
}

func TestRootPostRun_ShowsRecordedPushFailureOnce(t *testing.T) {
	initTestApp(t)
	src, err := newHubContactSource(app.mtixDir)
	require.NoError(t, err)
	require.NoError(t, src.state.RecordAddressedPush(src.key(),
		oplocal.AddressedPush{At: fixedPushClock(), ErrorClass: "push_failed"}))
	var first bytes.Buffer
	cmd := &cobra.Command{Use: "list"}
	cmd.SetErr(&first)
	require.NoError(t, rootPostRun(cmd, nil))
	assert.Contains(t, first.String(), "automatic push of an addressed send failed")
}
