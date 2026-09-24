// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestDoctorHubTriggers_MissingOrDisabled_NamesEachAndTheExactFix: the
// doctor's hub-triggers check compares the hub with the function and
// trigger sets the migrations define. A missing trigger and a disabled
// one (tgenabled other than 'O') are named with the fix: mtix sync init
// for what is missing, and the ALTER TABLE statement for what is
// disabled. It is a WARN by default and a FAIL in strict mode; once the
// fix is applied it passes (MTIX-95.7).
func TestDoctorHubTriggers_MissingOrDisabled_NamesEachAndTheExactFix(t *testing.T) {
	dsn := requireCmdPG(t)
	_ = openCmdHub(t)
	initTestApp(t)
	ctx := context.Background()
	pool := hubPool(t, dsn)

	report, err := runDoctorReport(t)
	require.NoError(t, err)
	pass, warn, detail, fix := doctorCheckNamed(t, report, "hub-triggers")
	require.True(t, pass, detail)
	require.False(t, warn, "a freshly migrated hub has every function and trigger: %s", detail)
	require.Empty(t, fix)

	for _, stmt := range []string{
		`ALTER TABLE audit_log DISABLE TRIGGER audit_log_no_update`,
		`ALTER TABLE sync_events ENABLE ALWAYS TRIGGER sync_events_no_truncate`,
		`DROP TRIGGER sync_conflicts_no_delete ON sync_conflicts`,
	} {
		_, err := pool.Exec(ctx, stmt)
		require.NoError(t, err, stmt)
	}

	report, err = runDoctorReport(t)
	require.NoError(t, err, "a WARN keeps the doctor's exit code 0")
	pass, warn, detail, fix = doctorCheckNamed(t, report, "hub-triggers")
	require.True(t, pass)
	require.True(t, warn)
	require.Contains(t, detail, "missing triggers: sync_conflicts_no_delete on public.sync_conflicts")
	require.Contains(t, detail, "audit_log_no_update on public.audit_log (tgenabled D)")
	require.Contains(t, detail, "sync_events_no_truncate on public.sync_events (tgenabled A)")
	require.Contains(t, fix, "mtix sync init")
	require.Contains(t, fix, "ALTER TABLE public.audit_log ENABLE TRIGGER audit_log_no_update;")
	require.Contains(t, fix, "ALTER TABLE public.sync_events ENABLE TRIGGER sync_events_no_truncate;")

	setKeepRoles(t, "mtix_team")
	report, err = runDoctorReport(t)
	require.ErrorIs(t, err, errDoctorChecksFailed)
	pass, warn, _, _ = doctorCheckNamed(t, report, "hub-triggers")
	require.False(t, pass, "strict mode fails the check")
	require.False(t, warn)
	setKeepRoles(t, "")

	var stdout, stderr bytes.Buffer
	require.NoError(t, runSyncInit(ctx, &stdout, &stderr, nil, cloudOpts), stderr.String())
	for _, stmt := range []string{
		`ALTER TABLE public.audit_log ENABLE TRIGGER audit_log_no_update;`,
		`ALTER TABLE public.sync_events ENABLE TRIGGER sync_events_no_truncate;`,
	} {
		_, err := pool.Exec(ctx, stmt)
		require.NoError(t, err, stmt)
	}
	report, err = runDoctorReport(t)
	require.NoError(t, err)
	pass, warn, detail, _ = doctorCheckNamed(t, report, "hub-triggers")
	require.True(t, pass, detail)
	require.False(t, warn, "the printed fix restores every trigger: %s", detail)
}
