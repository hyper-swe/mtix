// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/require"
)

// Malformed workflow events must neither win nor wedge (MTIX-95.27, a
// follow-up to MTIX-95.10).
//
// A workflow event is malformed when this build cannot use its payload: a
// transition_status without a usable to-status, or a claim or defer whose
// payload cannot be decoded (including an until that is not a timestamp).
// Before MTIX-95.27 a malformed transition still counted as its node's held
// winner, so an older valid event applied on one replica and was rejected on
// another, depending on arrival order. A winning malformed claim or defer
// failed its pull batch, and so every later pull, because the cursor moves
// only when a batch commits. A malformed event now changes no node column,
// is recorded as applied, never counts as the held winner and never fails
// the batch.

// malformedCase is one malformed workflow event: its op and its raw payload.
type malformedCase struct {
	name    string
	op      model.OpType
	payload string
}

// malformedTransitions are transition_status payloads without a usable
// to-status.
func malformedTransitions() []malformedCase {
	return []malformedCase{
		{"transition without to", model.OpTransitionStatus, `{"from":"open"}`},
		{"transition with null to", model.OpTransitionStatus, `{"from":"open","to":null}`},
		{"transition with empty to", model.OpTransitionStatus, `{"from":"open","to":""}`},
		{"transition with non-string to", model.OpTransitionStatus, `{"from":"open","to":5}`},
		{"transition with null payload", model.OpTransitionStatus, `null`},
		{"transition not JSON", model.OpTransitionStatus, `<<<not-json`},
	}
}

// malformedClaims are claim payloads that cannot be decoded.
func malformedClaims() []malformedCase {
	return []malformedCase{
		{"claim not JSON", model.OpClaim, `<<<not-json`},
		{"claim with non-string agent_id", model.OpClaim, `{"agent_id":5}`},
		{"claim payload a JSON string", model.OpClaim, `"agent-a"`},
		{"claim payload a JSON array", model.OpClaim, `[]`},
	}
}

// malformedDefers are defer payloads that cannot be decoded, including an
// until that is not an RFC 3339 timestamp.
func malformedDefers() []malformedCase {
	return []malformedCase{
		{"defer not JSON", model.OpDefer, `<<<not-json`},
		{"defer with unparseable until", model.OpDefer, `{"reason":"later","until":"next tuesday"}`},
		{"defer with date-only until", model.OpDefer, `{"until":"2027-01-01"}`},
		{"defer with numeric until", model.OpDefer, `{"until":1798761600}`},
		{"defer with non-string reason", model.OpDefer, `{"reason":5}`},
	}
}

// malformedEvent builds a foreign workflow event for nodeID that carries the
// case's payload verbatim.
func malformedEvent(t *testing.T, nodeID string, c malformedCase, lamport int64) *model.SyncEvent {
	t.Helper()
	e := foreignWorkflowEvent(t, nodeID, c.op, nil, lamport, "")
	e.Payload = json.RawMessage(c.payload)
	return e
}

// TestApply_ValidEventAndHigherMalformedWorkflowEvent_BothOrdersConverge is
// the MTIX-95.27 regression. Two replicas receive a valid workflow event and
// a malformed one with a higher key, in opposite orders. Before the fix, a
// claim and a transition without a to-status ended in_progress on the replica
// that received the claim first and open on the other, because the malformed
// transition counted as the held winner; a malformed claim or defer failed
// the pull.
func TestApply_ValidEventAndHigherMalformedWorkflowEvent_BothOrdersConverge(t *testing.T) {
	valid := []struct {
		name    string
		op      model.OpType
		payload any
		want    map[string]string // expected columns on every replica
	}{
		{"claim", model.OpClaim, &model.ClaimPayload{AgentID: "agent-a"},
			map[string]string{"status": "in_progress", "assignee": "agent-a", "closed_at": nullColumn}},
		{"done", model.OpTransitionStatus, transition(model.StatusOpen, model.StatusDone),
			map[string]string{"status": "done", "closed_at": foreignClosedAt}},
	}
	var cases []malformedCase
	cases = append(cases, malformedTransitions()...)
	cases = append(cases, malformedClaims()...)
	cases = append(cases, malformedDefers()...)
	for _, v := range valid {
		for _, c := range cases {
			t.Run(v.name+" and "+c.name, func(t *testing.T) {
				good := foreignWorkflowEvent(t, "MTIX-1", v.op, v.payload, 2, "")
				bad := malformedEvent(t, "MTIX-1", c, 3)
				storeA, rawA := replicaWithNode(t)
				storeB, rawB := replicaWithNode(t)

				pullEvents(t, storeA, []*model.SyncEvent{good, bad})
				pullEvents(t, storeB, []*model.SyncEvent{bad, good})

				rowA, rowB := nodeRow(t, rawA, "MTIX-1"), nodeRow(t, rawB, "MTIX-1")
				for _, col := range []string{"status", "assignee", "closed_at"} {
					require.Equal(t, rowA[col], rowB[col], "column %s converges in both orders", col)
				}
				for col, want := range v.want {
					require.Equal(t, want, rowA[col], "valid first: column %s", col)
					require.Equal(t, want, rowB[col], "malformed first: column %s", col)
				}
			})
		}
	}
}

// captureWarnings routes the default slog logger, which the apply path logs
// through, to a buffer for one test.
func captureWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// hasWarningFor reports whether logs hold a WARN line naming eventID.
func hasWarningFor(logs, eventID string) bool {
	for _, line := range strings.Split(logs, "\n") {
		if strings.Contains(line, "level=WARN") && strings.Contains(line, "event_id="+eventID) {
			return true
		}
	}
	return false
}

// pullMalformedThenNormal pulls, in one batch, the malformed event of c for
// MTIX-1 (the winner: no workflow event is held for it) followed by a normal
// transition for MTIX-2. The batch must commit, MTIX-1 must keep every
// column, the malformed event must be recorded as applied with a warning
// naming it, and the transition must apply.
func pullMalformedThenNormal(t *testing.T, c malformedCase) {
	t.Helper()
	s, raw := replicaWithNode(t)
	mustCreateNode(t, s, "MTIX-2", "")
	seedWorkflowColumns(t, raw, model.StatusOpen)
	before := nodeRow(t, raw, "MTIX-1")
	logs := captureWarnings(t)
	bad := malformedEvent(t, "MTIX-1", c, 2)
	normal := foreignWorkflowEvent(t, "MTIX-2", model.OpTransitionStatus,
		transition(model.StatusOpen, model.StatusInProgress), 3, "")

	require.NoError(t, applyEventsInTx(s, []*model.SyncEvent{bad, normal}), "the batch must not fail")

	require.Equal(t, before, nodeRow(t, raw, "MTIX-1"), "no node column changes")
	require.Equal(t, string(model.StatusInProgress), nodeRow(t, raw, "MTIX-2")["status"],
		"the event after it still applies")
	require.Equal(t, 1, countRows(t, raw,
		`SELECT COUNT(*) FROM applied_events WHERE event_id = ?`, bad.EventID),
		"the malformed event is recorded as applied")
	require.True(t, hasWarningFor(logs.String(), bad.EventID),
		"a warning names the event; logs:\n%s", logs.String())
}

// TestApplyClaim_UndecodablePayload_InBatch_ChangesNothingAndPullContinues:
// before MTIX-95.27 a winning claim whose payload could not be decoded failed
// its batch, so every later pull failed the same way.
func TestApplyClaim_UndecodablePayload_InBatch_ChangesNothingAndPullContinues(t *testing.T) {
	for _, c := range malformedClaims() {
		t.Run(c.name, func(t *testing.T) {
			pullMalformedThenNormal(t, c)
		})
	}
}

// TestApplyDefer_UndecodablePayload_InBatch_ChangesNothingAndPullContinues:
// before MTIX-95.27 a winning defer whose payload could not be decoded
// (including an until that is not an RFC 3339 timestamp) failed its batch, so
// every later pull failed the same way.
func TestApplyDefer_UndecodablePayload_InBatch_ChangesNothingAndPullContinues(t *testing.T) {
	for _, c := range malformedDefers() {
		t.Run(c.name, func(t *testing.T) {
			pullMalformedThenNormal(t, c)
		})
	}
}
