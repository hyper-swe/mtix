// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/require"
)

func TestPayload_RoundTrip(t *testing.T) {
	until := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		in   any
		out  any
	}{
		{"create_node",
			&model.CreateNodePayload{Title: "x", NodeType: model.NodeTypeIssue, Priority: model.Priority(2), Labels: []string{"a", "b"}},
			&model.CreateNodePayload{}},
		{"update_field",
			&model.UpdateFieldPayload{FieldName: "title", NewValue: json.RawMessage(`"new"`), OldValue: json.RawMessage(`"old"`)},
			&model.UpdateFieldPayload{}},
		{"transition_status",
			&model.TransitionStatusPayload{From: model.StatusOpen, To: model.StatusInProgress, Reason: "started"},
			&model.TransitionStatusPayload{}},
		{"claim",
			&model.ClaimPayload{AgentID: "agent-1", TTLSeconds: 600, Forced: true},
			&model.ClaimPayload{}},
		{"unclaim", &model.UnclaimPayload{}, &model.UnclaimPayload{}},
		{"defer",
			&model.DeferPayload{Reason: "wait", Until: &until},
			&model.DeferPayload{}},
		{"comment",
			&model.CommentPayload{AuthorID: "alice", Body: "looks good"},
			&model.CommentPayload{}},
		{"link_dep",
			&model.LinkDepPayload{DependsOnNodeID: "MTIX-2", DepType: "blocks"},
			&model.LinkDepPayload{}},
		{"unlink_dep",
			&model.UnlinkDepPayload{DependsOnNodeID: "MTIX-2", DepType: "blocks"},
			&model.UnlinkDepPayload{}},
		{"delete", &model.DeletePayload{}, &model.DeletePayload{}},
		{"set_acceptance",
			&model.SetAcceptancePayload{AcceptanceText: "must compile"},
			&model.SetAcceptancePayload{}},
		{"set_prompt",
			&model.SetPromptPayload{PromptText: "do the thing"},
			&model.SetPromptPayload{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := model.EncodePayload(tc.in)
			require.NoError(t, err)
			require.NotEmpty(t, raw)
			require.NoError(t, model.DecodePayload(raw, tc.out))
			require.Equal(t, tc.in, tc.out)
		})
	}
}

func TestEncodePayload_NilProducesJSONNull(t *testing.T) {
	raw, err := model.EncodePayload(nil)
	require.NoError(t, err)
	require.Equal(t, "null", string(raw),
		"nil input MUST become 'null' so SyncEvent.Payload is never zero-length")
}

func TestEncodePayload_EmptyStructProducesEmptyObject(t *testing.T) {
	raw, err := model.EncodePayload(&model.UnclaimPayload{})
	require.NoError(t, err)
	require.Equal(t, "{}", string(raw),
		"empty struct payloads serialize to {} so the validator's non-empty rule passes")
}

func TestDecodePayload_RejectsEmpty(t *testing.T) {
	var p model.UnclaimPayload
	err := model.DecodePayload(nil, &p)
	require.Error(t, err)
	require.True(t, errors.Is(err, model.ErrInvalidInput))
}

func TestDecodePayload_RejectsMalformedJSON(t *testing.T) {
	var p model.CreateNodePayload
	err := model.DecodePayload(json.RawMessage(`{"title":`), &p)
	require.Error(t, err)
	require.True(t, errors.Is(err, model.ErrInvalidInput))
}
