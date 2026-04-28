// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Package model defines the core domain types for mtix.
// This package has zero dependencies on other mtix packages.
package model

import "errors"

// Sentinel errors for mtix operations per FR-7.7.
// These errors MUST be used consistently across all layers.
// Wrap them with context using fmt.Errorf("context: %w", ErrSentinel).
var (
	// ErrNotFound indicates the requested resource does not exist.
	ErrNotFound = errors.New("not found")

	// ErrAlreadyExists indicates a resource with the given ID already exists.
	ErrAlreadyExists = errors.New("already exists")

	// ErrInvalidInput indicates the request contains invalid parameters.
	ErrInvalidInput = errors.New("invalid input")

	// ErrInvalidTransition indicates a status transition that violates the state machine (FR-3.5).
	ErrInvalidTransition = errors.New("invalid transition")

	// ErrCycleDetected indicates a circular dependency was detected (FR-4.3).
	ErrCycleDetected = errors.New("cycle detected")

	// ErrConflict indicates a concurrent modification conflict.
	ErrConflict = errors.New("conflict")

	// ErrAlreadyClaimed indicates the node is already assigned to another agent (FR-10.4).
	ErrAlreadyClaimed = errors.New("already claimed")

	// ErrNodeBlocked indicates the node has unresolved blocking dependencies (FR-3.8).
	ErrNodeBlocked = errors.New("node blocked")

	// ErrStillDeferred indicates the node's defer_until has not yet passed.
	ErrStillDeferred = errors.New("still deferred")

	// ErrAgentStillActive indicates the agent has an active session that must be ended first.
	ErrAgentStillActive = errors.New("agent still active")

	// ErrNoActiveSession indicates no active LLM session exists for the agent.
	ErrNoActiveSession = errors.New("no active session")

	// ErrInvalidConfigKey indicates the configuration key is not recognized.
	ErrInvalidConfigKey = errors.New("invalid config key")

	// ErrDepthWarning is advisory only per FR-1.1a.
	// It signals that a node exceeds the recommended depth of 50
	// but does NOT reject the operation.
	ErrDepthWarning = errors.New("depth warning")

	// ErrSyncQueueFull indicates the local sync_events queue has reached
	// its configured cap (sync.max_queue_size meta key) per FR-18 / MTIX-15.2.4.
	// Callers MUST surface a one-line guide such as: "mtix sync push --force,
	// or set sync.max_queue_size to 0".
	ErrSyncQueueFull = errors.New("sync queue full")

	// ErrSyncDivergentHistory indicates the local first_event_hash for a
	// project does not match the hub's first_event_hash for the same
	// prefix per FR-18.13 / SYNC-DESIGN section 10. Callers MUST refuse
	// to proceed and surface the four resolution paths to the user
	// (--discard-local, --rename-to, --import-as, --dry-run).
	ErrSyncDivergentHistory = errors.New("sync history divergent")

	// ErrSyncReconcilePrefixCollision indicates --rename-to NEWPREFIX
	// was attempted but the hub already owns NEWPREFIX with a different
	// first_event_hash per FR-18.13. The check happens BEFORE any local
	// mutation so the user can pick another prefix without rollback.
	ErrSyncReconcilePrefixCollision = errors.New("sync reconcile: prefix collision on hub")
)
