// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// These tests bind CLI agent/inbox/stale output to real durable local state.
package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func cliBehaviorOutput(t *testing.T, call func() error) (string, error) {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "stdout-")
	require.NoError(t, err)
	old := os.Stdout
	os.Stdout = file
	defer func() {
		os.Stdout = old
		require.NoError(t, file.Close())
	}()
	callErr := call()
	data, err := os.ReadFile(file.Name())
	require.NoError(t, err)
	return string(data), callErr
}

func TestAgentInboxOutput_AgentTransitionJSON(t *testing.T) {
	initTestApp(t)
	app.jsonOutput = true
	register := newAgentRegisterCmd()
	register.SetArgs([]string{"worker"})
	text, err := cliBehaviorOutput(t, register.Execute)
	require.NoError(t, err)
	var registered map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &registered))
	// Session fields are additive (MTIX-112.17.4): the original two keys keep their meaning.
	require.Equal(t, "worker", registered["agent_id"])
	require.Equal(t, "registered", registered["status"])
	state := newAgentStateCmd()
	state.SetArgs([]string{"worker", "--set", "working"})
	text, err = cliBehaviorOutput(t, state.Execute)
	require.NoError(t, err)
	require.JSONEq(t, `{"agent_id":"worker","state":"working","status":"updated"}`, text)
	actual, err := app.agentSvc.GetAgentState(context.Background(), "worker")
	require.NoError(t, err)
	require.Equal(t, model.AgentStateWorking, actual)
	text, err = cliBehaviorOutput(t, func() error { return runGetAgentState("worker") })
	require.NoError(t, err)
	require.JSONEq(t, `{"agent_id":"worker","state":"working"}`, text)
	_, err = app.agentSvc.GetCurrentWork(context.Background(), "worker")
	require.ErrorIs(t, err, model.ErrNotFound)
}

func TestAgentInboxOutput_RegistrationRefusals(t *testing.T) {
	t.Run("missing-service", func(t *testing.T) {
		saveAndResetApp(t)
		text, err := cliBehaviorOutput(t, func() error { return runAgentRegister("worker", sessionOptions{}) })
		require.ErrorContains(t, err, "not in an mtix project")
		require.Empty(t, text)
	})
	t.Run("missing-project", func(t *testing.T) {
		initTestApp(t)
		app.configSvc = nil
		text, err := cliBehaviorOutput(t, func() error { return runAgentRegister("worker", sessionOptions{}) })
		require.ErrorIs(t, err, model.ErrInvalidInput)
		require.Empty(t, text)
		_, err = app.agentSvc.GetAgentState(context.Background(), "worker")
		require.ErrorIs(t, err, model.ErrNotFound)
	})
}

func outputInboxFixture(t *testing.T) []sqlite.InboxEvent {
	t.Helper()
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	app.store.SetClock(func() time.Time { return now })
	for i, agent := range []string{"worker", "worker", "worker", "other"} {
		node, err := app.nodeSvc.CreateNode(context.Background(), &service.CreateNodeRequest{Project: "TEST", Title: "Addressed " + strconv.Itoa(i), Creator: "sender"})
		require.NoError(t, err)
		require.NoError(t, app.store.SetAnnotations(context.Background(), node.ID, []model.Annotation{{ID: "ann-" + strconv.Itoa(i), Author: "sender", Text: "body-" + strconv.Itoa(i), CreatedAt: now, Addressee: agent}}))
	}
	events, err := app.store.InboxList(context.Background(), "worker")
	require.NoError(t, err)
	require.Len(t, events, 3)
	for i, event := range events {
		require.Equal(t, "sender", event.Author)
		require.Equal(t, "body-"+strconv.Itoa(i), event.Body)
		require.NotEmpty(t, event.NodeID)
		require.NotEmpty(t, event.EventID)
	}
	return events
}

func outputInboxCommand(t *testing.T, args ...string) string {
	t.Helper()
	cmd := newInboxCmd()
	cmd.SetArgs(args)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	text, err := cliBehaviorOutput(t, cmd.Execute)
	require.NoError(t, err)
	return text
}

func TestAgentInboxOutput_InboxJSONDurability(t *testing.T) {
	initTestApp(t)
	app.jsonOutput = true
	events := outputInboxFixture(t)
	other := inboxSeqs(t, "other")
	var listed []sqlite.InboxEvent
	require.NoError(t, json.Unmarshal([]byte(outputInboxCommand(t, "--agent", "worker")), &listed))
	require.Equal(t, events, listed)
	s1, s2, s3 := events[0].Seq, events[1].Seq, events[2].Seq
	text := outputInboxCommand(t, "ack", strconv.FormatInt(s3, 10), "--agent", "worker")
	require.JSONEq(t, `{"agent":"worker","acked":[`+strconv.FormatInt(s3, 10)+`]}`, text)
	require.Equal(t, []int64{s1, s2}, inboxSeqs(t, "worker"))
	require.Equal(t, other, inboxSeqs(t, "other"))
	outputAckTimestamp(t, s3)
	text = outputInboxCommand(t, "ack", strconv.FormatInt(s1, 10), "--agent", "worker")
	require.JSONEq(t, `{"agent":"worker","acked":[`+strconv.FormatInt(s1, 10)+`]}`, text)
	outputAckTimestamp(t, s1)
	text = outputInboxCommand(t, "ack", strconv.FormatInt(s2, 10), "--agent", "worker", "--through")
	require.JSONEq(t, `{"agent":"worker","acked_through":`+strconv.FormatInt(s2, 10)+`}`, text)
	outputAckLedger(t, s2, s3)
	require.Empty(t, inboxSeqs(t, "worker"))
	require.Equal(t, other, inboxSeqs(t, "other"))
	require.NoError(t, app.store.Close())
	reopened, err := sqlite.New(filepath.Join(app.mtixDir, "data", "mtix.db"), nil)
	require.NoError(t, err)
	app.store = reopened
	outputAckLedger(t, s2, s3)
	require.Empty(t, inboxSeqs(t, "worker"))
	require.Equal(t, other, inboxSeqs(t, "other"))
}

func outputAckTimestamp(t *testing.T, seq int64) {
	t.Helper()
	var actual string
	// Verify the selective ledger's real stored timestamp for this recipient.
	require.NoError(t, app.store.ReadDB().QueryRow("SELECT acked_at FROM agent_inbox_ack WHERE agent_id = ? AND event_seq = ?", "worker", seq).Scan(&actual))
	require.Equal(t, "2026-01-02T03:04:05Z", actual)
}

func outputAckLedger(t *testing.T, cursor, higher int64) {
	t.Helper()
	var actual int64
	// The watermark must persist independently of higher selective acknowledgments.
	require.NoError(t, app.store.ReadDB().QueryRow("SELECT cursor FROM agent_inbox_cursor WHERE agent_id = ?", "worker").Scan(&actual))
	require.Equal(t, cursor, actual)
	rows, err := app.store.ReadDB().Query("SELECT event_seq FROM agent_inbox_ack WHERE agent_id = ? ORDER BY event_seq", "worker")
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	var seqs []int64
	for rows.Next() {
		var seq int64
		require.NoError(t, rows.Scan(&seq))
		seqs = append(seqs, seq)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, []int64{higher}, seqs)
}

func TestAgentInboxOutput_InboxRefusals(t *testing.T) {
	t.Run("missing-store", func(t *testing.T) {
		saveAndResetApp(t)
		for _, call := range []func() error{func() error { return runInbox("worker", false, 0, "") }, func() error { return runInboxAck("worker", []string{"1"}, false) }} {
			text, err := cliBehaviorOutput(t, call)
			require.ErrorContains(t, err, "not in an mtix project")
			require.Empty(t, text)
		}
	})
	t.Run("invalid-and-closed", func(t *testing.T) {
		initTestApp(t)
		app.jsonOutput = true
		events := outputInboxFixture(t)
		for _, through := range []bool{false, true} {
			for _, seq := range []string{"0", "999999"} {
				text, err := cliBehaviorOutput(t, func() error { return runInboxAck("worker", []string{seq}, through) })
				require.ErrorIs(t, err, model.ErrInvalidInput)
				require.Empty(t, text)
			}
		}
		text, err := cliBehaviorOutput(t, func() error { return runInboxAck("worker", []string{"not-a-sequence"}, false) })
		require.ErrorContains(t, err, "invalid sequence")
		require.Empty(t, text)
		require.Equal(t, []int64{events[0].Seq, events[1].Seq, events[2].Seq}, inboxSeqs(t, "worker"))
		require.NoError(t, app.store.Close())
		for _, call := range []func() error{func() error { return runInbox("worker", false, 0, "") }, func() error { return runInboxAck("worker", []string{"1"}, false) }, func() error { return runInboxAck("worker", []string{"1"}, true) }} {
			text, err = cliBehaviorOutput(t, call)
			require.Error(t, err)
			require.Empty(t, text)
		}
	})
}

func TestAgentInboxOutput_StaleMembershipAndRefusals(t *testing.T) {
	initTestApp(t)
	now := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	app.agentSvc = service.NewAgentService(app.store, nil, app.configSvc, nil, func() time.Time { return now })
	_, err := app.configSvc.Set("agent.stale_threshold", "30m")
	require.NoError(t, err)
	_, err = app.agentSvc.RegisterAgentWithStatus(context.Background(), "old-worker", "TEST")
	require.NoError(t, err)
	now = now.Add(time.Hour)
	_, err = app.agentSvc.RegisterAgentWithStatus(context.Background(), "current-worker", "TEST")
	require.NoError(t, err)
	app.jsonOutput = true
	text, err := cliBehaviorOutput(t, func() error { return runStale("", false) })
	require.NoError(t, err)
	require.JSONEq(t, `["old-worker"]`, text)
	app.jsonOutput = false
	text, err = cliBehaviorOutput(t, func() error { return runStale("", false) })
	require.NoError(t, err)
	require.Equal(t, "Stale agents:\n  old-worker\n", text)
	require.NoError(t, app.agentSvc.Heartbeat(context.Background(), "old-worker"))
	text, err = cliBehaviorOutput(t, func() error { return runStale("", false) })
	require.NoError(t, err)
	require.Equal(t, "No stale agents found\n", text)
	text, err = cliBehaviorOutput(t, func() error { return runStale("TEST", true) })
	require.Error(t, err)
	require.Empty(t, text)
	require.NoError(t, app.store.Close())
	text, err = cliBehaviorOutput(t, func() error { return runStale("", false) })
	require.ErrorContains(t, err, "query stale agents")
	require.Empty(t, text)
}
