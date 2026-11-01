// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

// Every test runs on a scratch board with generated operator state under the test
// home; none touches a real Claude settings file (MTIX-112.40.4).

func addressHookMail(t *testing.T, nodeID, id, body string) {
	t.Helper()
	require.NoError(t, app.store.SetAnnotations(t.Context(), nodeID, []model.Annotation{{
		ID: id, Author: "sender", Text: body, Addressee: "worker",
		CreatedAt: time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC),
	}}))
}

func runHook(t *testing.T, ev service.ClaudeHookEvent) (string, error) {
	t.Helper()
	var stderr bytes.Buffer
	err := runClaudeSessionHook(&stderr, ev, "worker", 5)
	return stderr.String(), err
}

func TestHookClaudeSession_SessionStart_RegistersArmsAndWakesWithExitTwo(t *testing.T) {
	node := wakeStatusApp(t)
	addressHookMail(t, node, "hook-wake-1", "please review the plan")

	stderr, err := runHook(t, service.ClaudeHookSessionStart)

	require.ErrorIs(t, err, errClaudeHookWake)
	assert.Equal(t, 2, exitCodeForError(err))
	assert.Contains(t, stderr, "please review the plan", "the quoted envelope reaches stderr")
	assert.Contains(t, stderr, "worker")
	host, uid := seededWakeHost(t)
	rows, err := app.store.ListRegistrations(t.Context(), host, uid)
	require.NoError(t, err)
	require.Len(t, rows, 1, "session-start registered the agent with no agent action")
	assert.Equal(t, "worker", rows[0].AgentID)
}

func TestHookClaudeSession_SameEventNeverWakesTwice(t *testing.T) {
	node := wakeStatusApp(t)
	addressHookMail(t, node, "hook-wake-2", "one event")
	_, err := runHook(t, service.ClaudeHookSessionStart)
	require.ErrorIs(t, err, errClaudeHookWake)

	stderr, err := runHook(t, service.ClaudeHookStop)

	require.NoError(t, err, "the re-arm after a wake finds the event already claimed")
	assert.Contains(t, stderr, service.ClaudeHookClaimLost)
	assert.Equal(t, 0, exitCodeForError(err))
}

func TestHookClaudeSession_WaiterAlreadyArmed_SecondStartExitsZero(t *testing.T) {
	wakeStatusApp(t)
	seedWakeRegistration(t, "worker")
	key, err := leaseIdentity("worker")
	require.NoError(t, err)
	_, err = service.NewWakeLeaseService(app.store).Acquire(t.Context(), key, time.Minute)
	require.NoError(t, err)

	stderr, err := runHook(t, service.ClaudeHookStop)

	require.NoError(t, err)
	assert.Contains(t, stderr, service.ClaudeHookAlreadyArmed)
}

func TestHookClaudeSession_UnregisteredStop_HeldByWakePolicy(t *testing.T) {
	node := wakeStatusApp(t)
	addressHookMail(t, node, "hook-wake-3", "x")

	stderr, err := runHook(t, service.ClaudeHookStop)

	require.NoError(t, err, "the policy holds the wake; the message stays in the inbox")
	assert.Contains(t, stderr, service.ClaudeHookHeld)
	assert.Contains(t, stderr, "not_registered")
}

func TestHookClaudeSession_NoProject_ExitsZero(t *testing.T) {
	saveAndResetApp(t)
	var stderr bytes.Buffer
	require.NoError(t, runClaudeSessionHook(&stderr, service.ClaudeHookStop, "worker", 1))
	assert.Contains(t, stderr.String(), "not armed")
}

func TestHookClaudeSession_UnsafeAgentID_ExitsZeroAndIsReported(t *testing.T) {
	wakeStatusApp(t)
	var stderr bytes.Buffer
	require.NoError(t, runClaudeSessionHook(&stderr, service.ClaudeHookStop, "../evil", 1), "a hook never breaks a session")
	assert.NotEmpty(t, stderr.String())
}

func TestHookClaudeSession_CommandTree_RegistersBothEvents(t *testing.T) {
	cmd, remaining, err := newRootCmd().Find([]string{"hook", "claude-session", "stop"})
	require.NoError(t, err)
	assert.Equal(t, "claude-session", cmd.Name())
	assert.Equal(t, []string{"stop"}, remaining)
	require.NotNil(t, cmd.Flags().Lookup("agent"))
	assert.Equal(t, 2, exitCodeForError(errClaudeHookWake))
	assert.Equal(t, 2, exitCodeForError(errors.Join(errClaudeHookWake)))
}

func TestDoctorHookErrors_RecordedFailure_RowFailsWithRunnableFix(t *testing.T) {
	wakeStatusApp(t)
	seedWakeRegistration(t, "worker")
	require.NoError(t, claudeHookLog().Record("worker", service.ClaudeHookStop, errors.New("wait for inbox: boom")))
	row := hookErrorsRow(t)
	assert.Equal(t, service.DoctorFail, row.Status)
	assert.Equal(t, "mtix hook claude-session stop --agent worker", row.Fix)
	assert.Contains(t, row.Detail, "wait for inbox: boom")
	everyFixParses(t, row.ID, row.Fix)

	require.NoError(t, claudeHookLog().Record("worker", service.ClaudeHookStop, nil))
	assert.Equal(t, service.DoctorInfo, hookErrorsRow(t).Status, "a successful re-run clears the failure")
}

func hookErrorsRow(t *testing.T) service.DoctorResult {
	t.Helper()
	svc, err := newConfiguredDoctorService()
	require.NoError(t, err)
	var out bytes.Buffer
	if err := runDoctor(context.Background(), &out, svc, doctorOutputOptions{JSON: true}); err != nil {
		require.ErrorIs(t, err, service.ErrDoctorChecksFailed, "a failed row is the only expected error")
	}
	var rows []service.DoctorResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &rows))
	for _, r := range rows {
		if r.ID == "wake.listener.hook-errors" {
			return r
		}
	}
	t.Fatal("doctor has no wake.listener.hook-errors row")
	return service.DoctorResult{}
}
