// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/hyper-swe/mtix/internal/model"
)

// JSONLInput represents a single task line in a JSONL plan file.
// Each line is an independent JSON object with task fields.
// This format is preferred over JSON arrays for LLM plan output because:
// - Each line is independently valid (partial output is still usable)
// - No bracket matching or trailing comma issues
// - Streaming-friendly (process lines as they arrive)
// - Append-friendly (add more tasks by appending lines)
//
// It is the same type as DecomposeInput, so a line accepts exactly the keys
// title, prompt, acceptance, description, priority, labels, issue_type and its
// alias type, and refuses any other key (MTIX-279).
type JSONLInput = DecomposeInput

// ParseJSONL reads JSONL-formatted task definitions from the given reader.
// Each non-empty line must be a valid JSON object with at least a "title" field.
// Empty and whitespace-only lines are skipped.
//
// Returns ErrInvalidInput if no tasks are found, a line has invalid JSON,
// or a line is missing a title.
func ParseJSONL(r io.Reader) ([]JSONLInput, error) {
	scanner := bufio.NewScanner(r)
	var results []JSONLInput
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var entry JSONLInput
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNum, err)
		}

		if entry.Title == "" {
			return nil, fmt.Errorf(
				"line %d: title is required: %w", lineNum, model.ErrInvalidInput)
		}

		if len(entry.Title) > model.MaxTitleLength {
			return nil, fmt.Errorf(
				"line %d: title too long (max %d): %w",
				lineNum, model.MaxTitleLength, model.ErrInvalidInput)
		}

		if err := model.ValidateIssueType(entry.IssueType); err != nil {
			return nil, fmt.Errorf("line %d: issue_type: %w", lineNum, err)
		}

		results = append(results, entry)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read JSONL input: %w", err)
	}

	if len(results) == 0 {
		return nil, fmt.Errorf("no tasks found in input: %w", model.ErrInvalidInput)
	}

	return results, nil
}

// ToDecomposeInputs converts parsed JSONL entries to DecomposeInput slice
// for use with NodeService.Decompose.
func ToDecomposeInputs(entries []JSONLInput) []DecomposeInput {
	inputs := make([]DecomposeInput, len(entries))
	copy(inputs, entries)
	return inputs
}
