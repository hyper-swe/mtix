// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Package service implements the business logic layer for mtix.
// All business rules, validation, and orchestration live here.
// Handlers (CLI, REST, gRPC, MCP) MUST access data through this layer only.
package service

import (
	"context"
	"encoding/json"
	"time"
)

// EventType identifies the kind of event being broadcast.
type EventType string

const (
	// EventNodeCreated is broadcast when a new node is created.
	EventNodeCreated EventType = "node.created"

	// EventNodeUpdated is broadcast when a node is modified.
	EventNodeUpdated EventType = "node.updated"

	// EventNodeDeleted is broadcast when a node is soft-deleted.
	EventNodeDeleted EventType = "node.deleted"

	// EventNodeUndeleted is broadcast when a node is restored from soft-delete.
	EventNodeUndeleted EventType = "node.undeleted"

	// EventStatusChanged is broadcast when a node's status transitions.
	EventStatusChanged EventType = "node.status_changed"

	// EventProgressChanged is broadcast when a node's progress is recalculated.
	EventProgressChanged EventType = "progress.changed"

	// EventNodeClaimed is broadcast when an agent claims a node.
	EventNodeClaimed EventType = "node.claimed"

	// EventNodeUnclaimed is broadcast when an agent releases a node.
	EventNodeUnclaimed EventType = "node.unclaimed"

	// EventNodeCancelled is broadcast when a node is cancelled.
	EventNodeCancelled EventType = "node.cancelled"

	// EventNodesInvalidated is broadcast as a batch when nodes are invalidated per FR-7.5a.
	EventNodesInvalidated EventType = "nodes.invalidated"

	// EventDependencyAdded is broadcast when a dependency is created.
	EventDependencyAdded EventType = "dependency.added"

	// EventDependencyRemoved is broadcast when a dependency is removed.
	EventDependencyRemoved EventType = "dependency.removed"

	// EventAgentStateChanged is broadcast when an agent's state transitions.
	EventAgentStateChanged EventType = "agent.state"

	// EventAgentStuck is broadcast when an agent reports stuck per FR-10.3a.
	EventAgentStuck EventType = "agent.stuck"
)

// Event represents a domain event broadcast after a successful mutation.
type Event struct {
	// Type identifies the event.
	Type EventType `json:"type"`

	// NodeID is the primary node affected by the event.
	NodeID string `json:"node_id"`

	// Timestamp is when the event occurred (UTC).
	Timestamp time.Time `json:"timestamp"`

	// Author is the agent or user who triggered the event.
	Author string `json:"author,omitempty"`

	// Data contains event-specific payload.
	Data json.RawMessage `json:"data,omitempty"`
}

// EventBroadcaster publishes domain events after successful mutations.
// Implementations include WebSocket broadcast (FR-7.5), sync event generation
// (NFR-3.2), and in-memory test doubles.
type EventBroadcaster interface {
	// Broadcast publishes an event to all subscribers.
	// Broadcast MUST NOT block — if delivery fails, log and continue.
	// Returns an error only for configuration-level failures (e.g., broadcaster closed).
	Broadcast(ctx context.Context, event Event) error
}

// NoopBroadcaster is a no-op implementation of EventBroadcaster for testing
// and CLI-only mode where no subscribers exist.
type NoopBroadcaster struct{}

// Broadcast does nothing and returns nil.
func (n *NoopBroadcaster) Broadcast(_ context.Context, _ Event) error {
	return nil
}
