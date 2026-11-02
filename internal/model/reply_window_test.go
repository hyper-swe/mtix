// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestParseAwaitReply_Table_DefaultClampAndRejects(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"empty is the one hour default", "", time.Hour, false},
		{"explicit thirty minutes", "30m", 30 * time.Minute, false},
		{"exactly eight hours", "8h", 8 * time.Hour, false},
		{"nine hours clamps to eight", "9h", 8 * time.Hour, false},
		{"three days clamps to eight", "72h", 8 * time.Hour, false},
		{"zero rejected", "0s", 0, true},
		{"negative rejected", "-5m", 0, true},
		{"garbage rejected", "soon", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := model.ParseAwaitReply(tt.in)
			if tt.wantErr {
				require.ErrorIs(t, err, model.ErrInvalidInput)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestReplyBy_FormatAndParse_RoundTripUTC(t *testing.T) {
	at := time.Date(2026, 11, 2, 14, 30, 0, 0, time.FixedZone("x", 5*3600))
	text := model.FormatReplyBy(at)
	require.Equal(t, "2026-11-02T09:30:00Z", text)
	require.True(t, model.ParseReplyBy(text).Equal(at))
	require.True(t, model.ParseReplyBy("").IsZero())
	require.True(t, model.ParseReplyBy("tomorrow").IsZero(), "a bad value from another replica is no deadline")
}

func TestCommentPayload_ReplyBy_AdditiveAndOmittedWhenAbsent(t *testing.T) {
	plain, err := json.Marshal(model.CommentPayload{AuthorID: "a", Body: "b", To: "c"})
	require.NoError(t, err)
	require.NotContains(t, string(plain), "reply_by")

	raw, err := json.Marshal(model.CommentPayload{AuthorID: "a", Body: "b", To: "c", Hop: 2, ReplyBy: "2026-11-02T09:30:00Z"})
	require.NoError(t, err)
	require.Contains(t, string(raw), `"reply_by":"2026-11-02T09:30:00Z"`)
	var back model.CommentPayload
	require.NoError(t, json.Unmarshal(raw, &back))
	require.Equal(t, "2026-11-02T09:30:00Z", back.ReplyBy)
	require.EqualValues(t, 2, back.Hop)

	var old model.CommentPayload
	require.NoError(t, json.Unmarshal([]byte(`{"author_id":"a","body":"b"}`), &old))
	require.Empty(t, old.ReplyBy)
}
