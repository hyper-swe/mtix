// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Package store defines the data access contract for mtix.
// The Store interface is the central shared contract consumed by
// service, CLI, API, MCP, and gRPC layers per CONTRIBUTING-LLM.md §8.2.
package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

// ListOptions controls pagination for list queries.
type ListOptions struct {
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
}

// NodeFilter defines filtering criteria for node queries.
type NodeFilter struct {
	Status   []model.Status `json:"status,omitempty"`
	Under    string         `json:"under,omitempty"`
	Assignee string         `json:"assignee,omitempty"`
	NodeType string         `json:"node_type,omitempty"`
	Priority *int           `json:"priority,omitempty"`
	Labels   []string       `json:"labels,omitempty"`
}

// Store defines the data access contract for mtix.
// All implementations MUST be safe for concurrent use.
// All write operations MUST be wrapped in transactions.
// All SQL MUST use parameterized queries — no string concatenation.
type Store interface {
	// Node operations

	// CreateNode persists a new node. The node's ID must be pre-generated.
	// Returns ErrAlreadyExists if a node with the given ID already exists.
	CreateNode(ctx context.Context, node *model.Node) error

	// GetNode retrieves a node by its dot-notation ID.
	// Returns ErrNotFound if the node does not exist or is soft-deleted.
	GetNode(ctx context.Context, id string) (*model.Node, error)

	// UpdateNode applies partial updates to a node.
	// Returns ErrNotFound if the node does not exist.
	UpdateNode(ctx context.Context, id string, updates *NodeUpdate) error

	// DeleteNode soft-deletes a node.
	// If cascade is true, all descendants are also soft-deleted.
	// Returns ErrNotFound if the node does not exist.
	DeleteNode(ctx context.Context, id string, cascade bool, deletedBy string) error

	// UndeleteNode restores a soft-deleted node.
	// Returns ErrNotFound if the node does not exist.
	UndeleteNode(ctx context.Context, id string) error

	// ListNodes returns nodes matching the filter with pagination.
	// The third return value is the total count matching the filter.
	ListNodes(ctx context.Context, filter NodeFilter, opts ListOptions) ([]*model.Node, int, error)

	// SearchNodes performs full-text search via FTS5 per NFR-2.7.
	// Returns nodes matching the query ranked by relevance.
	// Excludes soft-deleted nodes. Supports additional filter criteria.
	SearchNodes(ctx context.Context, query string, filter NodeFilter, opts ListOptions) ([]*model.Node, int, error)

	// GetTree returns a node and all descendants up to maxDepth levels deep.
	// The returned tree is a flat list; callers use parent_id to reconstruct hierarchy.
	// If maxDepth is 0, returns only the root node.
	// Excludes soft-deleted nodes.
	GetTree(ctx context.Context, rootID string, maxDepth int) ([]*model.Node, error)

	// GetStats returns aggregate statistics for a given scope.
	// If scopeID is empty, returns global statistics.
	GetStats(ctx context.Context, scopeID string) (*Stats, error)

	// Sequence operations

	// NextSequence atomically increments and returns the next sequence number
	// for the given key (project:parent_dotpath) per FR-2.7.
	NextSequence(ctx context.Context, key string) (int, error)

	// Dependency operations

	// AddDependency creates a dependency between two nodes.
	// Returns ErrCycleDetected if the dependency would create a cycle (for blocks type).
	AddDependency(ctx context.Context, dep *model.Dependency) error

	// RemoveDependency removes a dependency.
	RemoveDependency(ctx context.Context, fromID, toID string, depType model.DepType) error

	// GetBlockers returns all unresolved blocking dependencies for a node.
	GetBlockers(ctx context.Context, nodeID string) ([]*model.Dependency, error)

	// State machine operations

	// TransitionStatus changes a node's status per FR-3.5 state machine rules.
	// Validates the transition, records activity, sets closed_at as appropriate.
	// Returns ErrInvalidTransition if the transition is not allowed.
	TransitionStatus(ctx context.Context, id string, toStatus model.Status, reason, author string) error

	// ClaimNode atomically claims a node for an agent per FR-10.4.
	// Sets assignee and transitions to in_progress.
	// Returns ErrAlreadyClaimed, ErrNodeBlocked, ErrStillDeferred, or ErrInvalidTransition.
	ClaimNode(ctx context.Context, id, agentID string) error

	// UnclaimNode releases a node assignment per FR-10.4.
	// Requires a reason. Sets status back to open.
	UnclaimNode(ctx context.Context, id, reason, author string) error

	// ForceReclaimNode reclaims a node from a stale agent per FR-10.4a.
	// Succeeds only if the current assignee's last heartbeat exceeds staleThreshold.
	ForceReclaimNode(ctx context.Context, id, agentID string, staleThreshold time.Duration) error

	// CancelNode cancels a node with mandatory reason per FR-6.3.
	// If cascade is true, all descendants are also cancelled.
	// Recalculates progress excluding cancelled nodes (FR-5.4).
	CancelNode(ctx context.Context, id, reason, author string, cascade bool) error

	// Progress operations

	// UpdateProgress sets the progress value for a node.
	UpdateProgress(ctx context.Context, id string, progress float64) error

	// GetDirectChildren returns direct children of a node (excluding soft-deleted).
	GetDirectChildren(ctx context.Context, parentID string) ([]*model.Node, error)

	// Activity operations

	// GetActivity returns activity entries for a node with pagination per FR-3.6.
	// Returns entries ordered chronologically (oldest first).
	// Returns ErrNotFound if the node does not exist or is soft-deleted.
	GetActivity(ctx context.Context, nodeID string, limit, offset int) ([]model.ActivityEntry, error)

	// Context and annotation operations

	// GetAncestorChain returns ancestors from root to the node itself (inclusive),
	// ordered root-first per FR-12.2.
	GetAncestorChain(ctx context.Context, nodeID string) ([]*model.Node, error)

	// GetSiblings returns direct children of the node's parent,
	// excluding the node itself and soft-deleted nodes.
	GetSiblings(ctx context.Context, nodeID string) ([]*model.Node, error)

	// SetAnnotations replaces all annotations on a node per FR-3.4.
	SetAnnotations(ctx context.Context, nodeID string, annotations []model.Annotation) error

	// Raw query access (for background service operations).

	// Query executes a read query, returning rows.
	// The caller is responsible for closing the returned Rows.
	Query(ctx context.Context, query string, args ...any) (*sql.Rows, error)

	// QueryRow executes a read query returning a single row.
	QueryRow(ctx context.Context, query string, args ...any) *sql.Row

	// WriteDB returns the write database pool for operations that need
	// direct write access outside of transactions (e.g., permanent deletion).
	WriteDB() *sql.DB

	// Lifecycle

	// Close closes the store and releases all resources.
	// Implements io.Closer.
	Close() error
}

// Stats holds aggregate statistics per FR-2.7.5.
type Stats struct {
	TotalNodes     int            `json:"total_nodes"`
	ByStatus       map[string]int `json:"by_status"`
	ByPriority     map[string]int `json:"by_priority"`
	ByType         map[string]int `json:"by_type"`
	Progress       float64        `json:"progress"`
	ScopeID        string         `json:"scope_id,omitempty"`
}

// NodeUpdate represents a partial update to a node.
// Only non-nil fields are applied.
type NodeUpdate struct {
	Title       *string         `json:"title,omitempty"`
	Description *string         `json:"description,omitempty"`
	Prompt      *string         `json:"prompt,omitempty"`
	Acceptance  *string         `json:"acceptance,omitempty"`
	Status      *model.Status   `json:"status,omitempty"`
	Priority    *model.Priority `json:"priority,omitempty"`
	Labels      []string        `json:"labels,omitempty"`
	Assignee    *string         `json:"assignee,omitempty"`
	AgentState  *model.AgentState `json:"agent_state,omitempty"`
	ContentHash *string         `json:"content_hash,omitempty"`
	UpdatedAt   *string         `json:"updated_at,omitempty"`
}
