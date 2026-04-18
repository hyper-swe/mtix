// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/hyper-swe/mtix/internal/model"
)

// splitCSV parses a comma-separated flag value into a slice of trimmed,
// non-empty strings per FR-17.1. Returns nil for empty input or input
// containing only commas/whitespace. Used for multi-value filters such as
// --under, --status, --type, --assignee.
func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// splitCSVInts parses a comma-separated integer flag value per FR-17.1.
// Used for the --priority multi-value filter. Returns an error if any
// non-empty element fails strconv.Atoi.
func splitCSVInts(s string) ([]int, error) {
	parts := splitCSV(s)
	if len(parts) == 0 {
		return nil, nil
	}
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		v, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("invalid integer %q: %w", p, err)
		}
		if v < 1 || v > 5 {
			return nil, fmt.Errorf("priority %d out of range (must be 1-5): %w", v, model.ErrInvalidInput)
		}
		out = append(out, v)
	}
	return out, nil
}
