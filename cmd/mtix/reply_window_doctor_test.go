// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
)

func openTestWindow(t *testing.T, src *hubContactSource, ttl time.Duration) {
	t.Helper()
	w := oplocal.ReplyWindow{AwaitingFrom: "peer", Sender: "me", Node: "N-1", OpenedAt: schedT0, ReplyBy: schedT0.Add(ttl)}
	require.NoError(t, service.OpenReplyWindow(context.Background(), src.state, src.key(), w))
}

func TestHubContactSource_Snapshot_ReplyWindowFeedsCadence(t *testing.T) {
	src := hubSourceFixture(t, "")
	in, err := src.snapshot()
	require.NoError(t, err)
	assert.Equal(t, service.ReplyInputs{}, in.Reply, "no window: the cadence inputs are unchanged")

	openTestWindow(t, src, 10*time.Minute)
	in, err = src.snapshot()
	require.NoError(t, err)
	assert.True(t, in.Reply.WindowOpen)

	src.now = func() time.Time { return schedT0.Add(11 * time.Minute) }
	in, err = src.snapshot()
	require.NoError(t, err)
	assert.False(t, in.Reply.WindowOpen, "the TTL passed")
}

func TestHubContactSource_Snapshot_UnreadableReplyState_NeverWidensCadence(t *testing.T) {
	src := hubSourceFixture(t, "")
	require.NoError(t, src.state.WriteJSON("replywin-"+src.key(), "not an object"))
	in, err := src.snapshot()
	require.NoError(t, err, "a damaged reply record must not stop activity-driven contact")
	assert.Equal(t, service.ReplyInputs{}, in.Reply)
}

func TestHubContactRow_ReplyStatus_TextAndJSON(t *testing.T) {
	src := hubSourceFixture(t, "")
	openTestWindow(t, src, time.Hour)
	require.NoError(t, src.state.UpdateReplyState(context.Background(), src.key(), func(st *oplocal.ReplyState) error {
		st.Holds = []oplocal.ReplyHold{{Sender: "far", EventID: "e1", Start: schedT0, Until: schedT0.Add(2 * time.Hour)}}
		st.Day, st.HeldSecs, st.AccountedTo = schedT0.Format("2006-01-02"), 120, schedT0
		return nil
	}))
	val := "900"
	require.NoError(t, src.state.UpdateWakeSetting(context.Background(),
		oplocal.WakeSettingUpdate{Key: service.RemoteAwakeCapKey, Value: &val}, func(string, string) error { return nil }))

	row := hubContactRow(src, true, schedT0)
	assert.Contains(t, row.Detail, "reply windows 1 open, holds 1 active, remote-held today 120s (cap 900s")
	require.NotNil(t, row.Reply)
	assert.Len(t, row.Reply.OpenWindows, 1)
	assert.Len(t, row.Reply.ActiveHolds, 1)
	assert.EqualValues(t, 900, row.Reply.DailyCapSecs)

	raw, err := json.Marshal(row)
	require.NoError(t, err)
	var back struct {
		Reply struct {
			Open []any `json:"open_windows"`
			Held []any `json:"active_holds"`
			Secs int64 `json:"remote_held_secs_today"`
			Cap  int64 `json:"daily_cap_secs"`
		} `json:"reply"`
	}
	require.NoError(t, json.Unmarshal(raw, &back))
	assert.Len(t, back.Reply.Open, 1)
	assert.Len(t, back.Reply.Held, 1)
	assert.EqualValues(t, 120, back.Reply.Secs)
	assert.EqualValues(t, 900, back.Reply.Cap)
}

func TestHubContactRow_ReplyStatus_IdleBoardShowsZeroes(t *testing.T) {
	src := hubSourceFixture(t, "")
	row := hubContactRow(src, true, schedT0)
	assert.Contains(t, row.Detail, "reply windows 0 open, holds 0 active, remote-held today 0s (uncapped; extensions unlimited)")
	assert.False(t, row.Warn)
}

func TestHubContactRow_UnreadableReplyState_WarnsWithFix(t *testing.T) {
	src := hubSourceFixture(t, "")
	require.NoError(t, src.state.WriteJSON("replywin-"+src.key(), "not an object"))
	row := hubContactRow(src, true, schedT0)
	assert.True(t, row.Warn)
	assert.Contains(t, row.Detail, "reply windows unreadable")
	assert.Contains(t, row.Fix, fixReplyPolicy)
	assert.Contains(t, row.Fix, "replywin-"+src.key()+".json")
	assert.Nil(t, row.Reply)
}

func TestHubContactRow_SkippedReplyWindows_WarnsWithFixAndJSON(t *testing.T) {
	src := hubSourceFixture(t, "")
	require.NoError(t, src.state.UpdateReplyState(context.Background(), src.key(), func(st *oplocal.ReplyState) error {
		st.Skipped = []oplocal.SkippedReplyWindow{{Window: oplocal.ReplyWindow{AwaitingFrom: "peer"}, Reason: "blank", At: schedT0}}
		return nil
	}))
	row := hubContactRow(src, true, schedT0)
	assert.True(t, row.Warn)
	assert.Contains(t, row.Detail, "1 malformed reply window(s) skipped")
	assert.Contains(t, row.Fix, fixSkippedReplyWindow)
	raw, err := json.Marshal(row)
	require.NoError(t, err)
	var back struct {
		Reply struct {
			Skipped int `json:"skipped_windows"`
		} `json:"reply"`
	}
	require.NoError(t, json.Unmarshal(raw, &back))
	assert.Equal(t, 1, back.Reply.Skipped)
}

func recordSkippedWindow(t *testing.T, src *hubContactSource, at time.Time) {
	t.Helper()
	require.NoError(t, src.state.UpdateReplyState(context.Background(), src.key(), func(st *oplocal.ReplyState) error {
		st.Skipped = []oplocal.SkippedReplyWindow{{Window: oplocal.ReplyWindow{AwaitingFrom: "peer"}, Reason: "blank", At: at}}
		return nil
	}))
}

func TestHubContactRow_SkippedReplyWindow_FixSurvivesLaterConditions(t *testing.T) {
	tests := []struct {
		name         string
		cfg          string
		breakInputs  bool
		wantPass     bool
		wantAlsoFix  []string
		wantAlsoText string
	}{
		{"skipped only", "", false, true, nil, ""},
		{"grandfathered", "prefix: \"X\"\n", false, true, []string{fixHubContact}, "grandfathered"},
		{"unreadable inputs", "", true, false, []string{fixUnreadableInputs}, "inputs unreadable"},
		{"grandfathered and unreadable", "prefix: \"X\"\n", true, false, []string{fixHubContact, fixUnreadableInputs}, "grandfathered"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := hubSourceFixture(t, tt.cfg)
			recordSkippedWindow(t, src, schedT0)
			if tt.breakInputs {
				require.NoError(t, src.state.WriteJSON("hubact-"+src.key(), map[string]any{"at": schedT0, "kind": "parked_wait"}))
			}
			row := hubContactRow(src, true, schedT0)
			assert.Equal(t, tt.wantPass, row.Pass)
			assert.Contains(t, row.Detail, "malformed reply window(s) skipped")
			assert.Contains(t, row.Detail, tt.wantAlsoText)
			assert.Contains(t, row.Fix, fixSkippedReplyWindow, "the skipped-window fix is kept")
			for _, f := range tt.wantAlsoFix {
				assert.Contains(t, row.Fix, f, "the later condition's fix is kept too")
			}
		})
	}
}

func TestHubContactRow_SkippedReplyWindow_FixAndDetailNameTheRealCauses(t *testing.T) {
	src := hubSourceFixture(t, "")
	recordSkippedWindow(t, src, schedT0)
	row := hubContactRow(src, true, schedT0)
	assert.NotContains(t, row.Fix, "MTIX_AUTHOR_ID", "the CLI author is never blank, so that setting cures nothing")
	assert.Contains(t, row.Fix, "non-blank author")
	assert.Contains(t, row.Fix, "mtix_annotate")
	assert.NotContains(t, row.Detail, "(no sender)", "the cause may be a blank awaited agent or none at all")
	assert.Contains(t, row.Detail, "blank sender or awaited agent")
}

func TestHubContactRow_SkippedReplyWindow_AgeBoundary(t *testing.T) {
	tests := []struct {
		name     string
		age      time.Duration
		wantWarn bool
		want     int
	}{
		{"fresh", 0, true, 1},
		{"23 hours", 23 * time.Hour, true, 1},
		{"just under 24 hours", 24*time.Hour - time.Second, true, 1},
		{"exactly 24 hours", 24 * time.Hour, false, 0},
		{"25 hours", 25 * time.Hour, false, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := hubSourceFixture(t, "")
			recordSkippedWindow(t, src, schedT0)
			row := hubContactRow(src, true, schedT0.Add(tt.age))
			assert.Equal(t, tt.wantWarn, row.Warn)
			require.NotNil(t, row.Reply)
			assert.Equal(t, tt.want, row.Reply.SkippedWindows)
			if !tt.wantWarn {
				assert.NotContains(t, row.Detail, "malformed")
				assert.Empty(t, row.Fix)
			}
		})
	}
}
