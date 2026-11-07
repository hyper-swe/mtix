// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"fmt"
	"sort"

	"github.com/hyper-swe/mtix/internal/model"
)

// MTIX-15.6.3 dry-run support and prefix-collision guard. Both are
// pure data-layer functions with no side effects.

// Rename is one entry in a reconciliation plan: a single (old, new)
// node id pair that the executable function would write.
type Rename struct {
	OldID string `json:"old_id"`
	NewID string `json:"new_id"`
}

// Plan is what a DryRun returns. Mirrors the audit events that the
// corresponding executable function would emit, minus timestamps.
// Plan equivalence is asserted by TestDryRun_PlanMatchesExecutedAuditEvents
// in sync_reconcile_atomicity_test.go.
type Plan struct {
	Path      string   `json:"path"`                 // discard-local | rename-to | import-as
	NewPrefix string   `json:"new_prefix,omitempty"` // rename-to only
	ParentID  string   `json:"parent_id,omitempty"`  // import-as only
	NodeCount int      `json:"node_count"`
	Renames   []Rename `json:"renames"`
}

// DryRunDiscardLocal computes the plan that DiscardLocal would
// execute. DiscardLocal renames nothing; the plan reports the count
// of nodes that would be dropped.
func DryRunDiscardLocal(ctx context.Context, s *Store) (Plan, error) {
	var nodeCount int
	err := s.readDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM nodes WHERE deleted_at IS NULL`,
	).Scan(&nodeCount)
	if err != nil {
		return Plan{}, fmt.Errorf("DryRunDiscardLocal: %w", err)
	}
	return Plan{
		Path:      "discard-local",
		NodeCount: nodeCount,
		Renames:   []Rename{},
	}, nil
}

// DryRunRenameTo computes the plan that RenameTo would execute.
// Returns the rename mapping as the Plan.Renames slice.
//
// Refuses with model.ErrInvalidInput if newPrefix is malformed —
// matches RenameTo's pre-flight check so dry-run and real-run produce
// identical errors for invalid input.
func DryRunRenameTo(ctx context.Context, s *Store, newPrefix string) (Plan, error) {
	if !isValidProjectPrefix(newPrefix) {
		return Plan{}, fmt.Errorf("DryRunRenameTo: invalid newPrefix %q: %w",
			newPrefix, model.ErrInvalidInput)
	}
	mapping, err := buildRenameMapping(ctx, s, newPrefix)
	if err != nil {
		return Plan{}, fmt.Errorf("DryRunRenameTo: %w", err)
	}
	return Plan{
		Path:      "rename-to",
		NewPrefix: newPrefix,
		NodeCount: len(mapping),
		Renames:   sortedRenames(mapping),
	}, nil
}

// DryRunImportAs computes the plan that ImportAs would execute.
// Refuses with model.ErrNotFound if parentID is not in the local
// store (matches the executable function's precondition check).
func DryRunImportAs(ctx context.Context, s *Store, parentID string) (Plan, error) {
	if parentID == "" {
		return Plan{}, fmt.Errorf("DryRunImportAs: parentID required: %w", model.ErrInvalidInput)
	}
	var exists int
	err := s.readDB.QueryRowContext(ctx,
		`SELECT 1 FROM nodes WHERE id = ?`, parentID,
	).Scan(&exists)
	if err != nil {
		return Plan{}, fmt.Errorf("DryRunImportAs: parent %s not in local store: %w",
			parentID, model.ErrNotFound)
	}
	mapping, err := buildImportAsMapping(ctx, s, parentID)
	if err != nil {
		return Plan{}, fmt.Errorf("DryRunImportAs: %w", err)
	}
	return Plan{
		Path:      "import-as",
		ParentID:  parentID,
		NodeCount: len(mapping),
		Renames:   sortedRenames(mapping),
	}, nil
}

// sortedRenames flattens the rename map into a deterministic slice,
// sorted by old id so plan output is stable across runs.
func sortedRenames(mapping map[string]string) []Rename {
	keys := make([]string, 0, len(mapping))
	for k := range mapping {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]Rename, 0, len(mapping))
	for _, k := range keys {
		out = append(out, Rename{OldID: k, NewID: mapping[k]})
	}
	return out
}

// CheckPrefixCollision returns wrapped model.ErrSyncReconcilePrefixCollision
// if the hub already owns newPrefix per FR-18.13. The check happens
// BEFORE any local mutation so the user can pick another prefix
// without rollback.
//
// hubProjects is the prefix -> first_event_hash map the caller fetches
// from the hub via PullSyncProjects (lands in MTIX-15.7's CLI surface).
// Empty hash means the hub knows the prefix existed but has no anchor;
// either way it's a collision.
func CheckPrefixCollision(hubProjects map[string]string, newPrefix string) error {
	if newPrefix == "" {
		return fmt.Errorf("CheckPrefixCollision: newPrefix required: %w", model.ErrInvalidInput)
	}
	if _, taken := hubProjects[newPrefix]; taken {
		return fmt.Errorf("hub already owns prefix %q (cannot rename to a colliding name): %w",
			newPrefix, model.ErrSyncReconcilePrefixCollision)
	}
	return nil
}

// reconcileFailAfterN is a chaos hook used by atomicity tests. When
// non-nil and called for the Nth time, returns its embedded error so
// the caller's tx rolls back. Production code does not set this hook.
//
// The hook is checked inside applyRenameLoop just after each
// successful per-row rename — that's the latest point where a tx-
// internal failure is observable end-to-end (subsequent failures
// outside the loop are the WithTx caller's responsibility).
//
//nolint:gochecknoglobals // intentional test seam
var reconcileFailAfterN func(callIdx int) error

// resetReconcileChaosHook clears the hook. Tests use it via t.Cleanup.
//
//nolint:unused // used by the atomicity test file
func resetReconcileChaosHook() {
	reconcileFailAfterN = nil
}
