// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
)

// policyAgent is the registered fixture id; policyBody must never reach stdout
// or stderr while the id is held.
const (
	policyAgent = "permission-fixture"
	policyBody  = "held-body-must-stay-hidden"
)

// addInboxEvent addresses one comment to agent.
func addInboxEvent(t *testing.T, agent string, n int) {
	t.Helper()
	node, err := app.nodeSvc.CreateNode(context.Background(), &service.CreateNodeRequest{Project: "TEST", Title: "Addressed " + strconv.Itoa(n), Creator: "sender"})
	require.NoError(t, err)
	require.NoError(t, app.store.SetAnnotations(context.Background(), node.ID, []model.Annotation{{
		ID: "ann-" + agent + "-" + strconv.Itoa(n), Author: "sender", Text: policyBody + "-" + strconv.Itoa(n),
		CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Addressee: agent,
	}}))
}

// heldBoard registers policyAgent, addresses n events to it and marks the board
// hub-connected, so the id is unconfirmed (D10) and every event is held.
func heldBoard(t *testing.T, n int) *oplocal.State {
	t.Helper()
	state := permissionRegisterCurrentHost(t)
	for i := range n {
		addInboxEvent(t, policyAgent, i)
	}
	flipped, err := app.store.MarkHubConnected(context.Background(), "foreign_event")
	require.NoError(t, err)
	require.True(t, flipped)
	require.NoError(t, reconcileHubMarkMirror())
	return state
}

func runPolicyInbox(t *testing.T, o inboxOptions) (stdout, stderr string, err error) {
	t.Helper()
	var errBuf bytes.Buffer
	o.Stderr = &errBuf
	if o.Agent == "" {
		o.Agent = policyAgent
	}
	stdout, err = cliBehaviorOutput(t, func() error { return runInboxWith(o) })
	return stdout, errBuf.String(), err
}

func TestInboxWait_HubBoardUnconfirmedID_ReturnsNothingHeldOnStderr(t *testing.T) {
	heldBoard(t, 2)
	stdout, stderr, err := runPolicyInbox(t, inboxOptions{Wait: true, Timeout: 1})
	require.ErrorIs(t, err, errInboxWaitEmpty, "a wait with every event held times out empty")
	assert.Empty(t, strings.TrimSpace(strings.ReplaceAll(stdout, "(inbox empty)", "")), "stdout carries no event")
	assert.NotContains(t, stdout, policyBody)
	assert.Equal(t, "2 held: ask your operator to run `mtix setup`\n", stderr)
	assert.NotContains(t, stderr, policyBody)
}

func TestInboxWait_EmptyTimeout_Exit5(t *testing.T) {
	initTestApp(t)
	_, stderr, err := runPolicyInbox(t, inboxOptions{Agent: "nobody", Wait: true, Timeout: 1})
	require.ErrorIs(t, err, errInboxWaitEmpty)
	assert.Equal(t, exitCodeInboxEmpty, exitCodeForError(err))
	assert.Empty(t, stderr, "nothing held, nothing printed")

	heldBoard(t, 1)
	_, _, err = runPolicyInbox(t, inboxOptions{Wait: true, Timeout: 1})
	assert.Equal(t, exitCodeInboxEmpty, exitCodeForError(err), "an all-held timeout is exit 5 too")
}

func TestInboxPromptFormat_HeldOnly_StdoutEmpty(t *testing.T) {
	heldBoard(t, 3)
	for _, format := range []string{"prompt", "context"} {
		stdout, stderr, err := runPolicyInbox(t, inboxOptions{Format: format})
		require.NoError(t, err, format)
		assert.Empty(t, stdout, format)
		assert.Equal(t, "3 held: ask your operator to run `mtix setup`\n", stderr, format)
	}
}

// Nudged pull, MCP and priming show the same count (ADR-007 B1): the pull is
// the non-wait read; priming is claudeHookWait.
func TestInboxPull_AndPriming_HubBoardUnconfirmedID_SameHeldCount(t *testing.T) {
	heldBoard(t, 2)
	_, stderr, err := runPolicyInbox(t, inboxOptions{})
	require.NoError(t, err)
	assert.Contains(t, stderr, "2 held")

	svc := newPolicyInboxService()
	res, err := svc.InboxRead(context.Background(), policyAgent, service.InboxReadOptions{})
	require.NoError(t, err)
	assert.Equal(t, 2, res.Held.Count)

	events, err := claudeHookWait(context.Background(), policyAgent, 300*time.Millisecond,
		func(int64) bool { return false }, func(string, io.Writer) func() { return func() {} })
	require.NoError(t, err)
	assert.Empty(t, events, "priming returns no held event")
}

func TestInboxIncludeHeld_HarnessMarker_Refused(t *testing.T) {
	heldBoard(t, 1)
	_, _, err := runPolicyInbox(t, inboxOptions{IncludeHeld: true, Env: []string{"CLAUDECODE=1"}})
	require.ErrorIs(t, err, errIncludeHeldRefused)

	stdout, stderr, err := runPolicyInbox(t, inboxOptions{IncludeHeld: true, Env: []string{"PATH=/usr/bin"}})
	require.NoError(t, err)
	assert.Contains(t, stdout, policyBody+"-0", "held bodies come only from the explicit operator flag")
	assert.Contains(t, stderr, "1 held")
}

func TestInboxDefaultOutput_IsEnvelope_PlainKeepsListing(t *testing.T) {
	initTestApp(t)
	addInboxEvent(t, "worker", 0)
	app.jsonOutput = false
	envelope, _, err := runPolicyInbox(t, inboxOptions{Agent: "worker"})
	require.NoError(t, err)
	assert.Contains(t, envelope, "as claimed, not verified")
	plain, _, err := runPolicyInbox(t, inboxOptions{Agent: "worker", Format: "plain"})
	require.NoError(t, err)
	assert.Contains(t, plain, "ack with: mtix inbox ack")
	assert.NotContains(t, plain, "as claimed, not verified")
	_, _, err = runPolicyInbox(t, inboxOptions{Agent: "worker", Format: "yaml"})
	require.ErrorContains(t, err, "format")
}

func policySetLimit(t *testing.T, limit string) {
	t.Helper()
	env, err := oplocal.FromProcess()
	require.NoError(t, err)
	require.NoError(t, oplocal.New(env).UpdateWakeSetting(context.Background(),
		oplocal.WakeSettingUpdate{Key: "wake.wakes_per_hour", Value: &limit}, nil))
}

func policyCredits(t *testing.T, agent string) int64 {
	t.Helper()
	env, err := oplocal.FromProcess()
	require.NoError(t, err)
	state := oplocal.New(env)
	host, err := state.HostID()
	require.NoError(t, err)
	uid, err := currentUserID()
	require.NoError(t, err)
	mirror, present, err := state.ReadWakeBudget(model.WakeRecipient{AgentID: agent, HostID: host, UID: uid})
	require.NoError(t, err)
	if !present {
		return 0
	}
	var total int64
	for _, c := range mirror.Credits {
		total += c.Count
	}
	return total
}

// An unacked event plus one new event charge the budget once, not twice.
func TestInboxWait_UnackedPlusNew_BudgetChargedOnce(t *testing.T) {
	permissionRegisterCurrentHost(t)
	policySetLimit(t, "12")
	addInboxEvent(t, policyAgent, 0)
	_, _, err := runPolicyInbox(t, inboxOptions{Wait: true, Timeout: 1})
	require.NoError(t, err)
	require.Equal(t, int64(1), policyCredits(t, policyAgent))

	addInboxEvent(t, policyAgent, 1) // event 0 is still unacked
	stdout, _, err := runPolicyInbox(t, inboxOptions{Wait: true, Timeout: 1})
	require.NoError(t, err)
	assert.Contains(t, stdout, policyBody+"-0")
	assert.Contains(t, stdout, policyBody+"-1")
	assert.Equal(t, int64(2), policyCredits(t, policyAgent), "the repeated unacked event is not charged again")

	_, _, err = runPolicyInbox(t, inboxOptions{})
	require.NoError(t, err)
	assert.Equal(t, int64(2), policyCredits(t, policyAgent))
}

func TestInboxWait_ConfiguredBudgetExhausted_HoldsRemainder(t *testing.T) {
	permissionRegisterCurrentHost(t)
	policySetLimit(t, "2")
	for i := range 3 {
		addInboxEvent(t, policyAgent, i)
	}
	stdout, stderr, err := runPolicyInbox(t, inboxOptions{})
	require.NoError(t, err)
	assert.Contains(t, stdout, policyBody+"-1")
	assert.NotContains(t, stdout, policyBody+"-2")
	assert.Contains(t, stderr, "1 held")
}

// An unregistered worker loop on a board never connected is never capped.
func TestInboxWait_NeverConnectedUnregisteredLoop_NotCapped(t *testing.T) {
	initTestApp(t)
	policySetLimit(t, "1")
	for i := range 5 {
		addInboxEvent(t, "loop-worker", i)
	}
	for range 3 {
		stdout, stderr, err := runPolicyInbox(t, inboxOptions{Agent: "loop-worker", Wait: true, Timeout: 1})
		require.NoError(t, err)
		assert.Equal(t, 5, strings.Count(stdout, policyBody), "all five events returned every time")
		assert.Empty(t, stderr)
	}
	assert.Zero(t, policyCredits(t, "loop-worker"))
}

func TestAgentRegister_SecondRegister_ExitsZero(t *testing.T) {
	initTestApp(t)
	for range 2 {
		_, err := cliBehaviorOutput(t, func() error {
			return runAgentRegister("twice", sessionOptions{Harness: "fixture", Session: "same-session"})
		})
		require.NoError(t, err)
	}
}

func TestInboxHeldCheck_HubBoard_FailsWithExactFix(t *testing.T) {
	heldBoard(t, 2)
	perms, board, err := configuredPermissions()
	require.NoError(t, err)
	res := inboxHeldCheck(context.Background(), perms, board)
	assert.Equal(t, service.DoctorFail, res.Status)
	assert.Equal(t, "mtix agent allow "+policyAgent, res.Fix)
	assert.Contains(t, res.Detail, policyAgent+" (2 held)")
	assert.Equal(t, map[string]int64{policyAgent: 2}, res.Data["held"])
}

func TestInboxHeldCheck_NothingHeld_Passes(t *testing.T) {
	permissionRegisterCurrentHost(t)
	addInboxEvent(t, policyAgent, 0)
	perms, board, err := configuredPermissions()
	require.NoError(t, err)
	res := inboxHeldCheck(context.Background(), perms, board)
	assert.Equal(t, service.DoctorPass, res.Status)
}
