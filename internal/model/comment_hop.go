// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
)

// UnmarshalJSON tolerates malformed additive metadata on ingest so a comment
// accepted by older replicas remains visible. The raw sync event is not changed.
func (p *CommentPayload) UnmarshalJSON(raw []byte) error {
	type wire CommentPayload
	var value struct {
		wire
		Hop json.RawMessage `json:"hop"`
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return fmt.Errorf("decode comment payload: %w", err)
	}
	hop, err := ParseCommentHop(value.Hop)
	if err != nil {
		slog.Warn("invalid comment hop; using 0")
		hop = 0
	}
	*p = CommentPayload(value.wire)
	p.Hop = hop
	return nil
}

// ParseCommentHop validates a stored or locally supplied hop for guard decisions.
// Absent metadata is zero; null, negative and noninteger values are invalid.
func ParseCommentHop(raw json.RawMessage) (int64, error) {
	if len(raw) == 0 {
		return 0, nil
	}
	var hop int64
	if string(bytes.TrimSpace(raw)) == "null" {
		return 0, fmt.Errorf("comment hop must be an integer: %w", ErrInvalidInput)
	}
	if err := json.Unmarshal(raw, &hop); err != nil {
		return 0, fmt.Errorf("decode comment hop: %w", err)
	}
	if hop < 0 {
		return 0, fmt.Errorf("comment hop must be nonnegative: %w", ErrInvalidInput)
	}
	return hop, nil
}

// MarshalJSON keeps every locally encoded hop nonnegative and omits zero.
func (p CommentPayload) MarshalJSON() ([]byte, error) {
	if p.Hop < 0 {
		return nil, fmt.Errorf("comment hop must be nonnegative: %w", ErrInvalidInput)
	}
	type wire CommentPayload
	return json.Marshal(wire(p))
}
