// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
)

func TestWireWakeScanner_HostWithoutIdentity_WiresNothing(t *testing.T) {
	initTestApp(t)
	identityOperatorState(t) // a fresh operator state directory: no host identity yet
	app.wakeScanner = nil
	wireWakeScanner(app.mtixDir)
	require.Nil(t, app.wakeScanner, "no host identity means no scanner and no operator state created")
}

func TestWireWakeScanner_HostWithIdentity_WiresScannerIntoHooksDispatcher(t *testing.T) {
	initTestApp(t)
	env, err := oplocal.FromProcess()
	require.NoError(t, err)
	_, err = oplocal.New(env).HostID()
	require.NoError(t, err)
	app.wakeScanner = nil
	wireWakeScanner(app.mtixDir)
	require.NotNil(t, app.wakeScanner)
}

func TestWireWakeScanner_NilDispatcherOrStore_IsNoop(t *testing.T) {
	initTestApp(t)
	saved := app.hooksDisp
	app.hooksDisp = nil
	t.Cleanup(func() { app.hooksDisp = saved })
	app.wakeScanner = nil
	wireWakeScanner(app.mtixDir)
	require.Nil(t, app.wakeScanner)
}

func TestWakeScannerDoctorRow_States_ReportIdleActiveStaleAndNoBoard(t *testing.T) {
	initTestApp(t)
	identityOperatorState(t) // a fresh operator state directory: no host identity yet
	idle := wakeScannerDoctorRow(t.Context())
	require.Equal(t, service.DoctorInfo, idle.Status)
	require.Contains(t, idle.Fix, "mtix agent register")
	require.Equal(t, false, idle.Data["active"])

	env, err := oplocal.FromProcess()
	require.NoError(t, err)
	hostID, err := oplocal.New(env).HostID()
	require.NoError(t, err)
	uid, err := currentUserID()
	require.NoError(t, err)
	active := wakeScannerDoctorRow(t.Context())
	require.Equal(t, service.DoctorInfo, active.Status)
	require.Equal(t, true, active.Data["active"])
	require.Equal(t, 0, active.Data["registrations"])

	_, err = app.store.WriteDB().ExecContext(t.Context(), `INSERT INTO agent_registrations
		(agent_id,harness,uid,host_id,origin,epoch,high_water,pending_hold,registered_at,last_seen,registrar_version,attach_protocol,stale_since)
		VALUES ('gone-agent','codex',?,?,'manual',1,0,0,?,?,'test',1,?)`,
		uid, hostID, time.Now().UTC().Format(time.RFC3339), time.Now().UTC().Format(time.RFC3339), "2026-10-15T12:00:00Z")
	require.NoError(t, err)
	stale := wakeScannerDoctorRow(t.Context())
	require.Equal(t, service.DoctorWarn, stale.Status)
	require.Equal(t, 1, stale.Data["stale"])
	require.Equal(t, "mtix agent unregister gone-agent --force", stale.Fix)
}

func TestWakeScannerDoctorRow_NoStore_ReportsInfo(t *testing.T) {
	saveAndResetApp(t)
	row := wakeScannerDoctorRow(t.Context())
	require.Equal(t, service.DoctorInfo, row.Status)
}
