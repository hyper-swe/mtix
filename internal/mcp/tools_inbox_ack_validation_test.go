// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
)

// FR-21 §12.4 prerequisite regression: mtix_inbox_ack with missing or invalid
// required args used to report SUCCESS ("Acked inbox event 0") while acking
// nothing — the LLM caller believed the event handled, the inbox kept
// resurfacing it, and under a wake-hook setup that meant a wake loop. Every
// malformed call must now return a tool error, and a rejected ack must leave
// the inbox untouched.
func TestInboxAckTool_RejectsMissingOrInvalidArgs(t *testing.T) {
	s := newInboxTestStore(t)
	inboxSeedNode(t, s, "PROJ-1")

	reg := NewToolRegistry()
	promptSvc := service.NewPromptService(s, nil, slog.Default(), fixedClock)
	RegisterContextTools(reg, newTestContextService(), promptSvc)
	RegisterInboxTools(reg, s)

	ctx := context.Background()
	_, err := reg.Call(ctx, "mtix_annotate", json.RawMessage(
		`{"id":"PROJ-1","text":"ruling: proceed","author":"worker","to":"opus"}`))
	require.NoError(t, err)
	direct, err := s.InboxList(ctx, "opus")
	require.NoError(t, err)
	require.Len(t, direct, 1)
	seq := direct[0].Seq

	cases := []struct {
		name string
		args string
		want string
	}{
		{"missing seq (the field case: wrong key 'seqs')", `{"agent":"opus","seqs":[1,2]}`, "'seq' is required"},
		{"missing seq entirely", `{"agent":"opus"}`, "'seq' is required"},
		{"seq zero", `{"agent":"opus","seq":0}`, "not a valid inbox seq"},
		{"seq negative", `{"agent":"opus","seq":-3}`, "not a valid inbox seq"},
		{"missing agent", `{"seq":1}`, "'agent' is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, callErr := reg.Call(ctx, "mtix_inbox_ack", json.RawMessage(tc.args))
			require.NoError(t, callErr, "validation failures are tool errors, not transport errors")
			require.NotNil(t, res)
			assert.True(t, res.IsError, "malformed ack must NOT report success")
			require.Len(t, res.Content, 1)
			assert.Contains(t, res.Content[0].Text, tc.want)
		})
	}

	// No rejected call consumed the event.
	direct, err = s.InboxList(ctx, "opus")
	require.NoError(t, err)
	require.Len(t, direct, 1, "rejected acks must leave the inbox untouched")

	// A beyond-tail seq is refused by the store bound and surfaces as an error.
	_, err = reg.Call(ctx, "mtix_inbox_ack", json.RawMessage(`{"agent":"opus","seq":999999}`))
	require.Error(t, err, "beyond-tail ack is rejected (store tail clamp)")

	// The well-formed call still works.
	res, err := reg.Call(ctx, "mtix_inbox_ack", json.RawMessage(
		[]byte(jsonAck("opus", seq))))
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.False(t, res.IsError)
	direct, err = s.InboxList(ctx, "opus")
	require.NoError(t, err)
	assert.Empty(t, direct, "valid ack consumes the event")
}

func jsonAck(agent string, seq int64) string {
	b, _ := json.Marshal(map[string]any{"agent": agent, "seq": seq})
	return string(b)
}
