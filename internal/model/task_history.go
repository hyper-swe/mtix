// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model

import "time"

// HistoryKind says what a task history entry records (ADR-006 A3, MTIX-95.31.19).
type HistoryKind string

const (
	// HistoryKindChange records one changed field of a task.
	HistoryKindChange HistoryKind = "change"

	// HistoryKindRedaction is reserved for the redaction marker (MTIX-95.31.19.1).
	HistoryKindRedaction HistoryKind = "redaction"

	// HistoryKindResolution records an explicit merge workflow choice.
	HistoryKindResolution HistoryKind = "resolution"

	// HistoryKindRetentionDrop is reserved for the retention-drop marker (MTIX-95.31.19.4).
	HistoryKindRetentionDrop HistoryKind = "retention_drop"
)

// HistoryOrigin says where a change came from.
type HistoryOrigin string

const (
	// HistoryOriginLocal is a write made on this machine through mtix.
	HistoryOriginLocal HistoryOrigin = "local"

	// HistoryOriginImport is a write applied by a board import.
	HistoryOriginImport HistoryOrigin = "import"

	// HistoryOriginHub is a write applied from the sync hub.
	HistoryOriginHub HistoryOrigin = "hub"

	// HistoryOriginSystem is an automatic write such as a dependency unblock.
	HistoryOriginSystem HistoryOrigin = "system"
)

// HistoryEntry is one immutable row of a task's change history (MTIX-95.31.19).
//
// Shape: ID is a ULID minted once and never reused; TaskUID is the task's
// durable uid (its dot-path id for a legacy task without one); Timestamp comes
// from the store's injected clock; Actor is the resolved author; EventID
// optionally names the causal sync event. Old and New are JSON text for a
// structured field. nil means the entry carries no value: a free-text field,
// or the Old of a task's first entries (it had no earlier state). The JSON
// text "null" is a stored NULL: a structured field the task had cleared or
// never set. A stored NULL and an empty string are the same empty value, and
// for a list field so is "[]", so rewriting one as the other is not a change. For a
// free-text field Old and New are always nil: neither the text nor a hash of
// it is stored, because a hash of a short secret can be brute-forced.
type HistoryEntry struct {
	ID        string        `json:"id"`
	TaskUID   string        `json:"task_uid"`
	Timestamp time.Time     `json:"timestamp"`
	Actor     string        `json:"actor"`
	Origin    HistoryOrigin `json:"origin"`
	Kind      HistoryKind   `json:"kind"`
	Field     string        `json:"field"`
	Old       *string       `json:"old,omitempty"`
	New       *string       `json:"new,omitempty"`
	EventID   string        `json:"event_id,omitempty"`
}

// HistoryFieldClass classifies a task field for history recording.
type HistoryFieldClass int

const (
	// HistoryFieldValueFree is the default: record that the field changed,
	// never its old or new value. Every free-text field, and every field not
	// in the registry, is value-free.
	HistoryFieldValueFree HistoryFieldClass = iota

	// HistoryFieldStructured fields keep their old and new values.
	HistoryFieldStructured
)

// HistoryFieldClassOf returns how a field is recorded. Only the explicit
// structured fields keep values (ADR-006 A3 parent contract); every other
// field, including every free-text field and any future one, is value-free.
func HistoryFieldClassOf(field string) HistoryFieldClass {
	switch field {
	case "status", "assignee", "priority", "labels", "node_type",
		"issue_type", "parent", "defer_until", "deleted_at", "agent_state",
		"estimate_min", "actual_min", "weight", "display_path":
		return HistoryFieldStructured
	default:
		return HistoryFieldValueFree
	}
}
