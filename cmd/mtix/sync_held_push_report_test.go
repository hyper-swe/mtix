// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// `mtix sync status` and `mtix sync doctor` report held push events
// (MTIX-95.12 acceptance 4).

// holdOversizedEvents creates n nodes TEST-1..TEST-n whose prompts are over
// the wire cap, pushes to a fake hub, which holds their create events, and
// returns the held event ids.
func holdOversizedEvents(t *testing.T, n int) []string {
	t.Helper()
	ids := make([]string, n)
	for i := range ids {
		require.NoError(t, runCreate(fmt.Sprintf("big %d", i), "", "", 3, "", overWireCap(), "", "", ""))
		ids[i] = eventIDFor(t, fmt.Sprintf("TEST-%d", i+1), model.OpCreateNode)
	}
	var stderr bytes.Buffer
	_, _, _, _, err := pushLoop(context.Background(), &stderr, newFakePushHub(), app.store)
	require.NoError(t, err)
	return ids
}

// TestRunSyncStatus_HeldPushEvents_ShownInTableAndJSON: status counts held
// push events as held_push_events in --json and a "held push events" table
// row, zero included; they stay in pending and are not counted as
// quarantined pulled events.
func TestRunSyncStatus_HeldPushEvents_ShownInTableAndJSON(t *testing.T) {
	for _, n := range []int{0, 2} {
		t.Run(fmt.Sprintf("%d held", n), func(t *testing.T) {
			initTestApp(t)
			ctx := context.Background()
			holdOversizedEvents(t, n)

			var out, errOut bytes.Buffer
			app.jsonOutput = true
			require.NoError(t, runSyncStatus(ctx, &out, &errOut))
			var got map[string]any
			require.NoError(t, json.Unmarshal(out.Bytes(), &got))
			require.Contains(t, got, "held_push_events")
			require.Equal(t, float64(n), got["held_push_events"])
			require.Equal(t, float64(n), got["pending"], "held push events stay pending")
			require.Equal(t, float64(0), got["quarantined_events"], "they are not pulled events")

			out.Reset()
			app.jsonOutput = false
			require.NoError(t, runSyncStatus(ctx, &out, &errOut))
			require.Regexp(t, regexp.MustCompile(fmt.Sprintf(`(?m)^held push events\s+%d$`, n)), out.String())
		})
	}
}

// TestRunSyncDoctor_HeldPushEvents_ListedWithGuidance: doctor has a "held
// push events" check that passes with none held and otherwise fails, listing
// the held events (at most five, then how many more) with the guidance to
// shorten or split the field and the command that lists them. Held events
// older than an hour do not fail "queue draining": the queue still drains.
func TestRunSyncDoctor_HeldPushEvents_ListedWithGuidance(t *testing.T) {
	tests := []struct {
		name       string
		held       int
		wantPass   bool
		wantDetail []string
	}{
		{"none held", 0, true, []string{"ok"}},
		{"one held", 1, false, []string{"1 push events held", "TEST-1 create_node: stop editing this task and escalate",
			"prompt", "mtix sync quarantine list"}},
		{"more held than listed", 7, false, []string{"7 push events held", "TEST-5 create_node: stop editing", "and 2 more",
			"mtix sync quarantine list"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			ctx := context.Background()
			ids := holdOversizedEvents(t, tt.held)
			for _, id := range ids {
				_, err := app.store.WriteDB().ExecContext(ctx,
					`UPDATE sync_events SET created_at = ? WHERE event_id = ?`,
					time.Now().Add(-2*time.Hour).UTC().Format(time.RFC3339), id)
				require.NoError(t, err)
			}

			var out, errOut bytes.Buffer
			app.jsonOutput = true
			err := runSyncDoctor(ctx, &out, &errOut, nil, transport.Options{})
			require.ErrorIs(t, err, errDoctorChecksFailed, "no hub is reachable in this test")
			check := doctorCheckNamed(t, out.Bytes(), "held push events")
			require.Equal(t, tt.wantPass, check.Pass, check.Detail)
			for _, want := range tt.wantDetail {
				require.Contains(t, check.Detail, want)
			}
			if tt.held > 5 {
				require.NotContains(t, check.Detail, "TEST-6 create_node", "at most five are listed")
			}
			require.True(t, doctorCheckNamed(t, out.Bytes(), "queue draining").Pass,
				"held push events do not fail the queue check")
			require.True(t, doctorCheckNamed(t, out.Bytes(), "quarantined events").Pass,
				"held push events are not quarantined pulled events")

			out.Reset()
			app.jsonOutput = false
			_ = runSyncDoctor(ctx, &out, &errOut, nil, transport.Options{})
			mark := "PASS"
			if !tt.wantPass {
				mark = "FAIL"
			}
			require.Regexp(t, regexp.MustCompile(`(?m)^\[`+mark+`\] held push events\s+\S`), out.String())
		})
	}
}

// TestPrintHeldPushEvents_Count_ReportedWhenAny: at the end of a push,
// stdout names how many events are held, and nothing when none is.
func TestPrintHeldPushEvents_Count_ReportedWhenAny(t *testing.T) {
	for _, n := range []int{0, 2} {
		t.Run(fmt.Sprintf("%d held", n), func(t *testing.T) {
			initTestApp(t)
			holdOversizedEvents(t, n)
			var out, errOut bytes.Buffer
			printHeldPushEvents(context.Background(), &out, &errOut, app.store)
			require.Empty(t, errOut.String())
			if n == 0 {
				require.Empty(t, out.String())
				return
			}
			require.Equal(t,
				"held: 2 events not pushed because the hub would refuse them or they depend on a held task creation (see mtix sync doctor)\n",
				out.String())
		})
	}
}

// TestAppendHeldPushCheck_NoStore_Fails: without a local store the check
// fails like the other local checks.
func TestAppendHeldPushCheck_NoStore_Fails(t *testing.T) {
	r := appendHeldPushCheck(context.Background(), DoctorReport{OverallPass: true}, nil)
	require.False(t, r.OverallPass)
	require.Equal(t, []DoctorCheck{{Name: "held push events", Pass: false, Detail: "local store not initialized"}}, r.Checks)
}

// TestCheckHeldPushEvents_LongReason_Shortened: a listed reason is cut to
// heldPushReasonRunes runes, marked with "...", so the detail stays short;
// `mtix sync quarantine list` keeps the full reason.
func TestCheckHeldPushEvents_LongReason_Shortened(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	reason := strings.Repeat("a", heldPushReasonRunes) + "TAIL"
	require.NoError(t, app.store.HoldPushEvents(ctx, []sqlite.QuarantinedEvent{
		{EventID: "0193fb00-0000-7000-8000-0000000000bb", RawEvent: "{}", Reason: reason}}, "v"))

	ok, detail := checkHeldPushEvents(ctx, app.store)
	require.False(t, ok)
	require.Contains(t, detail, strings.Repeat("a", heldPushReasonRunes)+"...")
	require.NotContains(t, detail, "TAIL")
}

// TestRunSyncDoctor_HeldPushEvents_GuidanceMatchesOpAndReason: the doctor's
// fix for each held event matches why it is held (MTIX-95.12 round 2):
// a held creation cannot be fixed by an edit, a dependent resolves with its
// creation, a clock hold needs the clock fixed, and only a held field edit
// is fixed by shortening or splitting the field.
func TestRunSyncDoctor_HeldPushEvents_GuidanceMatchesOpAndReason(t *testing.T) {
	big := overWireCap()
	tests := []struct {
		name  string
		setup func(t *testing.T)
		item  string // "<node> <op>: <guidance>" as the detail lists it
		not   string
	}{
		{"held creation", func(t *testing.T) {
			require.NoError(t, runCreate("held", "", "", 3, "", big, "", "", ""))
		}, "TEST-1 create_node: stop editing this task and escalate: its creation cannot be sent, and there is no automatic re-send",
			"the next edit pushes"},
		{"held field edit", func(t *testing.T) {
			require.NoError(t, runCreate("small", "", "", 3, "", "", "", "", ""))
			pushToFakeHub(t)
			require.NoError(t, runPrompt("TEST-1", big))
		}, "TEST-1 set_prompt: shorten or split the field; the next edit pushes", "stop editing"},
		{"held dependent", func(t *testing.T) {
			require.NoError(t, runCreate("held", "", "", 3, "", big, "", "", ""))
			require.NoError(t, runClaim("TEST-1", "agent-a"))
		}, "TEST-1 claim: resolves with the held creation it depends on", "shorten or split"},
		{"held link", func(t *testing.T) {
			require.NoError(t, runCreate("small", "", "", 3, "", "", "", "", ""))
			require.NoError(t, runCreate("held", "", "", 3, "", big, "", "", ""))
			require.NoError(t, runDepAdd("TEST-1", "TEST-2", "related"))
		}, "TEST-1 link_dep: a link made while a task creation is held; it pushes once no creation made before it is held (this version names a link's target by number)",
			"resolves with the held creation it depends on"},
		{"held for the clock", func(t *testing.T) {
			require.NoError(t, runCreate("ahead", "", "", 3, "", "", "", "", ""))
			stampInFuture(t, eventIDFor(t, "TEST-1", model.OpCreateNode))
		}, "TEST-1 create_node: check this machine's clock; the event pushes once its stamp is within 24 h of the clock", "stop editing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			tt.setup(t)
			pushToFakeHub(t)
			ok, detail := checkHeldPushEvents(context.Background(), app.store)
			require.False(t, ok)
			require.Contains(t, detail, tt.item)
			require.NotContains(t, detail, tt.not)
		})
	}
}

// pushToFakeHub pushes the pending events to a fake hub.
func pushToFakeHub(t *testing.T) {
	t.Helper()
	var stderr bytes.Buffer
	_, _, _, _, err := pushLoop(context.Background(), &stderr, newFakePushHub(), app.store)
	require.NoError(t, err)
}

// TestPushAndReport_HeldEvents_EndLineCountsThem: the summary `mtix sync
// push` prints ends with how many events push holds, and has no such line
// when none is held (review r1 R5).
func TestPushAndReport_HeldEvents_EndLineCountsThem(t *testing.T) {
	tests := []struct {
		name   string
		prompt string
		want   string
	}{
		{"one event held", overWireCap(),
			"push complete: 1 events pushed across 1 batches; 0 renumbered, 0 conflicts surfaced\n" +
				"held: 1 events not pushed because the hub would refuse them or they depend on a held task creation (see mtix sync doctor)\n"},
		{"none held", "",
			"push complete: 2 events pushed across 1 batches; 0 renumbered, 0 conflicts surfaced\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			require.NoError(t, runCreate("small", "", "", 3, "", "", "", "", ""))
			require.NoError(t, runCreate("second", "", "", 3, "", tt.prompt, "", "", ""))
			var out, errOut bytes.Buffer
			require.NoError(t, pushAndReport(context.Background(), &out, &errOut, newFakePushHub(), app.store))
			require.Equal(t, tt.want, out.String())
		})
	}
}
