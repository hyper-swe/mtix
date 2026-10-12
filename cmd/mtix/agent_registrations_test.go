// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

// Session registration commands: detection, take-over, listing, unregister and doctor checks.

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// initRegistrationApp isolates the board and clears harness variables the
// developer's own shell may export, so detection tests are deterministic.
func initRegistrationApp(t *testing.T) {
	t.Helper()
	for _, name := range []string{"CLAUDE_CODE_MESSAGING_SOCKET", "CODEX_THREAD_ID", "PI_SESSION_ID"} {
		t.Setenv(name, "")
	}
	initTestApp(t)
}

func envFrom(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestDetectSession_Environment_PicksHarnessAndOrigin(t *testing.T) {
	tests := []struct {
		name                    string
		env                     map[string]string
		opts                    sessionOptions
		harness, handle, origin string
	}{
		{"claude socket", map[string]string{"CLAUDE_CODE_MESSAGING_SOCKET": "/tmp/s1"}, sessionOptions{}, "claude-code", "/tmp/s1", "session-env"},
		{"codex thread", map[string]string{"CODEX_THREAD_ID": "th-1"}, sessionOptions{}, "codex", "th-1", "session-env"},
		{"pi session", map[string]string{"PI_SESSION_ID": "pi-9"}, sessionOptions{}, "pi", "pi-9", "session-env"},
		{"typed session is manual", map[string]string{"CODEX_THREAD_ID": "th-1"}, sessionOptions{Session: "typed"}, "codex", "typed", "manual"},
		{"typed harness wins", map[string]string{"CODEX_THREAD_ID": "th-1"}, sessionOptions{Harness: "pi", Session: "x"}, "pi", "x", "manual"},
		{"nothing detected", map[string]string{}, sessionOptions{}, "unknown", "", "manual"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectSession(envFrom(tt.env), tt.opts)
			assert.Equal(t, tt.harness, got.Harness)
			assert.Equal(t, tt.handle, got.Handle)
			assert.Equal(t, tt.origin, got.Origin)
		})
	}
}

func registrationJSON(t *testing.T) []map[string]any {
	t.Helper()
	app.jsonOutput = true
	defer func() { app.jsonOutput = false }()
	out := captureStdout(t, func() { require.NoError(t, runAgentListRegistrations()) })
	var rows []map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &rows))
	return rows
}

func TestAgentRegister_SessionEnv_StoresRegistrationHandsFree(t *testing.T) {
	initRegistrationApp(t)
	t.Setenv("CODEX_THREAD_ID", "thread-A")
	captureStdout(t, func() { require.NoError(t, runAgentRegister("lane", sessionOptions{})) })
	rows := registrationJSON(t)
	require.Len(t, rows, 1)
	assert.Equal(t, "codex", rows[0]["harness"])
	assert.Equal(t, "session-env", rows[0]["origin"])
	assert.NotEmpty(t, rows[0]["liveness"])

	// A restarted lane (new thread, old session gone) registers with no flags.
	t.Setenv("CODEX_THREAD_ID", "thread-B")
	_, err := app.store.WriteDB().Exec(`UPDATE agent_registrations SET pid=2147483000, pid_start_time='gone'`)
	require.NoError(t, err)
	captureStdout(t, func() { require.NoError(t, runAgentRegister("lane", sessionOptions{})) })
	rows = registrationJSON(t)
	require.Len(t, rows, 1)
	assert.EqualValues(t, 2, rows[0]["epoch"])
}

func TestAgentRegister_LiveOtherSession_RefusedWithoutTakeOver(t *testing.T) {
	initRegistrationApp(t)
	t.Setenv("CODEX_THREAD_ID", "thread-A")
	captureStdout(t, func() { require.NoError(t, runAgentRegister("lane", sessionOptions{})) })
	// The recorded owner is this very process tree, so it probes alive.
	t.Setenv("CODEX_THREAD_ID", "thread-B")
	err := runAgentRegister("lane", sessionOptions{})
	require.ErrorIs(t, err, model.ErrAgentStillActive)
	assert.Contains(t, err.Error(), "--take-over")
	captureStdout(t, func() { require.NoError(t, runAgentRegister("lane", sessionOptions{TakeOver: true})) })
	rows := registrationJSON(t)
	assert.EqualValues(t, 2, rows[0]["epoch"])
}

func TestAgentRegister_JSON_ReportsSessionFields(t *testing.T) {
	initRegistrationApp(t)
	app.jsonOutput = true
	out := captureStdout(t, func() { require.NoError(t, runAgentRegister("lane", sessionOptions{Harness: "pi", Session: "s"})) })
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	assert.Equal(t, "lane", got["agent_id"])
	assert.Equal(t, "registered", got["status"])
	assert.Equal(t, "registered", got["session"])
	assert.Equal(t, "pi", got["harness"])
	assert.EqualValues(t, 1, got["epoch"])
}

func TestAgentListRegistrationsJSON_ShowsLivenessAndHold(t *testing.T) {
	initRegistrationApp(t)
	captureStdout(t, func() { require.NoError(t, runAgentRegister("lane", sessionOptions{Session: "s"})) })
	_, err := app.store.WriteDB().Exec(`UPDATE agent_registrations SET pending_hold=1`)
	require.NoError(t, err)
	rows := registrationJSON(t)
	require.Len(t, rows, 1)
	for _, key := range []string{"agent_id", "harness", "origin", "liveness", "hold", "live_route", "epoch", "high_water", "last_seen"} {
		assert.Contains(t, rows[0], key)
	}
	assert.Equal(t, true, rows[0]["hold"])
	assert.Equal(t, "alive", rows[0]["liveness"])

	app.jsonOutput = false
	text := captureStdout(t, func() { require.NoError(t, runAgentListRegistrations()) })
	assert.Contains(t, text, "lane")
	assert.Contains(t, text, "alive")
}

func TestAgentListRegistrations_Empty_PrintsNoneInText(t *testing.T) {
	initRegistrationApp(t)
	text := captureStdout(t, func() { require.NoError(t, runAgentListRegistrations()) })
	assert.Contains(t, text, "No registrations")
	assert.Equal(t, "[]", func() string {
		app.jsonOutput = true
		defer func() { app.jsonOutput = false }()
		return string(bytes.TrimSpace([]byte(captureStdout(t, func() { require.NoError(t, runAgentListRegistrations()) }))))
	}())
}

func TestAgentUnregister_OwnSessionAndForce(t *testing.T) {
	initRegistrationApp(t)
	t.Setenv("CODEX_THREAD_ID", "thread-A")
	captureStdout(t, func() { require.NoError(t, runAgentRegister("lane", sessionOptions{})) })
	t.Setenv("CODEX_THREAD_ID", "thread-B")
	require.ErrorIs(t, runAgentUnregister("lane", false), model.ErrAgentStillActive)
	require.ErrorIs(t, runAgentUnregister("nobody", false), model.ErrNotFound)
	captureStdout(t, func() { require.NoError(t, runAgentUnregister("lane", true)) })
	assert.Empty(t, registrationJSON(t))
	captureStdout(t, func() { require.NoError(t, runAgentRegister("lane", sessionOptions{})) })
	captureStdout(t, func() { require.NoError(t, runAgentUnregister("lane", false)) })
	assert.Empty(t, registrationJSON(t))
}

func TestOperatorTakeOverLog_AppendRecentAndPrune(t *testing.T) {
	initRegistrationApp(t)
	log, err := newOperatorTakeOverLog()
	require.NoError(t, err)
	ctx := context.Background()
	now := time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)
	require.NoError(t, log.Append(ctx, service.TakeOverRecord{AgentID: "a", At: now.Add(-48 * time.Hour), DisplacedSession: "old"}))
	require.NoError(t, log.Append(ctx, service.TakeOverRecord{AgentID: "a", At: now, DisplacedSession: "h1", NoticeAttempted: true}))
	require.NoError(t, log.Append(ctx, service.TakeOverRecord{AgentID: "b", At: now, DisplacedSession: "h2"}))
	got, err := log.Recent(ctx, "a", now.Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "h1", got[0].DisplacedSession)
	all, err := log.Recent(ctx, "a", now.Add(-72*time.Hour))
	require.NoError(t, err)
	assert.Len(t, all, 1, "entries older than a day are pruned on append")
}

func TestInboxNoticeSender_NoCurrentWork_ReturnsNoRoute(t *testing.T) {
	initRegistrationApp(t)
	captureStdout(t, func() { require.NoError(t, runAgentRegister("lane", sessionOptions{Session: "s"})) })
	err := inboxNoticeSender{}.SendTakeOverNotice(context.Background(), "lane")
	require.ErrorIs(t, err, service.ErrNoNoticeRoute)
}

func TestInboxNoticeSender_CurrentWork_DeliversFixedNoticeToInbox(t *testing.T) {
	initRegistrationApp(t)
	captureStdout(t, func() { require.NoError(t, runAgentRegister("lane", sessionOptions{Session: "s"})) })
	node := seedOneNode(t)
	_, err := app.store.WriteDB().Exec(`UPDATE agents SET current_node_id=? WHERE agent_id='lane'`, node)
	require.NoError(t, err)
	require.NoError(t, inboxNoticeSender{}.SendTakeOverNotice(context.Background(), "lane"))
	events, err := app.store.InboxList(context.Background(), "lane")
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, takeOverNoticeText, events[0].Body)
	assert.NotContains(t, takeOverNoticeText, "host")
}

func TestInboxList_SessionHandleInEnv_RefreshesLastSeen(t *testing.T) {
	initRegistrationApp(t)
	t.Setenv("CODEX_THREAD_ID", "thread-A")
	captureStdout(t, func() { require.NoError(t, runAgentRegister("lane", sessionOptions{})) })
	_, err := app.store.WriteDB().Exec(`UPDATE agent_registrations SET last_seen='2000-01-01T00:00:00Z'`)
	require.NoError(t, err)
	captureStdout(t, func() { _ = runInbox("lane", false, 1, "") })
	rows := registrationJSON(t)
	assert.NotEqual(t, "2000-01-01T00:00:00Z", rows[0]["last_seen"])

	_, err = app.store.WriteDB().Exec(`UPDATE agent_registrations SET last_seen='2000-01-01T00:00:00Z'`)
	require.NoError(t, err)
	t.Setenv("CODEX_THREAD_ID", "thread-OTHER")
	captureStdout(t, func() { _ = runInbox("lane", false, 1, "") })
	assert.Equal(t, "2000-01-01T00:00:00Z", registrationJSON(t)[0]["last_seen"])
}

func doctorChecks(t *testing.T) map[string]DoctorCheck {
	t.Helper()
	t.Setenv(transport.EnvDSN, "")
	var out, diag bytes.Buffer
	app.jsonOutput = true
	defer func() { app.jsonOutput = false }()
	_ = runSyncDoctor(t.Context(), &out, &diag, nil, transport.Options{})
	var report DoctorReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	got := map[string]DoctorCheck{}
	for _, c := range report.Checks {
		got[c.Name] = c
	}
	return got
}

func TestSyncDoctor_StaleAndMCPOnlyRegistrations_WarnWithFix(t *testing.T) {
	initRegistrationApp(t)
	captureStdout(t, func() { require.NoError(t, runAgentRegister("dead-lane", sessionOptions{Session: "s"})) })
	captureStdout(t, func() { require.NoError(t, runAgentRegister("mcp-lane", sessionOptions{Harness: "pi", Session: "x"})) })
	_, err := app.store.WriteDB().Exec(`UPDATE agent_registrations SET pid=2147483000, pid_start_time='gone' WHERE agent_id='dead-lane'`)
	require.NoError(t, err)
	_, err = app.store.WriteDB().Exec(`UPDATE agent_registrations SET origin='mcp', session_handle=NULL, pid=NULL, pid_start_time=NULL WHERE agent_id='mcp-lane'`)
	require.NoError(t, err)

	checks := doctorChecks(t)
	stale, mcp := checks["agents.stale"], checks["agents.mcp-only"]
	assert.True(t, stale.Pass)
	assert.True(t, stale.Warn)
	assert.Contains(t, stale.Detail, "dead-lane")
	assert.Contains(t, stale.Fix, "mtix agent unregister dead-lane --force")
	assert.True(t, mcp.Warn)
	assert.Contains(t, mcp.Detail, "mcp-lane")
	assert.Contains(t, mcp.Fix, "mtix agent register mcp-lane")
}

func TestSyncDoctor_RegistrationUnseenOverDayAndDead_Deleted(t *testing.T) {
	initRegistrationApp(t)
	captureStdout(t, func() { require.NoError(t, runAgentRegister("old-lane", sessionOptions{Session: "s"})) })
	_, err := app.store.WriteDB().Exec(`UPDATE agent_registrations SET pid=2147483000, pid_start_time='gone', last_seen='2020-01-01T00:00:00Z'`)
	require.NoError(t, err)
	checks := doctorChecks(t)
	assert.Contains(t, checks["agents.stale"].Detail, "expired")
	assert.Empty(t, registrationJSON(t))
}

func TestSyncDoctor_NoRegistrations_ChecksPass(t *testing.T) {
	initRegistrationApp(t)
	checks := doctorChecks(t)
	assert.True(t, checks["agents.stale"].Pass)
	assert.False(t, checks["agents.stale"].Warn)
	assert.False(t, checks["agents.mcp-only"].Warn)
	_ = os.Getpid()
}

func TestAgentRegisterHelp_ListsSessionFlags(t *testing.T) {
	cmd := newAgentRegisterCmd()
	for _, f := range []string{"harness", "session", "take-over"} {
		assert.NotNil(t, cmd.Flags().Lookup(f), f)
	}
	assert.Contains(t, cmd.Long, "--take-over")
	un := newAgentUnregisterCmd()
	assert.NotNil(t, un.Flags().Lookup("force"))
	list := newAgentListCmd()
	assert.NotNil(t, list.Flags().Lookup("registrations"))
}

func TestAgentRegister_NoOperatorDirectory_WarnsAndStillRegistersAgent(t *testing.T) {
	initRegistrationApp(t)
	t.Setenv("XDG_CONFIG_HOME", "relative/path") // an unusable operator directory
	t.Setenv("APPDATA", "relative")
	app.jsonOutput = true
	out := captureStdout(t, func() { require.NoError(t, runAgentRegister("lane", sessionOptions{})) })
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	assert.Equal(t, "registered", got["status"])
	assert.NotEmpty(t, got["session_warning"])
	assert.NotContains(t, got, "epoch")

	app.jsonOutput = false
	errText := captureStderr(t, func() { captureStdout(t, func() { require.NoError(t, runAgentRegister("lane", sessionOptions{})) }) })
	assert.Contains(t, errText, "session not registered")
}

func TestAgentList_WithoutRegistrationsFlag_Refused(t *testing.T) {
	initRegistrationApp(t)
	cmd := newAgentListCmd()
	cmd.SetArgs([]string{})
	require.Error(t, cmd.Execute())
}

func TestAgentRegister_InvalidHarness_Rejected(t *testing.T) {
	initRegistrationApp(t)
	err := runAgentRegister("lane", sessionOptions{Harness: "Bad Harness"})
	require.ErrorIs(t, err, model.ErrInvalidInput)
}

func TestAgentUnregister_JSON_ReportsStatus(t *testing.T) {
	initRegistrationApp(t)
	captureStdout(t, func() { require.NoError(t, runAgentRegister("lane", sessionOptions{Session: "s"})) })
	app.jsonOutput = true
	out := captureStdout(t, func() { require.NoError(t, runAgentUnregister("lane", true)) })
	assert.JSONEq(t, `{"agent_id":"lane","status":"unregistered"}`, out)
}

func TestListRegistrations_TextShowsDashForMissingMode(t *testing.T) {
	assert.Equal(t, "-", dash(""))
	assert.Equal(t, "plan", dash("plan"))
}

func TestOperatorTakeOverLog_KeyedByBoardAndAgent(t *testing.T) {
	initRegistrationApp(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)
	boardA, err := newOperatorTakeOverLog()
	require.NoError(t, err)
	require.NoError(t, boardA.Append(ctx, service.TakeOverRecord{AgentID: "a", At: now, DisplacedSession: "h1", NoticeAttempted: true}))

	other := *boardA
	other.board = "board:other"
	got, err := other.Recent(ctx, "a", now.Add(-time.Hour))
	require.NoError(t, err)
	assert.Empty(t, got, "another board's take-overs do not count against this board")
	require.NoError(t, other.Append(ctx, service.TakeOverRecord{AgentID: "a", At: now, DisplacedSession: "h2"}))
	got, err = boardA.Recent(ctx, "a", now.Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "h1", got[0].DisplacedSession)
	assert.Contains(t, boardKey(), "path:", "a board without a committed identity is keyed by its store path")
}

func TestBoardKey_CommittedIdentity_UsesBoardID(t *testing.T) {
	initRegistrationApp(t)
	id := "0c6f03d9-3633-4ab7-bcc9-b8da8d966023"
	require.NoError(t, os.WriteFile(filepath.Join(app.mtixDir, "board-id"), []byte(id+"\n"), 0o600))
	assert.Equal(t, "board:"+id, boardKey())
}
