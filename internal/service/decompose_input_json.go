// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/hyper-swe/mtix/internal/model"
)

// decomposeInputWire is DecomposeInput as it appears on the wire: the same
// keys plus the "type" alias for issue_type.
type decomposeInputWire struct {
	Title       string          `json:"title"`
	Prompt      string          `json:"prompt,omitempty"`
	Acceptance  string          `json:"acceptance,omitempty"`
	Description string          `json:"description,omitempty"`
	Priority    model.Priority  `json:"priority,omitempty"`
	Labels      []string        `json:"labels,omitempty"`
	IssueType   model.IssueType `json:"issue_type,omitempty"`
	Type        model.IssueType `json:"type,omitempty"`
}

// UnmarshalJSON decodes one decompose child strictly (MTIX-279): an unknown
// key is refused with ErrInvalidInput naming the key instead of being dropped,
// "type" is accepted as an alias of "issue_type", and giving both with
// different values is ErrInvalidInput. The issue type value itself is checked
// by the callers with model.ValidateIssueType, as create --type does.
func (in *DecomposeInput) UnmarshalJSON(data []byte) error {
	var w decomposeInputWire
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&w); err != nil {
		if key, ok := strings.CutPrefix(err.Error(), "json: unknown field "); ok {
			return fmt.Errorf("unknown key %s (allowed: title, prompt, acceptance, description, priority, labels, issue_type, type): %w",
				key, model.ErrInvalidInput)
		}
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) {
			return fmt.Errorf("key %q has the wrong value type (got a JSON %s): %w",
				typeErr.Field, typeErr.Value, model.ErrInvalidInput)
		}
		return fmt.Errorf("invalid JSON: %s: %w", err.Error(), model.ErrInvalidInput)
	}
	if w.IssueType != "" && w.Type != "" && w.IssueType != w.Type {
		return fmt.Errorf("type %q conflicts with issue_type %q; give one: %w",
			w.Type, w.IssueType, model.ErrInvalidInput)
	}
	if w.IssueType == "" {
		w.IssueType = w.Type
	}
	*in = DecomposeInput{
		Title: w.Title, Prompt: w.Prompt, Acceptance: w.Acceptance, Description: w.Description,
		Priority: w.Priority, Labels: w.Labels, IssueType: w.IssueType,
	}
	return nil
}
