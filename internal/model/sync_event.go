// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Package model defines the SyncEvent domain type per NFR-3.2.
// A SyncEvent is generated for every write operation to enable
// optional cloud sync. Events are append-only and immutable.
package model

// SyncOperation enumerates the types of write operations
// that generate sync events per NFR-3.2.
type SyncOperation string

const (
	// SyncOpCreate indicates a node was created.
	SyncOpCreate SyncOperation = "create"
	// SyncOpUpdate indicates a node field was updated.
	SyncOpUpdate SyncOperation = "update"
	// SyncOpDelete indicates a node was deleted (soft or hard).
	SyncOpDelete SyncOperation = "delete"
	// SyncOpStatusChange indicates a node status transition.
	SyncOpStatusChange SyncOperation = "status_change"
)

// SyncEvent represents a single write operation recorded for sync per NFR-3.2.
// Events are append-only — never modified or deleted except after confirmed push.
type SyncEvent struct {
	// ID is the local auto-incrementing identifier.
	ID int64 `json:"id"`
	// NodeID is the dot-notation ID of the affected node.
	NodeID string `json:"node_id"`
	// Operation is the type of write (create, update, delete, status_change).
	Operation SyncOperation `json:"operation"`
	// Field is which field changed (nullable for create/delete).
	Field string `json:"field,omitempty"`
	// OldValue is the previous value (empty for create).
	OldValue string `json:"old_value,omitempty"`
	// NewValue is the new value (empty for delete).
	NewValue string `json:"new_value,omitempty"`
	// Timestamp is the ISO-8601 UTC time of the event.
	Timestamp string `json:"timestamp"`
	// Author is the agent or human who made the change.
	Author string `json:"author"`
	// VectorClock is the serialized JSON vector clock at event time.
	VectorClock string `json:"vector_clock"`
	// Pushed indicates whether this event has been synced to the cloud.
	Pushed bool `json:"pushed"`
}
