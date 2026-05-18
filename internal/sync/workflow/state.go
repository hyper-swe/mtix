// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Package workflow detects sync state from local SQLite + filesystem
// and renders rule-based recommendations for the mtix_sync_workflow
// MCP tool per FR-18 / MTIX-15.8.
//
// State detection is local-only. No PG connection is opened from this
// package — that's the safety boundary. The output never carries DSN
// bytes; HasDSN is a boolean signal only. See state_test.go's
// TestDetectState_NeverSurfacesRawDSN regression.
package workflow

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// State enumerates the 5 sync states per FR-18 / SYNC-DESIGN.
type State int

const (
	// StateSolo: no DSN configured anywhere; user is operating local-only.
	StateSolo State = iota
	// StateSyncConfiguredNoHub: DSN reachable but no events emitted/applied yet.
	StateSyncConfiguredNoHub
	// StateSyncActive: DSN configured AND events flowing AND no unresolved conflicts.
	StateSyncActive
	// StateDivergentPending: unresolved entries in sync_conflicts.
	// Takes priority over SyncActive when both could apply.
	StateDivergentPending
	// StateHubUnreachable: DSN configured AND ConsecutiveErrors >= ConsecutiveErrorsThreshold.
	StateHubUnreachable
)

// ConsecutiveErrorsThreshold is the trip point for StateHubUnreachable.
// Three consecutive errors over the retry/backoff envelope strongly
// implies the hub is genuinely unreachable rather than a flake.
const ConsecutiveErrorsThreshold = 3

// String returns the stable, MCP-tool-contract name for each state.
// These names appear verbatim in tool output; do not rename.
func (s State) String() string {
	switch s {
	case StateSolo:
		return "solo"
	case StateSyncConfiguredNoHub:
		return "sync-configured-no-hub"
	case StateSyncActive:
		return "sync-active"
	case StateDivergentPending:
		return "divergent-state-pending"
	case StateHubUnreachable:
		return "hub-unreachable"
	}
	return "unknown"
}

// Report is the structured payload returned by DetectState.
//
// IMPORTANT: this struct MUST NOT carry the DSN value, hostnames, or
// any PG-side error strings. HasDSN is a presence boolean only.
// FR-18.17 regression test asserts the raw DSN never appears in any
// JSON encoding of this struct.
type Report struct {
	State                  State `json:"state_code"`
	StateName              string `json:"state"`
	HasDSN                 bool  `json:"has_dsn"`
	HasUnresolvedConflicts bool  `json:"has_unresolved_conflicts"`
	LocalEventCount        int   `json:"local_event_count"`
	AppliedEventCount      int   `json:"applied_event_count"`
	ConsecutiveErrors      int   `json:"consecutive_errors"`
	// LocalNodeCount carries the count of rows in the canonical `nodes`
	// table. Used by the recommendation engine to detect the
	// v0.1.x → v0.2.0-beta upgrader case (LocalNodeCount > 0 but
	// LocalEventCount == 0) and surface the `mtix sync backfill`
	// hint per MTIX-15.13.1.
	LocalNodeCount int `json:"local_node_count"`
}

// stateReader is the minimal database surface DetectState needs.
// Both *sql.DB and *sql.Tx satisfy it; tests can pass an in-memory DB
// without depending on the sqlite store package.
type stateReader interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// DetectState classifies the local sync state into one of 5 buckets
// per FR-18 / MTIX-15.8.1. Reads only local SQLite + filesystem;
// never opens a PG connection.
//
// Precedence (when multiple conditions apply):
//
//	divergent-pending  > hub-unreachable > sync-active >
//	sync-configured-no-hub > solo
//
// The agent should surface the highest-priority state since it
// always implies the largest action.
func DetectState(ctx context.Context, db stateReader, mtixDir string) (Report, error) {
	hasDSN, err := dsnConfigured(mtixDir)
	if err != nil {
		// Filesystem error reading the secrets file — surface but treat as no DSN.
		// We don't propagate the path in the report (no leakage of mtixDir).
		return Report{}, fmt.Errorf("dsn presence check: %w", err)
	}

	machineHash, err := readMetaString(ctx, db, "meta.sync.machine_hash")
	if err != nil {
		return Report{}, fmt.Errorf("read machine_hash: %w", err)
	}
	consecutiveErrors, err := readMetaInt(ctx, db, "meta.sync.consecutive_errors")
	if err != nil {
		return Report{}, fmt.Errorf("read consecutive_errors: %w", err)
	}
	localEventCount, err := scanCount(ctx, db, `SELECT COUNT(*) FROM sync_events`)
	if err != nil {
		return Report{}, fmt.Errorf("count sync_events: %w", err)
	}
	appliedEventCount, err := scanCount(ctx, db, `SELECT COUNT(*) FROM applied_events`)
	if err != nil {
		return Report{}, fmt.Errorf("count applied_events: %w", err)
	}
	unresolvedCount, err := scanCount(ctx, db,
		`SELECT COUNT(*) FROM sync_conflicts WHERE resolved_at IS NULL`)
	if err != nil {
		return Report{}, fmt.Errorf("count unresolved conflicts: %w", err)
	}
	// Best-effort node count for the upgrader-detection heuristic.
	// The `nodes` table may not exist in test environments that use a
	// hand-rolled schema (e.g. internal/sync/workflow tests). Failures
	// degrade to 0 — the recommendation just doesn't fire.
	localNodeCount, _ := scanCount(ctx, db, `SELECT COUNT(*) FROM nodes WHERE deleted_at IS NULL`)

	r := Report{
		HasDSN:                 hasDSN,
		HasUnresolvedConflicts: unresolvedCount > 0,
		LocalEventCount:        localEventCount,
		AppliedEventCount:      appliedEventCount,
		ConsecutiveErrors:      consecutiveErrors,
		LocalNodeCount:         localNodeCount,
	}
	r.State = classify(r, machineHash)
	r.StateName = r.State.String()
	return r, nil
}

// classify applies precedence rules. Pure function for easy testing.
func classify(r Report, machineHash string) State {
	if r.HasUnresolvedConflicts {
		return StateDivergentPending
	}
	if !r.HasDSN {
		return StateSolo
	}
	if r.ConsecutiveErrors >= ConsecutiveErrorsThreshold {
		return StateHubUnreachable
	}
	hasActivity := r.LocalEventCount > 0 || r.AppliedEventCount > 0
	if hasActivity && machineHash != "" {
		return StateSyncActive
	}
	return StateSyncConfiguredNoHub
}

// dsnConfigured reports whether either the env var or .mtix/secrets
// supplies a non-empty DSN. Returns the boolean only — never the value.
func dsnConfigured(mtixDir string) (bool, error) {
	if v := strings.TrimSpace(os.Getenv("MTIX_SYNC_DSN")); v != "" {
		return true, nil
	}
	if mtixDir == "" {
		return false, nil
	}
	secretsPath := filepath.Join(mtixDir, "secrets")
	body, err := os.ReadFile(secretsPath) //nolint:gosec // path is mtixDir + canonical filename
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return strings.TrimSpace(string(body)) != "", nil
}

// readMetaString returns the meta value or "" if absent.
func readMetaString(ctx context.Context, db stateReader, key string) (string, error) {
	var v string
	err := db.QueryRowContext(ctx,
		`SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return v, nil
}

// readMetaInt parses a meta value as int. Treats absent/empty as 0
// (matches the schema default '0'). A garbage value is reported as
// an error rather than silently coerced to 0.
func readMetaInt(ctx context.Context, db stateReader, key string) (int, error) {
	v, err := readMetaString(ctx, db, key)
	if err != nil {
		return 0, err
	}
	if v == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("meta %q has non-integer value", key)
	}
	return n, nil
}

// scanCount runs a COUNT(*) query and returns the integer.
func scanCount(ctx context.Context, db stateReader, query string) (int, error) {
	var n int
	if err := db.QueryRowContext(ctx, query).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}
