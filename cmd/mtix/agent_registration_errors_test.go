// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

// Failure paths of session registration: unusable operator state, no project, corrupt records.

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
)

func breakOperatorDirectory(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", "relative/path")
	t.Setenv("APPDATA", "relative")
}

func TestRegistrationCommands_UnusableOperatorDirectory_ReturnError(t *testing.T) {
	initRegistrationApp(t)
	breakOperatorDirectory(t)
	require.Error(t, runAgentUnregister("lane", true))
	require.Error(t, runAgentListRegistrations())
	_, err := newRegistrationService()
	require.Error(t, err)
	_, err = newOperatorTakeOverLog()
	require.NoError(t, err, "the log is built lazily; reading it fails")
	log, _ := newOperatorTakeOverLog()
	_, err = log.Recent(context.Background(), "a", time.Now())
	require.Error(t, err)
	require.Error(t, log.Append(context.Background(), service.TakeOverRecord{AgentID: "a", At: time.Now()}))

	var report DoctorReport
	report = appendRegistrationChecks(context.Background(), report)
	require.Len(t, report.Checks, 1)
	assert.True(t, report.Checks[0].Warn)
	assert.Contains(t, report.Checks[0].Detail, "not checked")
}

func TestRegistrationCommands_NoHomeDirectory_ReturnError(t *testing.T) {
	initRegistrationApp(t)
	t.Setenv("HOME", "")
	t.Setenv("CODEX_THREAD_ID", "thread-A")
	_, err := newRegistrationService()
	require.Error(t, err)
	_, err = newOperatorTakeOverLog()
	require.Error(t, err)
	ctx := context.Background()
	assert.Equal(t, ctx, sessionContext(ctx), "without a usable operator directory the context carries no session")
}

func TestSessionContext_NoHandleInEnv_ReturnsContextUnchanged(t *testing.T) {
	initRegistrationApp(t)
	ctx := context.Background()
	assert.Equal(t, ctx, sessionContext(ctx))
	t.Setenv("CODEX_THREAD_ID", "thread-A")
	assert.NotEqual(t, ctx, sessionContext(ctx))
	breakOperatorDirectory(t)
	assert.Equal(t, ctx, sessionContext(ctx))
}

func TestRegistrationCommands_NoProject_AreRefused(t *testing.T) {
	saveAndResetApp(t)
	require.ErrorContains(t, runAgentUnregister("lane", false), "not in an mtix project")
	require.ErrorContains(t, runAgentListRegistrations(), "not in an mtix project")
	var report DoctorReport
	assert.Empty(t, appendRegistrationChecks(context.Background(), report).Checks)
}

func TestOperatorTakeOverLog_CorruptRecord_ReturnsError(t *testing.T) {
	initRegistrationApp(t)
	env, err := oplocal.FromProcess()
	require.NoError(t, err)
	state := oplocal.New(env)
	require.NoError(t, state.WriteJSON(takeOverRecordName, "not a list"))
	log, err := newOperatorTakeOverLog()
	require.NoError(t, err)
	_, err = log.Recent(context.Background(), "a", time.Now())
	require.ErrorContains(t, err, "read take-over record")
	require.Error(t, log.Append(context.Background(), service.TakeOverRecord{AgentID: "a", At: time.Now()}))
}

func TestSessionReport_PrintAndJSONFields(t *testing.T) {
	var nilReport *sessionReport
	nilReport.print()
	out := map[string]any{}
	nilReport.addTo(out)
	assert.Empty(t, out)

	warn := &sessionReport{Warning: "no operator directory"}
	warn.addTo(out)
	assert.Equal(t, "no operator directory", out["session_warning"])

	taken := &sessionReport{Outcome: "taken_over", Harness: "codex", Origin: "manual", Epoch: 2, NoticeSent: true}
	text := captureStdout(t, taken.print)
	assert.Contains(t, text, "taken over")
	assert.Contains(t, text, "one notice")
	quiet := captureStdout(t, (&sessionReport{Outcome: "registered", Harness: "pi", Origin: "manual", Epoch: 1}).print)
	assert.NotContains(t, quiet, "notice")
}

func TestAgentListCommand_WithRegistrationsFlag_Runs(t *testing.T) {
	initRegistrationApp(t)
	cmd := newAgentListCmd()
	cmd.SetArgs([]string{"--registrations"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	text := captureStdout(t, func() { require.NoError(t, cmd.Execute()) })
	assert.Contains(t, text, "No registrations")
}

func TestAgentUnregisterCommand_Runs(t *testing.T) {
	initRegistrationApp(t)
	captureStdout(t, func() { require.NoError(t, runAgentRegister("lane", sessionOptions{Session: "s"})) })
	cmd := newAgentUnregisterCmd()
	cmd.SetArgs([]string{"lane", "--force"})
	text := captureStdout(t, func() { require.NoError(t, cmd.Execute()) })
	assert.Contains(t, text, "Unregistered agent lane")
}

func TestDoctorRegistrationChecks_ClosedStore_FailsWithDetail(t *testing.T) {
	initRegistrationApp(t)
	require.NoError(t, app.store.Close())
	var report DoctorReport
	report = appendRegistrationChecks(context.Background(), report)
	require.Len(t, report.Checks, 1)
	assert.False(t, report.Checks[0].Pass)
	assert.Contains(t, report.Checks[0].Detail, "expire stale registrations")
}
