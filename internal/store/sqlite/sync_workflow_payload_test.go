// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/require"
)

// TestDecodeWorkflowPayload_PerOp_AcceptsUsableAndRejectsMalformed pins the
// one workflow payload rule (MTIX-95.27) that both the apply path and the held
// winner lookup use.
func TestDecodeWorkflowPayload_PerOp_AcceptsUsableAndRejectsMalformed(t *testing.T) {
	until := time.Date(2027, 1, 1, 4, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		op      model.OpType
		payload string
		want    workflowPayload
		wantErr bool
	}{
		{"transition", model.OpTransitionStatus, `{"from":"open","to":"done","reason":"r"}`,
			workflowPayload{from: model.StatusOpen, to: model.StatusDone}, false},
		{"transition to an unknown status", model.OpTransitionStatus, `{"to":"archived"}`,
			workflowPayload{to: "archived"}, false},
		{"transition without to", model.OpTransitionStatus, `{"from":"open"}`, workflowPayload{}, true},
		{"transition with null to", model.OpTransitionStatus, `{"to":null}`, workflowPayload{}, true},
		{"transition with empty to", model.OpTransitionStatus, `{"to":""}`, workflowPayload{}, true},
		{"transition with non-string to", model.OpTransitionStatus, `{"to":5}`, workflowPayload{}, true},
		{"transition with null payload", model.OpTransitionStatus, `null`, workflowPayload{}, true},
		{"transition not JSON", model.OpTransitionStatus, `<<<`, workflowPayload{}, true},
		// A usable to-status does not rescue a payload that fails to decode.
		{"transition with non-string from", model.OpTransitionStatus, `{"from":5,"to":"done"}`, workflowPayload{}, true},
		{"transition with non-string reason", model.OpTransitionStatus, `{"to":"done","reason":5}`, workflowPayload{}, true},
		{"claim", model.OpClaim, `{"agent_id":"agent-a","ttl_seconds":60}`,
			workflowPayload{agentID: "agent-a"}, false},
		{"claim not JSON", model.OpClaim, `<<<`, workflowPayload{}, true},
		{"claim with non-string agent_id", model.OpClaim, `{"agent_id":5}`, workflowPayload{}, true},
		{"defer with until", model.OpDefer, `{"until":"2027-01-01T09:30:00+05:30"}`,
			workflowPayload{deferUntil: &until}, false},
		{"defer without until", model.OpDefer, `{"reason":"later"}`, workflowPayload{}, false},
		{"defer with null until", model.OpDefer, `{"until":null}`, workflowPayload{}, false},
		{"defer not JSON", model.OpDefer, `<<<`, workflowPayload{}, true},
		{"defer with unparseable until", model.OpDefer, `{"until":"next tuesday"}`, workflowPayload{}, true},
		{"unclaim", model.OpUnclaim, `{}`, workflowPayload{}, false},
		{"unclaim with any payload", model.OpUnclaim, `<<<`, workflowPayload{}, false},
		{"not a workflow op", model.OpComment, `{"body":"x"}`, workflowPayload{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decodeWorkflowPayload(tt.op, []byte(tt.payload))

			if tt.wantErr {
				require.Error(t, err)
				require.True(t, errors.Is(err, model.ErrInvalidInput), "malformed wraps ErrInvalidInput: %v", err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want.from, got.from)
			require.Equal(t, tt.want.to, got.to)
			require.Equal(t, tt.want.agentID, got.agentID)
			if tt.want.deferUntil == nil {
				require.Nil(t, got.deferUntil)
				return
			}
			require.NotNil(t, got.deferUntil)
			require.True(t, tt.want.deferUntil.Equal(*got.deferUntil), "until %v", got.deferUntil)
		})
	}
}

// TestWorkflowInputForApply_WellFormedEvent_CarriesPayloadAndEnvelope checks
// that a usable event's input carries the decoded payload, the event's
// wall_clock_ts and the caller's apply time.
func TestWorkflowInputForApply_WellFormedEvent_CarriesPayloadAndEnvelope(t *testing.T) {
	e := winnerTestEvent(t, "t-1", model.OpTransitionStatus,
		&model.TransitionStatusPayload{From: model.StatusBlocked, To: model.StatusOpen}, 4)
	e.WallClockTS = 1_234

	in, ok := workflowInputForApply(e, "2026-09-24T00:00:00Z")

	require.True(t, ok)
	require.Equal(t, workflowInput{
		op: model.OpTransitionStatus, from: model.StatusBlocked, to: model.StatusOpen,
		wallClockTS: 1_234, updatedAt: "2026-09-24T00:00:00Z",
	}, in)
}

// TestWorkflowInputForApply_MalformedEvent_WarnsNamingEventAndOp checks the
// warning a malformed winner logs before the apply path leaves the node alone.
func TestWorkflowInputForApply_MalformedEvent_WarnsNamingEventAndOp(t *testing.T) {
	logs := captureDefaultLog(t)
	e := winnerTestEvent(t, "claim-bad", model.OpClaim, nil, 4)
	e.Payload = []byte(`<<<not-json`)

	_, ok := workflowInputForApply(e, "2026-09-24T00:00:00Z")

	require.False(t, ok)
	var warned bool
	for _, line := range strings.Split(logs.String(), "\n") {
		if strings.Contains(line, "level=WARN") && strings.Contains(line, "event_id=claim-bad") &&
			strings.Contains(line, "op_type=claim") {
			warned = true
		}
	}
	require.True(t, warned, "logs:\n%s", logs.String())
}
