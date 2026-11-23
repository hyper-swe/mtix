// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"encoding/json"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// ValidateDecomposeChildren checks every child of a decompose batch before
// anything is written (FR-3.1, FR-6.3, MTIX-279). It is the one rule shared by
// Decompose, DecomposeDryRun and the REST handler, so the CLI, MCP, REST and
// gRPC surfaces refuse the same batches and a refused batch creates nothing.
//
// Returns ErrInvalidInput naming the child index for an empty batch, a missing
// or over-long title, an invalid issue type, a priority outside 1..5, or a
// description or prompt over its size limit.
func ValidateDecomposeChildren(children []DecomposeInput) error {
	if len(children) == 0 {
		return fmt.Errorf("at least one child is required: %w", model.ErrInvalidInput)
	}
	for i := range children {
		if err := validateDecomposeChild(&children[i]); err != nil {
			return fmt.Errorf("child %d: %w", i, err)
		}
	}
	return nil
}

// validateDecomposeChild checks one child's fields against the node limits.
func validateDecomposeChild(child *DecomposeInput) error {
	if child.Title == "" {
		return fmt.Errorf("title is required: %w", model.ErrInvalidInput)
	}
	if len(child.Title) > model.MaxTitleLength {
		return fmt.Errorf("title too long (max %d): %w", model.MaxTitleLength, model.ErrInvalidInput)
	}
	if err := model.ValidateIssueType(child.IssueType); err != nil {
		return fmt.Errorf("issue_type: %w", err)
	}
	if child.Priority != 0 && !child.Priority.IsValid() {
		return fmt.Errorf("priority must be between %d and %d, got %d: %w",
			model.PriorityCritical, model.PriorityBacklog, child.Priority, model.ErrInvalidInput)
	}
	if len(child.Description) > model.MaxDescriptionSize {
		return fmt.Errorf("description exceeds maximum size of %d bytes: %w",
			model.MaxDescriptionSize, model.ErrInvalidInput)
	}
	if len(child.Prompt) > model.MaxPromptSize {
		return fmt.Errorf("prompt exceeds maximum size of %d bytes: %w",
			model.MaxPromptSize, model.ErrInvalidInput)
	}
	return nil
}

// DecodeDecomposeChildren decodes a JSON array of decompose children strictly
// (MTIX-279), naming the child index in any decode error: an unknown key, a
// wrong value type or a type/issue_type conflict is ErrInvalidInput. The MCP
// and REST surfaces use it so an agent can tell which child to fix.
func DecodeDecomposeChildren(raws []json.RawMessage) ([]DecomposeInput, error) {
	children := make([]DecomposeInput, len(raws))
	for i, raw := range raws {
		if err := json.Unmarshal(raw, &children[i]); err != nil {
			return nil, fmt.Errorf("child %d: %w", i, err)
		}
	}
	return children, nil
}
