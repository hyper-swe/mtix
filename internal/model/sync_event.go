// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Package model — sync event types per FR-18.6 / SYNC-DESIGN.md §3.1.
//
// A SyncEvent is the unit of replication between local CLIs and the shared
// sync hub. Events are append-only on the hub. Each mutation that writes to
// the local nodes table also writes exactly one (or for multi-field updates,
// one per field) sync_events row in the same transaction (FR-18.3).
//
// This file defines the on-the-wire and on-disk shape. The 12 op_type
// payloads live in sync_payload.go. The VectorClock type lives in
// sync_clock.go.
package model

import (
	"encoding/json"
	"fmt"
	"regexp"
	"time"
)

// OpType enumerates the 12 operation types per SYNC-DESIGN.md §3.3.
// Adding a new op_type is a major protocol bump (FR-18.14).
type OpType string

const (
	OpCreateNode       OpType = "create_node"
	OpUpdateField      OpType = "update_field"
	OpTransitionStatus OpType = "transition_status"
	OpClaim            OpType = "claim"
	OpUnclaim          OpType = "unclaim"
	OpDefer            OpType = "defer"
	OpComment          OpType = "comment"
	OpLinkDep          OpType = "link_dep"
	OpUnlinkDep        OpType = "unlink_dep"
	OpDelete           OpType = "delete"
	OpSetAcceptance    OpType = "set_acceptance"
	OpSetPrompt        OpType = "set_prompt"
)

// AllOpTypes is the canonical list. Used by schema CHECK constraints,
// fuzz targets, and exhaustive-switch lints.
var AllOpTypes = []OpType{
	OpCreateNode, OpUpdateField, OpTransitionStatus,
	OpClaim, OpUnclaim, OpDefer,
	OpComment, OpLinkDep, OpUnlinkDep,
	OpDelete, OpSetAcceptance, OpSetPrompt,
}

// IsValid reports whether s is one of the 12 known op_types.
func (o OpType) IsValid() bool {
	for _, v := range AllOpTypes {
		if v == o {
			return true
		}
	}
	return false
}

// SyncStatus is the local-mirror replication state per FR-18.6.
// The hub mirror always equals "applied"; this enum is local-only.
type SyncStatus string

const (
	SyncStatusPending    SyncStatus = "pending"
	SyncStatusPushed     SyncStatus = "pushed"
	SyncStatusConflicted SyncStatus = "conflicted"
	SyncStatusApplied    SyncStatus = "applied"
)

// AllSyncStatuses is the canonical list for CHECK constraints.
var AllSyncStatuses = []SyncStatus{
	SyncStatusPending, SyncStatusPushed,
	SyncStatusConflicted, SyncStatusApplied,
}

// IsValid reports whether s is one of the 4 known sync statuses.
func (s SyncStatus) IsValid() bool {
	for _, v := range AllSyncStatuses {
		if v == s {
			return true
		}
	}
	return false
}

// authorIDPattern enforces the FR-18.7 author_id grammar.
var authorIDPattern = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)

// projectPrefixPattern enforces the SYNC-DESIGN §5.1 project_prefix grammar.
// Underscore is permitted to match existing mtix project naming conventions
// (e.g. "DEP_ADD" used in tests; user projects may also contain underscores).
var projectPrefixPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,15}$`)

// machineHashPattern enforces the 16-hex shape produced by sync/clock.MachineHash.
var machineHashPattern = regexp.MustCompile(`^[a-f0-9]{16}$`)

// SyncEvent is the canonical replication record per FR-18.6.
//
// Field ordering matches the SQLite table definition for cache-friendly
// scanning. JSON tags use snake_case to align with the wire protocol.
type SyncEvent struct {
	EventID           string          `json:"event_id"`
	ProjectPrefix     string          `json:"project_prefix"`
	NodeID            string          `json:"node_id"`
	OpType            OpType          `json:"op_type"`
	Payload           json.RawMessage `json:"payload"`
	WallClockTS       int64           `json:"wall_clock_ts"`
	LamportClock      int64           `json:"lamport_clock"`
	VectorClock       VectorClock     `json:"vector_clock"`
	AuthorID          string          `json:"author_id"`
	AuthorMachineHash string          `json:"author_machine_hash"`
	SyncStatus        SyncStatus      `json:"sync_status,omitempty"`
	CreatedAt         time.Time       `json:"created_at"`
	RetainedUntil     *time.Time      `json:"retained_until,omitempty"`
}

// Validate enforces the FR-18.7 / SYNC-DESIGN §5.1 schema validation rules
// that do not require the hub to evaluate (size limits and JSON depth are
// hub-side, applied in MTIX-15.3 before persistence).
//
// Returns a wrapped ErrInvalidInput with the failing field named so the
// caller can surface a structured error.
func (e *SyncEvent) Validate() error {
	if e.EventID == "" {
		return fmt.Errorf("event_id required: %w", ErrInvalidInput)
	}
	if !e.OpType.IsValid() {
		return fmt.Errorf("op_type %q not in canonical 12: %w", e.OpType, ErrInvalidInput)
	}
	if e.SyncStatus != "" && !e.SyncStatus.IsValid() {
		return fmt.Errorf("sync_status %q not in canonical 4: %w", e.SyncStatus, ErrInvalidInput)
	}
	if e.NodeID == "" {
		return fmt.Errorf("node_id required: %w", ErrInvalidInput)
	}
	if !projectPrefixPattern.MatchString(e.ProjectPrefix) {
		return fmt.Errorf("project_prefix %q does not match ^[A-Z][A-Z0-9]{0,15}$: %w",
			e.ProjectPrefix, ErrInvalidInput)
	}
	if !authorIDPattern.MatchString(e.AuthorID) {
		return fmt.Errorf("author_id %q does not match ^[a-z0-9_-]{1,64}$: %w",
			e.AuthorID, ErrInvalidInput)
	}
	if !machineHashPattern.MatchString(e.AuthorMachineHash) {
		return fmt.Errorf("author_machine_hash %q does not match ^[a-f0-9]{16}$: %w",
			e.AuthorMachineHash, ErrInvalidInput)
	}
	if e.WallClockTS < 0 {
		return fmt.Errorf("wall_clock_ts negative (%d): %w", e.WallClockTS, ErrInvalidInput)
	}
	if e.LamportClock < 0 {
		return fmt.Errorf("lamport_clock negative (%d): %w", e.LamportClock, ErrInvalidInput)
	}
	if len(e.Payload) == 0 {
		return fmt.Errorf("payload required (use 'null' for empty): %w", ErrInvalidInput)
	}
	if e.VectorClock == nil {
		return fmt.Errorf("vector_clock required (use empty map for none): %w", ErrInvalidInput)
	}
	return nil
}
