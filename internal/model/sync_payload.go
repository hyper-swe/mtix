// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"encoding/json"
	"fmt"
	"time"
)

// Payload structs for the 12 op_types per SYNC-DESIGN.md §3.3.
//
// Each payload type carries the minimum fields needed to (a) replay the
// mutation against a fresh node tree and (b) detect conflicts at the
// field level (OldValue captured where applicable for §8 LWW).
//
// Marshaled into json.RawMessage in SyncEvent.Payload. The hub validator
// in MTIX-15.3 enforces the 64KB size cap per FR-18.7.

// CreateNodePayload carries the full node fields needed to recreate.
// Fields mirror model.Node but use json tags only — no DB-coupled types.
type CreateNodePayload struct {
	Title       string   `json:"title"`
	ParentID    string   `json:"parent_id,omitempty"`
	NodeType    NodeType `json:"node_type"`
	Description string   `json:"description,omitempty"`
	Prompt      string   `json:"prompt,omitempty"`
	Acceptance  string   `json:"acceptance,omitempty"`
	Priority    int      `json:"priority,omitempty"`
	Labels      []string `json:"labels,omitempty"`
	Assignee    string   `json:"assignee,omitempty"`
	Creator     string   `json:"creator,omitempty"`
}

// UpdateFieldPayload captures a single-field update.
//
// OldValue is captured to enable conflict detection in MTIX-15.5 — the LWW
// resolver uses the (field_name, old_value) pair to detect concurrent
// updates to the same field.
//
// Both NewValue and OldValue are json.RawMessage so any JSON-serializable
// value passes through unchanged (string, number, bool, array, object).
type UpdateFieldPayload struct {
	FieldName string          `json:"field_name"`
	NewValue  json.RawMessage `json:"new_value"`
	OldValue  json.RawMessage `json:"old_value,omitempty"`
}

// TransitionStatusPayload captures a state-machine transition.
type TransitionStatusPayload struct {
	From   Status `json:"from"`
	To     Status `json:"to"`
	Reason string `json:"reason,omitempty"`
}

// ClaimPayload captures an agent claim.
//
// Forced indicates a force-reclaim (ForceReclaimNode); the implicit unclaim
// of the previous holder is encoded in the same event so replay produces a
// single claim transition.
type ClaimPayload struct {
	AgentID    string `json:"agent_id"`
	TTLSeconds int64  `json:"ttl_seconds,omitempty"`
	Forced     bool   `json:"forced,omitempty"`
}

// UnclaimPayload is empty — the act of unclaiming carries no parameters
// beyond the node_id and author_id already on the SyncEvent envelope.
type UnclaimPayload struct{}

// DeferPayload captures a deferral. Until is nil for indefinite defers.
type DeferPayload struct {
	Reason string     `json:"reason,omitempty"`
	Until  *time.Time `json:"until,omitempty"`
}

// CommentPayload carries a comment annotation. AuthorID is duplicated from
// the envelope because annotations historically allow a different author
// from the CLI agent (e.g. attributed comments).
type CommentPayload struct {
	AuthorID string `json:"author_id"`
	Body     string `json:"body"`
}

// LinkDepPayload captures a dependency creation.
type LinkDepPayload struct {
	DependsOnNodeID string `json:"depends_on_node_id"`
	DepType         string `json:"dep_type"`
}

// UnlinkDepPayload captures a dependency removal.
type UnlinkDepPayload struct {
	DependsOnNodeID string `json:"depends_on_node_id"`
	DepType         string `json:"dep_type,omitempty"`
}

// DeletePayload is empty — the tombstone op carries no parameters beyond
// the node_id on the SyncEvent envelope. Tombstones are monotonic per
// SYNC-DESIGN §8.3.
type DeletePayload struct{}

// SetAcceptancePayload carries the new acceptance text. OldValue elided —
// the LWW resolver compares acceptance via content_hash on apply.
type SetAcceptancePayload struct {
	AcceptanceText string `json:"acceptance_text"`
}

// SetPromptPayload carries the new prompt text. Same elision rationale as
// SetAcceptancePayload.
type SetPromptPayload struct {
	PromptText string `json:"prompt_text"`
}

// EncodePayload marshals a typed payload into a json.RawMessage. Always
// produces a non-nil result (empty struct → "{}") so SyncEvent.Validate's
// non-empty-payload check is satisfied.
func EncodePayload(v any) (json.RawMessage, error) {
	if v == nil {
		return json.RawMessage("null"), nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encode payload: %w", err)
	}
	return b, nil
}

// DecodePayload is a typed decoder used by tests and the apply engine
// (MTIX-15.4). Returns ErrInvalidInput if the JSON does not match the
// target type.
func DecodePayload(raw json.RawMessage, target any) error {
	if len(raw) == 0 {
		return fmt.Errorf("empty payload: %w", ErrInvalidInput)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("decode payload: %w: %w", ErrInvalidInput, err)
	}
	return nil
}
