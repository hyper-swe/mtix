// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/hyper-swe/mtix/internal/model"
)

// TestStatus_IsValid_AllValidStatuses verifies all 7 statuses are recognized.
func TestStatus_IsValid_AllValidStatuses(t *testing.T) {
	for _, s := range model.AllStatuses() {
		t.Run(string(s), func(t *testing.T) {
			assert.True(t, s.IsValid(), "status %q should be valid", s)
		})
	}
}

// TestStatus_IsValid_InvalidStatus verifies unrecognized statuses are rejected.
func TestStatus_IsValid_InvalidStatus(t *testing.T) {
	tests := []struct {
		name   string
		status model.Status
	}{
		{"empty", model.Status("")},
		{"unknown", model.Status("unknown")},
		{"IN_PROGRESS", model.Status("IN_PROGRESS")},
		{"Done", model.Status("Done")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.False(t, tt.status.IsValid(), "status %q should be invalid", tt.status)
		})
	}
}

// TestStatus_IsTerminal verifies terminal status detection per FR-3.9.
func TestStatus_IsTerminal(t *testing.T) {
	tests := []struct {
		status   model.Status
		terminal bool
	}{
		{model.StatusOpen, false},
		{model.StatusInProgress, false},
		{model.StatusBlocked, false},
		{model.StatusDeferred, false},
		{model.StatusDone, true},
		{model.StatusCancelled, true},
		{model.StatusInvalidated, true},
	}

	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			assert.Equal(t, tt.terminal, tt.status.IsTerminal(),
				"status %q terminal should be %v", tt.status, tt.terminal)
		})
	}
}

// TestAllStatuses_Returns7Statuses verifies the complete list.
func TestAllStatuses_Returns7Statuses(t *testing.T) {
	all := model.AllStatuses()
	assert.Len(t, all, 7, "should have exactly 7 statuses")
}

// TestPriority_IsValid verifies priority range [1,5].
func TestPriority_IsValid(t *testing.T) {
	tests := []struct {
		name     string
		priority model.Priority
		valid    bool
	}{
		{"zero", 0, false},
		{"critical", model.PriorityCritical, true},
		{"high", model.PriorityHigh, true},
		{"medium", model.PriorityMedium, true},
		{"low", model.PriorityLow, true},
		{"backlog", model.PriorityBacklog, true},
		{"six", 6, false},
		{"negative", -1, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.valid, tt.priority.IsValid())
		})
	}
}

// TestDepType_IsValid verifies all dependency types.
func TestDepType_IsValid(t *testing.T) {
	tests := []struct {
		name    string
		depType model.DepType
		valid   bool
	}{
		{"blocks", model.DepTypeBlocks, true},
		{"related", model.DepTypeRelated, true},
		{"discovered_from", model.DepTypeDiscoveredFrom, true},
		{"duplicates", model.DepTypeDuplicates, true},
		{"empty", model.DepType(""), false},
		{"invalid", model.DepType("depends_on"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.valid, tt.depType.IsValid())
		})
	}
}
