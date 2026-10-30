// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Sender-visible wake delivery status on mtix comment --to, mtix inbox status and the
// wake.listener doctor rows (MTIX-112.40.6). Ledger-row states (nudged, unconfirmed,
// confirmed, held, undeliverable) are SEEDED-ONLY until the wake scanner (PR 256) writes
// rows in production; here they are seeded straight into the ledger.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/testutil"
)

// seededWakeHost returns the host id and uid the production code resolves.
func seededWakeHost(t *testing.T) (string, string) {
	t.Helper()
	env, err := oplocal.FromProcess()
	require.NoError(t, err)
	host, err := oplocal.New(env).HostID()
	require.NoError(t, err)
	uid, err := currentUserID()
	require.NoError(t, err)
	return host, uid
}

func seedWakeRegistration(t *testing.T, agent string) {
	t.Helper()
	host, uid := seededWakeHost(t)
	_, err := app.store.RegisterSessionAtomic(t.Context(), sqlite.RegistrationKey{AgentID: agent, HostID: host, UID: uid},
		func(_ *sqlite.Registration, _ int64) (*sqlite.Registration, error) {
			return &sqlite.Registration{AgentID: agent, HostID: host, UID: uid, Harness: "claude", Origin: "test",
				RegisteredAt: "2026-10-29T00:00:00Z", LastSeen: "2026-10-29T00:00:00Z", RegistrarVersion: "test", AttachProtocol: 1}, nil
		})
	require.NoError(t, err)
}

func seedWakeDispatcher(t *testing.T) {
	t.Helper()
	host, uid := seededWakeHost(t)
	require.NoError(t, app.store.RecordDispatcherHeartbeat(t.Context(), sqlite.DispatcherHeartbeat{
		HostID: host, UID: uid, PID: 4242, Protocol: 1, Version: "test", SeenAt: "2026-10-29T00:00:00Z"}))
}

// seedWakeRow writes one ledger row whose outcome time is age ago.
func seedWakeRow(t *testing.T, agent string, seq int64, res model.WakeResult, age time.Duration) {
	t.Helper()
	host, uid := seededWakeHost(t)
	app.store.SetClock(func() time.Time { return time.Now().Add(-age) })
	t.Cleanup(func() { app.store.SetClock(time.Now) })
	key := model.WakeKey{AgentID: agent, HostID: host, UID: uid, EventSeq: seq, Epoch: 1}
	attempt, won, err := app.store.ClaimWake(t.Context(), key, time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	if res.Outcome == model.WakeConfirmed { // confirmed is reached only by upgrading a sent row
		require.NoError(t, app.store.RecordWakeResult(t.Context(), attempt, model.WakeResult{Outcome: model.WakeSent, Route: res.Route}))
		require.NoError(t, app.store.UpgradeWakeConfirmed(t.Context(), key))
		return
	}
	require.NoError(t, app.store.RecordWakeResult(t.Context(), attempt, res))
}

func wakeStatusApp(t *testing.T) string {
	t.Helper()
	initTestApp(t)
	testutil.OperatorState(t)
	return seedOneNode(t)
}

func commentWakeJSON(t *testing.T, id, to string) map[string]any {
	t.Helper()
	app.jsonOutput = true
	out := captureStdout(t, func() { require.NoError(t, runComment(id, "ping", to)) })
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	return got
}

func TestRunComment_ToAgent_JSONCarriesWakeState(t *testing.T) {
	id := wakeStatusApp(t)
	got := commentWakeJSON(t, id, "worker")
	require.Equal(t, "annotation_added", got["status"])
	require.Equal(t, "worker", got["to"])
	wake, ok := got["wake"].(map[string]any)
	require.True(t, ok, "wake object present")
	require.Equal(t, "no-live-session", wake["state"])
	require.Contains(t, wake["fix"], "mtix agent register worker")
}

func TestRunComment_WithoutTo_HasNoWakeField(t *testing.T) {
	id := wakeStatusApp(t)
	got := commentWakeJSON(t, id, "")
	require.NotContains(t, got, "wake")
}

func TestRunComment_ToAgent_StateFollowsRegistrationDispatcherAndSwitch(t *testing.T) {
	id := wakeStatusApp(t)
	seedWakeRegistration(t, "worker")
	require.Equal(t, "no-dispatcher", commentWakeJSON(t, id, "worker")["wake"].(map[string]any)["state"])
	seedWakeDispatcher(t)
	require.Equal(t, "queued", commentWakeJSON(t, id, "worker")["wake"].(map[string]any)["state"])

	writeHostWakeSetting(t, "wake.live", false)
	require.Equal(t, "off", commentWakeJSON(t, id, "worker")["wake"].(map[string]any)["state"])
}

// writeHostWakeSetting stores one host wake value in the operator state production reads.
func writeHostWakeSetting(t *testing.T, key string, value any) {
	t.Helper()
	env, err := oplocal.FromProcess()
	require.NoError(t, err)
	require.NoError(t, oplocal.New(env).WriteJSON("wake", map[string]any{"host": map[string]any{key: value}}))
}

func TestRunComment_ToAgent_TextPrintsWakeLine(t *testing.T) {
	id := wakeStatusApp(t)
	app.jsonOutput = false
	out := captureStdout(t, func() { require.NoError(t, runComment(id, "ping", "worker")) })
	require.Contains(t, out, "→ worker inbox")
	require.Contains(t, out, "wake: no-live-session")
	require.Contains(t, out, "fix: mtix agent register worker")
}

func inboxStatusJSON(t *testing.T, args ...string) map[string]any {
	t.Helper()
	app.jsonOutput = true
	out := outputInboxCommand(t, append([]string{"status"}, args...)...)
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &got), out)
	return got
}

func TestInboxStatus_SeededRows_ShowEveryStateWithAge(t *testing.T) {
	wakeStatusApp(t)
	seedWakeRegistration(t, "worker")
	seedWakeDispatcher(t)
	seedWakeRow(t, "worker", 1, model.WakeResult{Outcome: model.WakeSent, Route: "hook-waiter"}, 2*time.Minute)
	seedWakeRow(t, "worker", 2, model.WakeResult{Outcome: model.WakeHeld, Route: "claude-socket"}, time.Hour)
	seedWakeRow(t, "worker", 3, model.WakeResult{Outcome: model.WakeUndeliverable, Route: "inbox-only", Reason: "no-live-route"}, time.Hour)

	got := inboxStatusJSON(t, "--agent", "worker")
	require.Equal(t, "worker", got["agent"])
	require.Equal(t, "queued", got["current"].(map[string]any)["state"])
	recent := got["recent"].([]any)
	require.Len(t, recent, 3)
	first := recent[0].(map[string]any)
	require.EqualValues(t, 3, first["event_seq"])
	require.Equal(t, "undeliverable", first["state"])
	require.Equal(t, "no-live-route", first["reason"])
	require.Equal(t, "held", recent[1].(map[string]any)["state"])
	require.Equal(t, "unconfirmed", recent[2].(map[string]any)["state"], "sent 2 minutes ago, window 30 s")
	require.Greater(t, recent[2].(map[string]any)["age_seconds"], float64(100))
}

func TestInboxStatus_NeverWritesAnInboxEvent(t *testing.T) {
	wakeStatusApp(t)
	seedWakeRegistration(t, "worker")
	seedWakeRow(t, "worker", 1, model.WakeResult{Outcome: model.WakeSent, Route: "hook-waiter"}, 5*time.Minute)
	before, err := app.store.InboxList(t.Context(), "worker")
	require.NoError(t, err)
	_ = inboxStatusJSON(t, "--agent", "worker")
	after, err := app.store.InboxList(t.Context(), "worker")
	require.NoError(t, err)
	require.Equal(t, before, after, "unconfirmed is derived at read time, never an inbox event")
}

func TestInboxStatus_TextAndLimitAndValidation(t *testing.T) {
	wakeStatusApp(t)
	seedWakeRegistration(t, "worker")
	seedWakeRow(t, "worker", 1, model.WakeResult{Outcome: model.WakeConfirmed, Route: "claude-socket"}, time.Minute)
	seedWakeRow(t, "worker", 2, model.WakeResult{Outcome: model.WakeSent, Route: "hook-waiter"}, time.Second)
	app.jsonOutput = false
	out := outputInboxCommand(t, "status", "--agent", "worker", "--limit", "1")
	require.Contains(t, out, "current: ")
	require.Contains(t, out, "event 2")
	require.NotContains(t, out, "event 1")
	require.Contains(t, out, "nudged")

	cmd := newInboxCmd()
	cmd.SetArgs([]string{"status", "--agent", "worker", "--limit", "0"})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	require.ErrorIs(t, cmd.Execute(), model.ErrInvalidInput)
}

func TestInboxStatus_NoRows_SaysSoExplicitly(t *testing.T) {
	wakeStatusApp(t)
	app.jsonOutput = false
	out := outputInboxCommand(t, "status", "--agent", "nobody")
	require.Contains(t, out, "no wake rows")
	require.Contains(t, out, "no-live-session")
}

func TestInboxStatus_ConfirmWindowSetting_ChangesUnconfirmedBoundary(t *testing.T) {
	wakeStatusApp(t)
	seedWakeRegistration(t, "worker")
	seedWakeRow(t, "worker", 1, model.WakeResult{Outcome: model.WakeSent, Route: "hook-waiter"}, 45*time.Second)
	state := func() string {
		return inboxStatusJSON(t, "--agent", "worker")["recent"].([]any)[0].(map[string]any)["state"].(string)
	}
	require.Equal(t, "unconfirmed", state(), "default window is 30 s")
	writeHostWakeSetting(t, "wake.confirm_window", 120)
	require.Equal(t, "nudged", state())
}

func TestDoctorWakeListener_JSONRowsCarryIDStatusDetailFix(t *testing.T) {
	wakeStatusApp(t)
	seedWakeRegistration(t, "worker")
	svc, err := newConfiguredDoctorService()
	require.NoError(t, err)
	var out bytes.Buffer
	require.NoError(t, runDoctor(context.Background(), &out, svc, doctorOutputOptions{JSON: true}))
	var rows []service.DoctorResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &rows))
	byID := map[string]service.DoctorResult{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	for _, id := range []string{"wake.listener.routes", "wake.listener.dispatcher", "wake.listener.unconfirmed-rate",
		"wake.listener.hook-installed", "wake.listener.waiter-armed", "wake.listener.route-contract", "wake.listener.bypass-hold"} {
		require.Contains(t, byID, id)
		require.NotEmpty(t, byID[id].Fix, id)
	}
	require.Equal(t, service.DoctorWarn, byID["wake.listener.dispatcher"].Status)
	require.Contains(t, byID["wake.listener.dispatcher"].Fix, "mtix inbox --agent worker --wait")
	require.Equal(t, false, byID["wake.listener.waiter-armed"].Data["reachable"])
}

func TestConfigWakeConfirmWindow_DocumentedCommand_RunsWithoutTerminal(t *testing.T) {
	wakeStatusApp(t)
	seedWakeRegistration(t, "worker")
	seedWakeRow(t, "worker", 1, model.WakeResult{Outcome: model.WakeSent, Route: "hook-waiter"}, 45*time.Second)
	state := func() string {
		return inboxStatusJSON(t, "--agent", "worker")["recent"].([]any)[0].(map[string]any)["state"].(string)
	}
	require.Equal(t, "unconfirmed", state())
	// The exact commands the docs and the doctor fix print; the test process has no terminal.
	for _, args := range [][]string{{"wake.confirm_window", "60", "--agent", "worker"}, {"wake.confirm_window", "120"}} {
		cmd := newConfigSetCmd()
		cmd.SetArgs(args)
		require.NoError(t, cmd.Execute(), "mtix config set %v", args)
	}
	require.Equal(t, "nudged", state())
	del := newConfigDeleteCmd()
	del.SetArgs([]string{"wake.confirm_window"})
	require.NoError(t, del.Execute())
	del = newConfigDeleteCmd()
	del.SetArgs([]string{"wake.confirm_window", "--agent", "worker"})
	require.NoError(t, del.Execute())
	require.Equal(t, "unconfirmed", state(), "back to the 30 s default")
}

func TestInboxStatus_UnreadableSettings_ShowsUnknownInsteadOfAborting(t *testing.T) {
	wakeStatusApp(t)
	env, err := oplocal.FromProcess()
	require.NoError(t, err)
	dir, err := oplocal.New(env).Path()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "wake.json"), []byte("{not json"), 0o600))
	got := inboxStatusJSON(t, "--agent", "worker")
	require.Equal(t, "unknown", got["current"].(map[string]any)["state"])
	recent := got["recent"].([]any)
	require.Len(t, recent, 1)
	require.Equal(t, "unknown", recent[0].(map[string]any)["state"])
	require.NotEmpty(t, recent[0].(map[string]any)["reason"])
}

func TestInboxStatus_StoredOutOfRangeWindow_IsUnknownNotUnconfirmed(t *testing.T) {
	// A hand-edited wake.json can hold a value normalization would refuse; status must not trust it.
	for _, bad := range []any{0, -5, 86401, 9999999999} {
		wakeStatusApp(t)
		seedWakeRegistration(t, "worker")
		seedWakeRow(t, "worker", 1, model.WakeResult{Outcome: model.WakeSent, Route: "hook-waiter"}, time.Hour)
		writeHostWakeSetting(t, "wake.confirm_window", bad)
		got := inboxStatusJSON(t, "--agent", "worker")
		require.Equal(t, "unknown", got["current"].(map[string]any)["state"], "%v", bad)
		require.Contains(t, got["current"].(map[string]any)["reason"], "wake.confirm_window", "%v", bad)
	}
}

// everyFixParses asserts each semicolon-separated fix is a real command of this build, or the fix is empty.
func everyFixParses(t *testing.T, id, fix string) {
	t.Helper()
	require.NotContains(t, fix, "<", id+" fix must run as printed")
	for _, part := range strings.Split(fix, ";") {
		fields := strings.Fields(part)
		if len(fields) == 0 {
			continue
		}
		require.Equal(t, "mtix", fields[0], id)
		found, _, err := newRootCmd().Find(fields[1:])
		require.NoError(t, err, id+": "+part)
		require.NotEqual(t, "mtix", found.Name(), id+" fix names no command of this build: "+part)
	}
}

func TestDoctorWakeListener_EveryFixParsesAgainstTheCommandTree(t *testing.T) {
	for _, registered := range []bool{false, true} {
		wakeStatusApp(t)
		if registered {
			seedWakeRegistration(t, "worker")
		}
		svc, err := newConfiguredDoctorService()
		require.NoError(t, err)
		var out bytes.Buffer
		require.NoError(t, runDoctor(context.Background(), &out, svc, doctorOutputOptions{JSON: true}))
		var rows []service.DoctorResult
		require.NoError(t, json.Unmarshal(out.Bytes(), &rows))
		seen := 0
		for _, r := range rows {
			if strings.HasPrefix(r.ID, "wake.listener.") {
				everyFixParses(t, r.ID, r.Fix)
				seen++
			}
		}
		require.Equal(t, 7, seen)
	}
}

func TestRunComment_ToAgent_FixesParseAgainstTheCommandTree(t *testing.T) {
	id := wakeStatusApp(t)
	wake := commentWakeJSON(t, id, "worker")["wake"].(map[string]any)
	everyFixParses(t, "comment --to", wake["fix"].(string))
}
