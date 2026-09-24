// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

// Workflow payload rule (MTIX-95.27, a follow-up to MTIX-95.10; ADR-006
// §4.4; FR-18.9, FR-18.11).
//
// decodeWorkflowPayload is the one definition of which workflow events this
// build can use. A workflow event is malformed when the rule rejects its
// payload:
//
//	op                 malformed when
//	transition_status  the payload cannot be decoded, or its to-status is missing, null or empty
//	claim              the payload cannot be decoded
//	defer              the payload cannot be decoded, including an until that is not an RFC 3339 timestamp
//	unclaim            never: it carries no parameters, and its payload is not read
//
// A to-status this build does not know is not malformed: it is the unknown
// to-status residual in sync_workflow_winner.go.
//
// Both halves of the workflow winner rule use this rule, so they cannot
// disagree about an event: the apply path (workflowInputForApply, called by
// applyTransitionStatus, applyClaim, applyUnclaim and applyDefer) and the held
// winner lookup (latestHeldWorkflowKey). A malformed event therefore changes
// no node column, is recorded as applied, never counts as its node's held
// winner (so it neither wins over nor blocks an older event, and status
// converges whatever order it arrived in), and never fails its pull batch: a
// failed event fails the whole batch, and the pull cursor never moves past it.
//
// Widening this rule changes which stored events count as held: ship any
// widening with a re-apply or quarantine step (see the malformed events
// residual in sync_workflow_winner.go).

// workflowPayload is what the workflow payload rule reads from one event's
// payload. Only the fields of the event's op are set.
type workflowPayload struct {
	from, to   model.Status // transition_status
	agentID    string       // claim
	deferUntil *time.Time   // defer; nil when the defer has no until
}

// decodeWorkflowPayload applies the workflow payload rule to one workflow
// event's op_type and payload (MTIX-95.27). It returns an error wrapping
// model.ErrInvalidInput when the payload is malformed for op, and when op is
// not a workflow op.
func decodeWorkflowPayload(op model.OpType, payload []byte) (workflowPayload, error) {
	switch op {
	case model.OpTransitionStatus:
		var p model.TransitionStatusPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return workflowPayload{}, malformedPayload(op, err)
		}
		if p.To == "" {
			return workflowPayload{}, fmt.Errorf("%s payload has no to-status: %w", op, model.ErrInvalidInput)
		}
		return workflowPayload{from: p.From, to: p.To}, nil
	case model.OpClaim:
		var p model.ClaimPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return workflowPayload{}, malformedPayload(op, err)
		}
		return workflowPayload{agentID: p.AgentID}, nil
	case model.OpDefer:
		var p model.DeferPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return workflowPayload{}, malformedPayload(op, err)
		}
		return workflowPayload{deferUntil: p.Until}, nil
	case model.OpUnclaim:
		// unclaim carries no parameters; its payload is not read.
		return workflowPayload{}, nil
	default:
		return workflowPayload{}, fmt.Errorf("op_type %q is not a workflow op: %w", op, model.ErrInvalidInput)
	}
}

// malformedPayload wraps the decode error of an op's payload as
// model.ErrInvalidInput (MTIX-95.27).
func malformedPayload(op model.OpType, cause error) error {
	return fmt.Errorf("decode %s payload: %w: %w", op, cause, model.ErrInvalidInput)
}

// workflowInputForApply reads a winning workflow event with the workflow
// payload rule and returns its input for applyWorkflowWinner, with updatedAt
// (the caller's apply time) as updated_at (MTIX-95.27).
//
// ok is false when the payload is malformed. A warning then names the event
// and its op, and the caller changes no node column and returns nil, so the
// event is still recorded as applied and the rest of the pull batch applies.
func workflowInputForApply(e *model.SyncEvent, updatedAt string) (workflowInput, bool) {
	p, err := decodeWorkflowPayload(e.OpType, e.Payload)
	if err != nil {
		slog.Default().Warn("sync apply: malformed workflow event; node left unchanged",
			"event_id", e.EventID, "op_type", string(e.OpType), "error", err)
		return workflowInput{}, false
	}
	return workflowInput{
		op: e.OpType, from: p.from, to: p.to, agentID: p.agentID, deferUntil: p.deferUntil,
		wallClockTS: e.WallClockTS, updatedAt: updatedAt,
	}, true
}
