// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"encoding/json"
	"fmt"
)

// UnmarshalJSON accepts the additive hop field and refuses invalid chain depth.
func (p *CommentPayload) UnmarshalJSON(raw []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return fmt.Errorf("decode comment payload fields: %w", err)
	}
	if hop, present := fields["hop"]; present && string(hop) == "null" {
		return fmt.Errorf("comment hop must be an integer: %w", ErrInvalidInput)
	}
	type wire CommentPayload
	var value wire
	if err := json.Unmarshal(raw, &value); err != nil {
		return fmt.Errorf("decode comment payload: %w", err)
	}
	if value.Hop < 0 {
		return fmt.Errorf("comment hop must be nonnegative: %w", ErrInvalidInput)
	}
	*p = CommentPayload(value)
	return nil
}

// MarshalJSON keeps every locally encoded hop nonnegative and omits zero.
func (p CommentPayload) MarshalJSON() ([]byte, error) {
	if p.Hop < 0 {
		return nil, fmt.Errorf("comment hop must be nonnegative: %w", ErrInvalidInput)
	}
	type wire CommentPayload
	return json.Marshal(wire(p))
}
