// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model_test

import (
	"bytes"
	"encoding/json"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/require"
	"log/slog"
	"testing"
)

func TestCommentHop_InvalidMetadata_WarnsAndPreservesComment(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	for _, hop := range []string{`null`, `-1`, `1.5`, `"2"`, `9223372036854775808`, `{}`, `[]`, `true`} {
		t.Run(hop, func(t *testing.T) {
			logs.Reset()
			var payload model.CommentPayload
			raw := `{"body":"retained body","to":"worker","annotation_id":"remote","hop":` + hop + `}`
			require.NoError(t, json.Unmarshal([]byte(raw), &payload))
			require.Zero(t, payload.Hop)
			require.Equal(t, "retained body", payload.Body)
			require.Equal(t, "worker", payload.To)
			require.Contains(t, logs.String(), "invalid comment hop; using 0")
			require.NotContains(t, logs.String(), "retained body")
		})
	}
	for _, raw := range []string{`{}`, `{"hop":0}`, `{"hop":32}`} {
		logs.Reset()
		var payload model.CommentPayload
		require.NoError(t, json.Unmarshal([]byte(raw), &payload))
		require.Empty(t, logs.String())
	}
}

func TestCommentHop_Encoding_RefusesNegativeAndOmitsZero(t *testing.T) {
	_, err := json.Marshal(model.CommentPayload{To: "worker", Hop: -1})
	require.Error(t, err)
	raw, err := json.Marshal(model.CommentPayload{To: "worker"})
	require.NoError(t, err)
	require.NotContains(t, string(raw), "hop")
	raw, err = json.Marshal(model.CommentPayload{To: "worker", Hop: 32})
	require.NoError(t, err)
	require.Contains(t, string(raw), `"hop":32`)
}

func TestParseCommentHop_StrictGuardBoundaries(t *testing.T) {
	for _, tt := range []struct {
		raw  string
		want int64
		bad  bool
	}{
		{"", 0, false}, {"0", 0, false}, {"33", 33, false}, {"9223372036854775807", 9223372036854775807, false},
		{"null", 0, true}, {" null ", 0, true}, {"-1", 0, true}, {"1.5", 0, true}, {`"2"`, 0, true}, {"9223372036854775808", 0, true},
	} {
		got, err := model.ParseCommentHop(json.RawMessage(tt.raw))
		if tt.bad {
			require.Error(t, err)
			continue
		}
		require.NoError(t, err)
		require.Equal(t, tt.want, got)
	}
}

func TestCommentHop_InvalidCommentFields_StillRefused(t *testing.T) {
	for _, raw := range []string{`{"body":3,"hop":null}`, `{"to":false,"hop":-1}`, `[]`, `true`, `{"hop":`} {
		var payload model.CommentPayload
		require.Error(t, json.Unmarshal([]byte(raw), &payload))
	}
}
