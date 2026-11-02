// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/sync/pushlock"
)

// A board that is both listed and enrolled on the host still pushes: only a
// listed-only board is refused (ADR-007 A8).
func TestAddressedSend_ListedAndEnrolledBoard_Pushes(t *testing.T) {
	spy := enrolledSpyApp(t)
	state, err := operatorState()
	require.NoError(t, err)
	root, err := filepath.EvalSymlinks(filepath.Dir(app.mtixDir))
	require.NoError(t, err)
	instance, err := app.store.StoreInstanceID(context.Background())
	require.NoError(t, err)
	require.NoError(t, state.ListBoardInstance(context.Background(),
		oplocal.ListedBoard{Key: instance, Path: root, StoreInstanceID: instance}))
	hub := oplocal.HubIdentity{Kind: "postgres", ProjectPrefix: "TEST", FirstEventHash: "observed"}
	entry := &oplocal.EnrolledBoard{Path: root, BoardID: instance, StoreInstanceID: instance, Hub: hub}
	require.NoError(t, state.SetEnrollment(context.Background(), entry,
		oplocal.EnrollmentAudit{Path: root, Action: "enroll", Mode: "yes", Source: "test", At: time.Now().UTC()}))

	got := cliAddressedPush(context.Background())
	assert.Equal(t, pushStateQueued, got.State, got.Reason)
	assert.Equal(t, 1, spy.count())
}

// An invalid sync.hub_contact value fails closed: an operator who meant
// manual and mistyped it gets no push.
func TestAddressedSend_InvalidHubContact_FailsClosed(t *testing.T) {
	spy := enrolledSpyApp(t)
	require.NoError(t, os.WriteFile(filepath.Join(app.mtixDir, "config.yaml"),
		[]byte("prefix: TEST\nsync:\n  hub_contact: mnaual\n"), 0o600))
	svc, err := service.NewConfigService(filepath.Join(app.mtixDir, "config.yaml"))
	require.NoError(t, err)
	app.configSvc = svc
	got := cliAddressedPush(context.Background())
	assert.Equal(t, pushStateOff, got.State)
	assert.Contains(t, got.Reason, "could not read push settings")
	assert.Zero(t, spy.count())
}

// A push lock that stays held is a pending message, never a success: the
// send waits, then records push_held with the push fix.
func TestAddressedSend_LockHeldThroughoutWait_RecordsPushHeld(t *testing.T) {
	t.Setenv(transport.EnvDSN, fakeEnrolledDSN)
	initTestApp(t)
	_ = seedOneNode(t) // a pending event, so the send has something to push
	lock, err := pushlock.Acquire(app.mtixDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = lock.Release() })
	waits := 0
	o := testRun(transport.Options{InsecureTLS: true})
	o.HeldTries = 2
	o.Wait = func(time.Duration) { waits++ }
	err = runAddressedSendPush(context.Background(), &bytes.Buffer{}, o)
	require.ErrorIs(t, err, errPushHeld)
	assert.Equal(t, 2, waits, "the child probes the lock again before giving up")
	ok, class := waitAddressedPushRecord(t)
	assert.False(t, ok)
	assert.Equal(t, "push_held", class)
	src, srcErr := newHubContactSource(app.mtixDir)
	require.NoError(t, srcErr)
	assert.Equal(t, fixAddressedPush, addressedPushDoctorRow(src).Fix)
}

// A holder that releases during the wait lets the child push itself: the
// message the holder's pass budget missed is not left unannounced.
func TestAddressedSend_LockReleasedDuringWait_ChildPushes(t *testing.T) {
	t.Setenv(transport.EnvDSN, fakeEnrolledDSN) // nothing listens: the push itself then fails
	initTestApp(t)
	_ = seedOneNode(t) // a pending event, so the send has something to push
	lock, err := pushlock.Acquire(app.mtixDir)
	require.NoError(t, err)
	o := testRun(transport.Options{InsecureTLS: true})
	o.Wait = func(time.Duration) { _ = lock.Release() }
	err = runAddressedSendPush(context.Background(), &bytes.Buffer{}, o)
	require.Error(t, err)
	assert.NotErrorIs(t, err, errPushHeld, "the second probe took the lock and ran the push")
	_, class := waitAddressedPushRecord(t)
	assert.Equal(t, "push_failed", class)
}

// The printed fix clears the row: a completed push resolves the hub part of
// the failure, a working relay pass resolves the relay part, and a record
// holding both keeps the other one (MTIX-112.20.2).
func TestResolveAddressedPush_ClearsOnlyTheFixedPart(t *testing.T) {
	tests := []struct {
		name      string
		class     string
		relay     bool
		wantOK    bool
		wantClass string
	}{
		{"push fixes hub failure", "push_failed", false, true, ""},
		{"relay pass fixes relay failure", relayFailedClass, true, true, ""},
		{"push leaves relay part", "push_failed," + relayFailedClass, false, false, relayFailedClass},
		{"relay leaves hub part", "push_failed," + relayFailedClass, true, false, "push_failed"},
		{"push does not clear relay failure", relayFailedClass, false, false, relayFailedClass},
		{"push fixes held", "push_held", false, true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			src, err := newHubContactSource(app.mtixDir)
			require.NoError(t, err)
			require.NoError(t, src.state.RecordAddressedPush(src.key(),
				oplocal.AddressedPush{At: fixedPushClock().Add(-time.Hour), ErrorClass: tt.class, Shown: true}))
			require.NoError(t, resolveAddressedPush(app.mtixDir, fixedPushClock, tt.relay))
			rec, present, err := src.state.ReadAddressedPush(src.key())
			require.NoError(t, err)
			require.True(t, present)
			assert.Equal(t, tt.wantOK, rec.OK)
			assert.Equal(t, tt.wantClass, rec.ErrorClass)
			if tt.wantClass != tt.class {
				assert.False(t, rec.Shown, "a changed record is shown again")
			}
			assert.Equal(t, tt.wantOK, addressedPushDoctorRow(src).Pass)
		})
	}
}

// The documented fix path: a failing row turns green after the next
// completed relay pass on a healthy relay.
func TestAddressedPushDoctorRow_RelayFixClearsRow(t *testing.T) {
	t.Setenv(transport.EnvDSN, "")
	initCommandRelay(t, false)
	src, err := newHubContactSource(app.mtixDir)
	require.NoError(t, err)
	require.NoError(t, src.state.RecordAddressedPush(src.key(),
		oplocal.AddressedPush{At: fixedPushClock(), ErrorClass: relayFailedClass}))
	require.False(t, addressedPushDoctorRow(src).Pass)
	require.NoError(t, runRelayPhase(context.Background(), &bytes.Buffer{}))
	assert.True(t, addressedPushDoctorRow(src).Pass)
}

func TestShowAddressedPushFailure_OKRecordPrintsNothing(t *testing.T) {
	initTestApp(t)
	src, err := newHubContactSource(app.mtixDir)
	require.NoError(t, err)
	require.NoError(t, src.state.RecordAddressedPush(src.key(), oplocal.AddressedPush{At: fixedPushClock(), OK: true}))
	var out bytes.Buffer
	showAddressedPushFailure(&out, app.mtixDir)
	assert.Empty(t, out.String())
}

func TestPushStatusLine_Table(t *testing.T) {
	tests := []struct {
		in   addressedPush
		want string
	}{
		{addressedPush{State: pushStateQueued}, "queued for hub push"},
		{addressedPush{State: pushStateDeferred, Reason: "r"}, "deferred (r)"},
		{addressedPush{State: pushStateOff, Reason: "r"}, "off (r)"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, pushStatusLine(tt.in))
	}
}

// The failure text says what turns the row green, per class.
func TestAddressedPushFailureText_NamesFixAndClearing(t *testing.T) {
	tests := []struct {
		class, wantFix, wantClears string
	}{
		{"push_failed", fixAddressedPush, "clears when a push completes"},
		{relayFailedClass, fixAddressedRelay, "next successful relay pass"},
		{"push_incomplete", fixAddressedIncomplete, "clears when a push completes"},
	}
	for _, tt := range tests {
		t.Run(tt.class, func(t *testing.T) {
			rec := oplocal.AddressedPush{At: fixedPushClock(), ErrorClass: tt.class}
			text := addressedPushFailureText(rec)
			assert.Contains(t, text, tt.wantFix)
			assert.Contains(t, text, tt.wantClears)
			assert.Equal(t, tt.wantFix, addressedPushFix(rec))
		})
	}
}
