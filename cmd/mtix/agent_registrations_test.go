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
	"github.com/hyper-swe/mtix/internal/procinfo"
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
	fakeSessionProcess(t, "codex")
}

// fakeSessionProcess makes the ancestor walk report a session process with the
// given executable name: the real parent of the test binary (alive, with its
// real start time), so liveness probes behave as in production.
func fakeSessionProcess(t *testing.T, name string) {
	t.Helper()
	pid := os.Getppid()
	start, err := procinfo.StartTime(pid)
	require.NoError(t, err)
	old := sessionWalker
	t.Cleanup(func() { sessionWalker = old })
	sessionWalker = func() (walkedSession, bool) {
		return walkedSession{PID: pid, Name: name, Harness: procinfo.Identify(procinfo.Proc{Name: name, Argv0: name}), StartTime: start}, true
	}
}

func envFrom(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

var harnessEnv = map[string]string{"claude": "CLAUDE_CODE_MESSAGING_SOCKET", "codex": "CODEX_THREAD_ID", "pi": "PI_SESSION_ID"}
var harnessName = map[string]string{"claude": "claude-code", "codex": "codex", "pi": "pi"}

func sessionProc(name string) walkedSession {
	return walkedSession{PID: 50, Name: name, Harness: procinfo.Identify(procinfo.Proc{Name: name, Argv0: name}), StartTime: "s50"}
}

func TestDetectSession_ByProcessIdentity_PicksOwnHarnessAndHandle(t *testing.T) {
	tests := []struct {
		name                    string
		env                     map[string]string
		opts                    sessionOptions
		proc                    walkedSession
		sure                    bool
		harness, handle, origin string
		uncertain               bool
	}{
		{"claude process", map[string]string{"CLAUDE_CODE_MESSAGING_SOCKET": "/tmp/s1"}, sessionOptions{}, sessionProc("claude"), true, "claude-code", "/tmp/s1", "session-env", false},
		{"codex process", map[string]string{"CODEX_THREAD_ID": "th-1"}, sessionOptions{}, sessionProc("/usr/bin/codex"), true, "codex", "th-1", "session-env", false},
		{"pi process", map[string]string{"PI_SESSION_ID": "pi-9"}, sessionOptions{}, sessionProc("pi"), true, "pi", "pi-9", "session-env", false},
		{"login-style name", map[string]string{"CODEX_THREAD_ID": "th-1"}, sessionOptions{}, sessionProc("-Codex"), true, "codex", "th-1", "session-env", false},
		{"typed session is manual", map[string]string{"CODEX_THREAD_ID": "th-1"}, sessionOptions{Session: "typed"}, sessionProc("codex"), true, "codex", "typed", "manual", false},
		{"typed harness wins", map[string]string{"CODEX_THREAD_ID": "th-1"}, sessionOptions{Harness: "pi", Session: "x"}, sessionProc("codex"), true, "pi", "x", "manual", false},
		{"typed session rescues an unknown process", map[string]string{}, sessionOptions{Session: "x"}, sessionProc("node"), true, "unknown", "x", "manual", false},
		{"known process without its variable", map[string]string{"CLAUDE_CODE_MESSAGING_SOCKET": "/tmp/outer"}, sessionOptions{}, sessionProc("codex"), true, "codex", "", "manual", true},
		{"unknown process borrows nothing", map[string]string{"CODEX_THREAD_ID": "th", "CLAUDE_CODE_MESSAGING_SOCKET": "/s"}, sessionOptions{}, sessionProc("node"), true, "unknown", "", "manual", true},
		{"unsure walk borrows nothing", map[string]string{"CODEX_THREAD_ID": "th"}, sessionOptions{}, sessionProc("codex"), false, "unknown", "", "manual", true},
		{"nothing detected", map[string]string{}, sessionOptions{}, walkedSession{PID: 50}, false, "unknown", "", "manual", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectSession(envFrom(tt.env), tt.opts, tt.proc, tt.sure)
			assert.Equal(t, tt.harness, got.Harness)
			assert.Equal(t, tt.handle, got.Handle)
			assert.Equal(t, tt.origin, got.Origin)
			assert.Equal(t, tt.uncertain, got.Uncertain)
		})
	}
}

// Every ordered pair of harnesses nested in each other: the environment holds
// both variables (the inner one inherited the outer's), and the inner harness
// owns the process, so detection must return the inner harness and handle.
func TestDetectSession_NestedHarnesses_EveryPairBothOrders_UsesInnerHandle(t *testing.T) {
	for outer := range harnessEnv {
		for inner := range harnessEnv {
			if outer == inner {
				continue
			}
			t.Run(outer+" outer, "+inner+" inner", func(t *testing.T) {
				env := map[string]string{harnessEnv[outer]: "outer-handle", harnessEnv[inner]: "inner-handle"}
				got := detectSession(envFrom(env), sessionOptions{}, sessionProc(inner), true)
				assert.Equal(t, harnessName[inner], got.Harness)
				assert.Equal(t, "inner-handle", got.Handle)
				assert.False(t, got.Uncertain)
			})
			t.Run(outer+" outer, "+inner+" inner without its own variable", func(t *testing.T) {
				got := detectSession(envFrom(map[string]string{harnessEnv[outer]: "outer-handle"}), sessionOptions{}, sessionProc(inner), true)
				assert.Empty(t, got.Handle, "the outer harness's handle is never borrowed")
				assert.True(t, got.Uncertain)
			})
		}
	}
}

// Real values observed on macOS: the kernel name of Claude Code is its version
// string; argv[0] and the exec path identify it. A lane started from a Codex
// session sits below codex's node child and must resolve to codex.
func TestDetectSession_RealObservedProcessTables_ResolveHarness(t *testing.T) {
	claude := table{
		91: {PPID: 26, Name: "/bin/bash", StartTime: "b"},
		26: {PPID: 10, Name: "2.1.296", Argv0: "claude", Exe: "/Users/u/.local/bin/claude", StartTime: "s26"},
	}
	proc, sure := procinfo.FindSession(claude.read, 91)
	got := detectSession(envFrom(map[string]string{"CLAUDE_CODE_MESSAGING_SOCKET": "/tmp/claude-sock"}), sessionOptions{}, proc, sure)
	assert.Equal(t, "claude-code", got.Harness)
	assert.Equal(t, "/tmp/claude-sock", got.Handle)
	assert.False(t, got.Uncertain)

	codex := table{
		91: {PPID: 80, Name: "bash", StartTime: "b"},
		80: {PPID: 50, Name: "node", Argv0: "node", StartTime: "n"},
		50: {PPID: 10, Name: "codex", Argv0: "codex", StartTime: "s50"},
	}
	proc, sure = procinfo.FindSession(codex.read, 91)
	got = detectSession(envFrom(map[string]string{"CODEX_THREAD_ID": "th-1"}), sessionOptions{}, proc, sure)
	assert.Equal(t, "codex", got.Harness)
	assert.Equal(t, 50, proc.PID, "the codex process, not its node child")
	assert.Equal(t, "th-1", got.Handle)
}

type table map[int]procinfo.Proc

func (t table) read(pid int) (procinfo.Proc, error) {
	p, ok := t[pid]
	if !ok {
		return procinfo.Proc{}, procinfo.ErrNoProcess
	}
	return p, nil
}

func TestCurrentSession_UncertainSession_RecordsUnverifiedStartTime(t *testing.T) {
	old := sessionWalker
	t.Cleanup(func() { sessionWalker = old })
	sessionWalker = func() (walkedSession, bool) { return sessionProc("node"), true }
	det, pid, start := currentSession(sessionOptions{})
	assert.True(t, det.Uncertain)
	assert.Equal(t, 50, pid)
	assert.Equal(t, service.UnverifiedStartPrefix+"s50", start, "a real but unidentified process probes as unknown")

	sessionWalker = func() (walkedSession, bool) { return walkedSession{PID: 77}, false }
	_, pid, start = currentSession(sessionOptions{})
	assert.Equal(t, 77, pid)
	assert.Empty(t, start, "an unsure walk recorded a short-lived parent: no start time at all")

	t.Setenv("CODEX_THREAD_ID", "th")
	sessionWalker = func() (walkedSession, bool) { return sessionProc("codex"), true }
	det, _, start = currentSession(sessionOptions{})
	assert.False(t, det.Uncertain)
	assert.Equal(t, "s50", start)
}

func TestAgentRegister_NestedLanesSharingOuterHandle_SecondIsRefused(t *testing.T) {
	initRegistrationApp(t)
	// Two Claude lanes started from one Codex shell: both inherit CODEX_THREAD_ID.
	t.Setenv("CODEX_THREAD_ID", "th-outer")
	t.Setenv("CLAUDE_CODE_MESSAGING_SOCKET", "/tmp/lane-a")
	fakeSessionProcess(t, "claude")
	captureStdout(t, func() { require.NoError(t, runAgentRegister("lane", sessionOptions{})) })
	rows := registrationJSON(t)
	assert.Equal(t, "claude-code", rows[0]["harness"])
	assert.Equal(t, "/tmp/lane-a", rows[0]["session_handle"])

	t.Setenv("CLAUDE_CODE_MESSAGING_SOCKET", "/tmp/lane-b")
	err := runAgentRegister("lane", sessionOptions{})
	require.ErrorIs(t, err, model.ErrAgentStillActive, "a sibling lane must not displace the first")
}

func TestAgentRegister_UncertainSession_NoHandleAndTakeOverStillWorks(t *testing.T) {
	initRegistrationApp(t)
	t.Setenv("CODEX_THREAD_ID", "th-outer")
	fakeSessionProcess(t, "node")
	captureStdout(t, func() { require.NoError(t, runAgentRegister("lane", sessionOptions{})) })
	rows := registrationJSON(t)
	assert.NotContains(t, rows[0], "session_handle")
	assert.Equal(t, "unknown", rows[0]["liveness"])

	captureStdout(t, func() { require.NoError(t, runAgentRegister("lane", sessionOptions{})) }) // same caller: a repeat

	sessionWalker = func() (walkedSession, bool) { return walkedSession{PID: 4242, Name: "node", StartTime: "x"}, true }
	err := runAgentRegister("lane", sessionOptions{})
	require.ErrorIs(t, err, model.ErrAgentStillActive, "unknown liveness is never replaced silently by another process")
	captureStdout(t, func() { require.NoError(t, runAgentRegister("lane", sessionOptions{TakeOver: true})) })
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
	var out bytes.Buffer
	app.jsonOutput = true
	defer func() { app.jsonOutput = false }()
	_ = runSyncDoctor(t.Context(), &out, nil, transport.Options{})
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
