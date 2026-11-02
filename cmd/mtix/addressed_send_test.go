// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// Addressed-send auto-push (ADR-007 D21, MTIX-112.20.2). A fake DSN makes
// the checkout enrolled without any network: only the decision and the
// spawn request are under test here, never a process.
const fakeEnrolledDSN = "postgres://u:p@127.0.0.1:1/db?sslmode=disable"

// spawnSpy records push-child requests instead of starting a process.
type spawnSpy struct {
	mu    sync.Mutex
	calls [][]string
	fail  error
}

func (s *spawnSpy) spawn(_, _ string, argv []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, argv)
	return s.fail
}

func (s *spawnSpy) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func enrolledSpyApp(t *testing.T) *spawnSpy {
	t.Helper()
	t.Setenv(transport.EnvDSN, fakeEnrolledDSN)
	initTestApp(t)
	spy := &spawnSpy{}
	app.detachedSpawn = spy.spawn
	return spy
}

func TestAddressedSend_ManualMode_NoPush(t *testing.T) {
	tests := []struct {
		name, key, value, reason string
	}{
		{"hub contact manual", "sync.hub_contact", "manual", "manual"},
		{"switch false", service.PushOnAddressedSendKey, "false", "false"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spy := enrolledSpyApp(t)
			_, setErr := app.configSvc.Set(tt.key, tt.value)
			require.NoError(t, setErr)
			got := cliAddressedPush(context.Background())
			assert.Equal(t, pushStateOff, got.State)
			assert.Contains(t, got.Reason, tt.reason)
			assert.Zero(t, spy.count(), "an off board must never start a push child")
		})
	}
}

func TestAddressedSend_ListedOnlyBoard_NoPush(t *testing.T) {
	spy := enrolledSpyApp(t)
	state, err := operatorState()
	require.NoError(t, err)
	root, err := filepath.EvalSymlinks(filepath.Dir(app.mtixDir))
	require.NoError(t, err)
	require.NoError(t, state.ListBoardInstance(context.Background(),
		oplocal.ListedBoard{Key: uuid.NewString(), Path: root}))
	got := cliAddressedPush(context.Background())
	assert.Equal(t, pushStateOff, got.State)
	assert.Contains(t, got.Reason, "listed")
	assert.Zero(t, spy.count(), "a listed-only board never pushes from a send (A8)")
}

func TestAddressedSend_EnrolledBoard_RequestsDetachedChild(t *testing.T) {
	spy := enrolledSpyApp(t)
	got := cliAddressedPush(context.Background())
	assert.Equal(t, addressedPush{State: pushStateQueued}, got)
	require.Equal(t, 1, spy.count())
	argv := strings.Join(spy.calls[0], " ")
	assert.Equal(t, "sync push --on-addressed-send", argv)
	assert.NotContains(t, argv, "postgres://", "the child's argv names no credential")
}

func TestAddressedSend_NoHubOrRelay_NoPush(t *testing.T) {
	t.Setenv(transport.EnvDSN, "")
	initTestApp(t)
	spy := &spawnSpy{}
	app.detachedSpawn = spy.spawn
	got := cliAddressedPush(context.Background())
	assert.Equal(t, pushStateOff, got.State)
	assert.Zero(t, spy.count())
}

func TestAddressedSend_SandboxedSpawnRefused_Defers(t *testing.T) {
	spy := enrolledSpyApp(t)
	spy.fail = errors.New("operation not permitted")
	got := cliAddressedPush(context.Background())
	assert.Equal(t, pushStateDeferred, got.State)
	assert.Contains(t, got.Reason, "dispatcher")
}

func TestAddressedSend_UnreadableSettings_FailsClosed(t *testing.T) {
	spawned := 0
	got := requestAddressedPush(addressedPushDeps{
		inputs: func() (addressedPushInputs, error) { return addressedPushInputs{}, errors.New("boom") },
		spawn:  func() error { spawned++; return nil },
	})
	assert.Equal(t, pushStateOff, got.State)
	assert.Zero(t, spawned)
}

func TestDecideAddressedPush_Table(t *testing.T) {
	on := addressedPushInputs{Enabled: true, Enrolled: true,
		Mode: service.HubContactMode{Kind: service.HubContactActivity}}
	interval := on
	interval.Mode = service.HubContactMode{Kind: service.HubContactInterval, Interval: time.Minute}
	manual := on
	manual.Mode.Kind = service.HubContactManual
	listed := on
	listed.ListedOnly = true
	off := on
	off.Enabled = false
	bare := on
	bare.Enrolled = false
	tests := []struct {
		name string
		in   addressedPushInputs
		want string
	}{
		{"activity", on, pushStateQueued},
		{"interval", interval, pushStateQueued},
		{"manual", manual, pushStateOff},
		{"listed only", listed, pushStateOff},
		{"switch off", off, pushStateOff},
		{"not enrolled", bare, pushStateOff},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, decideAddressedPush(tt.in).State)
		})
	}
}

func TestCommentTo_JSONCarriesPushState(t *testing.T) {
	spy := enrolledSpyApp(t)
	id := seedOneNode(t)
	app.jsonOutput = true
	out := captureStdout(t, func() { require.NoError(t, runComment(id, "hello", "agent-b")) })
	var got struct {
		Push addressedPush `json:"push"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &got), out)
	assert.Equal(t, pushStateQueued, got.Push.State)
	assert.Equal(t, 1, spy.count())

	app.jsonOutput = false
	text := captureStdout(t, func() { require.NoError(t, runComment(id, "again", "agent-b")) })
	assert.Contains(t, text, "push: queued for hub push")
}

func TestAddressedSend_ReturnsBeforeNetwork(t *testing.T) {
	release := make(chan struct{})
	var started atomic.Bool
	got := make(chan addressedPush, 1)
	go func() {
		got <- requestAddressedPush(addressedPushDeps{
			inputs: func() (addressedPushInputs, error) {
				return addressedPushInputs{Enabled: true, Enrolled: true, Mode: service.HubContactMode{Kind: service.HubContactActivity}}, nil
			},
			inProcess: (&coalescingRunner{job: func() { started.Store(true); <-release }}).Trigger,
		})
	}()
	select {
	case res := <-got:
		assert.Equal(t, pushStateQueued, res.State)
	case <-time.After(5 * time.Second):
		t.Fatal("the request waited for the push job")
	}
	close(release)
}

func TestCoalescingRunner_BurstNeverRunsTwoAtOnce(t *testing.T) {
	var running, peak, runs int32
	release := make(chan struct{})
	idle := make(chan struct{})
	r := &coalescingRunner{done: func() { close(idle) }}
	r.job = func() {
		n := atomic.AddInt32(&running, 1)
		for {
			p := atomic.LoadInt32(&peak)
			if n <= p || atomic.CompareAndSwapInt32(&peak, p, n) {
				break
			}
		}
		if atomic.AddInt32(&runs, 1) == 1 {
			<-release
		}
		atomic.AddInt32(&running, -1)
	}
	r.Trigger()
	r.Trigger() // arrives while the first job runs: marks it dirty
	r.Trigger() // a burst coalesces into one more run
	close(release)
	select {
	case <-idle:
	case <-time.After(5 * time.Second):
		t.Fatal("runner never went idle")
	}
	assert.Equal(t, int32(1), atomic.LoadInt32(&peak), "two quick sends never push concurrently")
	assert.Equal(t, int32(2), atomic.LoadInt32(&runs), "a trigger during a run is never lost, a burst costs one more run")
}

func TestAddressedSend_PushFailure_PersistedAndShown(t *testing.T) {
	t.Setenv(transport.EnvDSN, fakeEnrolledDSN) // nothing listens there: the push fails
	initTestApp(t)
	var stderr bytes.Buffer
	err := runAddressedSendPush(context.Background(), &stderr, testRun(transport.Options{InsecureTLS: true}))
	require.Error(t, err)

	src, err := newHubContactSource(app.mtixDir)
	require.NoError(t, err)
	rec, present, err := src.state.ReadAddressedPush(src.key())
	require.NoError(t, err)
	require.True(t, present)
	assert.False(t, rec.OK)
	assert.NotEmpty(t, rec.ErrorClass)
	raw, _ := json.Marshal(rec)
	assert.NotContains(t, string(raw), "127.0.0.1", "only a class is kept, never error text")

	row := addressedPushDoctorRow(src)
	assert.False(t, row.Pass)
	assert.Equal(t, fixAddressedPush, row.Fix)
	assert.Contains(t, row.Detail, "still pending")

	var shown bytes.Buffer
	showAddressedPushFailure(&shown, app.mtixDir)
	assert.Contains(t, shown.String(), "automatic push of an addressed send failed")
	assert.Contains(t, shown.String(), "mtix sync push")
	var again bytes.Buffer
	showAddressedPushFailure(&again, app.mtixDir)
	assert.Empty(t, again.String(), "a failure is printed once")
	assert.False(t, addressedPushDoctorRow(src).Pass, "doctor keeps failing until a push succeeds")
}

func TestAddressedPushDoctorRow_States(t *testing.T) {
	initTestApp(t)
	src, err := newHubContactSource(app.mtixDir)
	require.NoError(t, err)
	row := addressedPushDoctorRow(src)
	assert.True(t, row.Pass)
	assert.Equal(t, "sync.push-on-send", row.Name)

	require.NoError(t, src.state.RecordAddressedPush(src.key(),
		oplocal.AddressedPush{At: time.Now().UTC(), OK: true}))
	assert.Contains(t, addressedPushDoctorRow(src).Detail, "succeeded")

	_, setErr := app.configSvc.Set(service.PushOnAddressedSendKey, "false")
	require.NoError(t, setErr)
	assert.Contains(t, addressedPushDoctorRow(src).Detail, "false")
}

func TestAddressedSend_ChildArgvCarriesNoCredential(t *testing.T) {
	for _, a := range childPushArgv() {
		assert.NotContains(t, a, "postgres")
		assert.NotContains(t, a, "dsn")
	}
}

func TestPushErrorClass_Table(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"no dsn", fmt.Errorf("resolve: %w", transport.ErrDSNNotConfigured), "no_hub_configured"},
		{"timeout", fmt.Errorf("connect: %w", context.DeadlineExceeded), "timeout"},
		{"other", errors.New("host db.internal refused"), "push_failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, pushErrorClass(tt.err))
		})
	}
}
