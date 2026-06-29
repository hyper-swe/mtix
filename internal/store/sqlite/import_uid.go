// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/sync/clock"
)

// ErrImportConfirmationRequired is returned by ImportReconcile when applying the
// import would mutate an existing (non-empty) live store — by renumbering an
// incoming provisional node or otherwise — and the caller has not passed an
// explicit confirmation. Per ADR-003 §6 the offline/export-import path never
// silently mutates a live store: it renumbers incoming provisional nodes
// deterministically, emits a uid-keyed remap report, and applies only with
// confirmation.
var ErrImportConfirmationRequired = errors.New("import requires confirmation")

// ImportReconcileOptions controls how ImportReconcile validates and applies an
// import (ADR-003 §6, audit F-3).
type ImportReconcileOptions struct {
	// Mode selects replace vs. merge semantics (FR-7.8), as for Store.Import.
	Mode ImportMode
	// Force allows importing zero nodes into a non-empty database (FR-7.8).
	Force bool
	// ForceRename re-stamps an import node whose uid collides with a DIFFERENT
	// local node, minting it a fresh local uid instead of rejecting the import
	// (ADR-003 §6 — the explicit --force-rename escape hatch).
	ForceRename bool
	// Confirm authorizes applying mutations (provisional renumbers) to a
	// non-empty live store. Without it, such an import returns the remap report
	// and ErrImportConfirmationRequired without touching the store (ADR-003 §6).
	Confirm bool
}

// ImportConflictKind classifies an import-boundary uid collision (audit F-3).
type ImportConflictKind int

const (
	// ConflictLocalUIDMismatch is an incoming uid that duplicates an existing
	// LOCAL node carrying a DIFFERENT display_path (ADR-003 §6).
	ConflictLocalUIDMismatch ImportConflictKind = iota
	// ConflictExportDuplicateUID is two nodes WITHIN the export sharing one uid
	// — a buggy or crafted export that must never be silently linked
	// (ADR-003 §6, audit F-3).
	ConflictExportDuplicateUID
)

// String renders the conflict kind for the loud report (ADR-003 §6).
func (k ImportConflictKind) String() string {
	switch k {
	case ConflictLocalUIDMismatch:
		return "uid collides with a different local node"
	case ConflictExportDuplicateUID:
		return "duplicate uid within the export"
	default:
		return "unknown conflict"
	}
}

// ImportUIDConflict records a single rejected uid collision (ADR-003 §6, F-3).
type ImportUIDConflict struct {
	// UID is the colliding durable identity.
	UID string
	// ImportPath is the incoming node's display_path.
	ImportPath string
	// LocalPath is the existing node's display_path the uid resolves to: the
	// local node for ConflictLocalUIDMismatch, or the first export node for
	// ConflictExportDuplicateUID.
	LocalPath string
	// Kind classifies the collision.
	Kind ImportConflictKind
}

// ImportRemapEntry records that the node identified by UID moved from OldPath to
// NewPath during reconciliation — the content of the uid-keyed remap file
// (ADR-003 §6). For a force-renamed node OldPath/NewPath are equal but the uid
// itself is re-stamped (see ImportReconcileReport.Renamed).
type ImportRemapEntry struct {
	// UID is the durable identity whose display_path moved (or was re-stamped).
	UID string
	// OldPath is the display_path carried by the incoming export.
	OldPath string
	// NewPath is the clean local display_path the node settled into.
	NewPath string
}

// ImportReconcileReport is the loud, reviewable outcome of an import
// reconciliation (ADR-003 §6). It is produced even when the import is rejected
// or withheld for confirmation so a human can review exactly what would change.
type ImportReconcileReport struct {
	// Conflicts are rejected uid collisions (non-empty => the import failed
	// unless every collision was resolved by ForceRename).
	Conflicts []ImportUIDConflict
	// Remaps are provisional incoming nodes renumbered to clean local numbers,
	// keyed by uid (ADR-003 §6 — the remap file).
	Remaps []ImportRemapEntry
	// Renamed are import nodes re-stamped with a fresh local uid under
	// --force-rename (their OldPath uid is the colliding one).
	Renamed []ImportRemapEntry
	// Idempotent counts incoming nodes that were an exact uid+display_path
	// no-op against the local store (ADR-003 §6).
	Idempotent int
	// Applied is true once the (possibly rewritten) import has been committed.
	Applied bool
}

// String renders a loud, human-readable summary of the reconciliation
// (ADR-003 §6). Every conflict, remap, and re-stamp is listed so the operator
// can audit exactly what the import did or would do.
func (r *ImportReconcileReport) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "import reconciliation report (applied=%t):\n", r.Applied)
	fmt.Fprintf(&b, "  idempotent no-ops: %d\n", r.Idempotent)
	if len(r.Conflicts) > 0 {
		fmt.Fprintf(&b, "  REJECTED uid conflicts: %d\n", len(r.Conflicts))
		for _, c := range r.Conflicts {
			fmt.Fprintf(&b, "    - uid=%s import=%s local=%s (%s)\n",
				c.UID, c.ImportPath, c.LocalPath, c.Kind)
		}
	}
	if len(r.Remaps) > 0 {
		fmt.Fprintf(&b, "  renumbered provisional nodes: %d\n", len(r.Remaps))
		for _, m := range r.Remaps {
			fmt.Fprintf(&b, "    - uid=%s %s -> %s\n", m.UID, m.OldPath, m.NewPath)
		}
	}
	if len(r.Renamed) > 0 {
		fmt.Fprintf(&b, "  force-renamed (re-stamped) nodes: %d\n", len(r.Renamed))
		for _, m := range r.Renamed {
			fmt.Fprintf(&b, "    - %s re-stamped (was uid=%s)\n", m.NewPath, m.UID)
		}
	}
	return b.String()
}

// ImportReconcile validates an import at the boundary and applies it with
// offline/export-import reconciliation (ADR-003 §6, audit F-3). It is the
// reconciling counterpart to Store.Import:
//
//  1. Import-boundary uid validation (audit F-3 — uid uniqueness is guaranteed
//     by construction only over the hub event-log path, NOT at import). Before
//     anything is applied, every incoming uid is validated against the local
//     store and against the rest of the export: an identical uid+display_path is
//     an idempotent no-op; a uid that duplicates a DIFFERENT local node is
//     rejected (or, with ForceRename, the import node is re-stamped with a fresh
//     local uid); two export nodes sharing one uid are detected and rejected,
//     never silently linked.
//  2. Provisional reconciliation: incoming provisional (uid-bearing)
//     display_paths are renumbered deterministically to clean local numbers and
//     recorded in a uid-keyed remap, so references resolve via uid after import.
//  3. Live-store safety: if the rewrite would mutate a non-empty store and
//     Confirm is false, the remap report is returned with
//     ErrImportConfirmationRequired and the store is left untouched.
//
// The returned report is always non-nil when data is non-nil, even on error, so
// callers can surface the loud report. The ImportResult is nil unless the import
// was applied.
func (s *Store) ImportReconcile(
	ctx context.Context,
	data *ExportData,
	opts ImportReconcileOptions,
) (*ImportReconcileReport, *ImportResult, error) {
	if data == nil {
		return nil, nil, fmt.Errorf("import data is nil: %w", model.ErrInvalidInput)
	}

	report := &ImportReconcileReport{}

	// Step 1: detect duplicate uids within the export itself (crafted/buggy
	// export). These can never be safely applied, so fail before touching the
	// store (ADR-003 §6, F-3).
	report.Conflicts = append(report.Conflicts, exportDuplicateUIDConflicts(data)...)

	// Step 2: validate each incoming uid against the local store, classifying
	// idempotent no-ops, local collisions, and (when ForceRename) re-stamps.
	if err := s.classifyAgainstLocal(ctx, data, opts, report); err != nil {
		return report, nil, err
	}
	if len(report.Conflicts) > 0 {
		return report, nil, fmt.Errorf(
			"import rejected: %d uid conflict(s): %w", len(report.Conflicts), model.ErrConflict)
	}

	// Step 3: plan deterministic renumbers for incoming provisional nodes so
	// they settle into clean local numbers (ADR-003 §6). The plan rewrites the
	// in-memory export; nothing is written to the store yet.
	if err := s.planProvisionalRemaps(ctx, data, report); err != nil {
		return report, nil, err
	}

	// Step 4: live-store safety. Renumbering touches an existing store's
	// namespace, so require confirmation unless the store is empty (ADR-003 §6).
	if len(report.Remaps) > 0 && !opts.Confirm {
		empty, err := s.storeIsEmpty(ctx)
		if err != nil {
			return report, nil, err
		}
		if !empty {
			return report, nil, fmt.Errorf(
				"%d provisional node(s) would be renumbered in a live store; "+
					"re-run with confirmation: %w",
				len(report.Remaps), ErrImportConfirmationRequired)
		}
	}

	// Step 5: the reconcile rewrote node ids/uids/seqs in place, so the
	// original checksum no longer matches the (now-clean) content. Recompute it
	// over the rewritten content before the integrity-checked apply: the import
	// attests to what is actually being written (ADR-003 §6).
	if len(report.Remaps) > 0 || len(report.Renamed) > 0 {
		if err := RecomputeExportChecksum(data); err != nil {
			return report, nil, fmt.Errorf("recompute checksum after reconcile: %w", err)
		}
	}

	// Step 6: apply the (validated, rewritten) import via the existing path.
	result, err := s.Import(ctx, data, opts.Mode, opts.Force)
	if err != nil {
		return report, nil, err
	}
	report.Applied = true
	return report, result, nil
}

// exportDuplicateUIDConflicts returns a conflict for every uid that appears on
// more than one node within the export (ADR-003 §6, F-3). Empty uids (pre-v3
// exports) are ignored: they are not a shared identity. The conflict is reported
// once per offending uid, pointing at the first two nodes that share it.
func exportDuplicateUIDConflicts(data *ExportData) []ImportUIDConflict {
	firstPathByUID := make(map[string]string, len(data.Nodes))
	reported := make(map[string]bool)
	var conflicts []ImportUIDConflict
	for i := range data.Nodes {
		uid := data.Nodes[i].UID
		if uid == "" {
			continue
		}
		first, seen := firstPathByUID[uid]
		if !seen {
			firstPathByUID[uid] = data.Nodes[i].ID
			continue
		}
		if reported[uid] {
			continue
		}
		reported[uid] = true
		conflicts = append(conflicts, ImportUIDConflict{
			UID:        uid,
			ImportPath: data.Nodes[i].ID,
			LocalPath:  first,
			Kind:       ConflictExportDuplicateUID,
		})
	}
	return conflicts
}

// classifyAgainstLocal validates each incoming uid against the local store
// (ADR-003 §6, F-3). For each node carrying a uid that already exists locally:
// an identical display_path is an idempotent no-op (counted, left untouched); a
// different display_path is a collision — rejected, or, with ForceRename,
// re-stamped on the import node with a freshly minted local uid. Empty uids are
// skipped (no shared identity to validate).
func (s *Store) classifyAgainstLocal(
	ctx context.Context,
	data *ExportData,
	opts ImportReconcileOptions,
	report *ImportReconcileReport,
) error {
	for i := range data.Nodes {
		n := &data.Nodes[i]
		if n.UID == "" {
			continue
		}
		localPath, err := s.ResolveDisplayPathByUID(ctx, n.UID)
		if errors.Is(err, model.ErrNotFound) {
			continue // uid is new to this store — nothing to reconcile.
		}
		if err != nil {
			return fmt.Errorf("validate import uid %s: %w", n.UID, err)
		}
		if localPath == n.ID {
			report.Idempotent++ // identical uid + display_path — a no-op.
			continue
		}
		if !opts.ForceRename {
			report.Conflicts = append(report.Conflicts, ImportUIDConflict{
				UID:        n.UID,
				ImportPath: n.ID,
				LocalPath:  localPath,
				Kind:       ConflictLocalUIDMismatch,
			})
			continue
		}
		// Force-rename: re-stamp the IMPORT node with a fresh local uid so it
		// stops colliding with the local node (ADR-003 §6).
		freshUID, mintErr := clock.NewEventID()
		if mintErr != nil {
			return fmt.Errorf("mint replacement uid for %s: %w", n.ID, mintErr)
		}
		report.Renamed = append(report.Renamed, ImportRemapEntry{
			UID: n.UID, OldPath: n.ID, NewPath: n.ID,
		})
		n.UID = freshUID
	}
	return nil
}

// planProvisionalRemaps renumbers every incoming provisional (uid-bearing)
// node to a clean local number under its parent and rewrites the in-memory
// export accordingly (ADR-003 §6). The new number is the lowest free sibling
// sequence (deterministic), so the same export reconciles to the same local
// numbers. Each move is recorded in the uid-keyed remap. Nodes are processed
// shallowest-first so a parent's clean path is in place before its children are
// rebased onto it.
func (s *Store) planProvisionalRemaps(
	ctx context.Context,
	data *ExportData,
	report *ImportReconcileReport,
) error {
	order := provisionalNodeOrder(data)
	// taken tracks sibling sequences already claimed under each parent during
	// this plan, so two incoming provisionals under one parent get distinct
	// numbers even before either is written.
	taken := make(map[string]map[int]bool)
	// rebase maps an old (provisional) display_path prefix to its new clean
	// path so descendants of a renumbered node follow their ancestor.
	rebase := make(map[string]string)

	for _, idx := range order {
		n := &data.Nodes[idx]
		oldID := n.ID

		// Rebase this node's id and parent onto any already-renumbered ancestor.
		newParent := applyRebase(rebase, n.ParentID)
		n.ParentID = newParent
		if newParent == "" {
			// Provisional roots are impossible in ADR-003 (the root is always
			// settled), so a provisional node always has a parent; guard anyway.
			return fmt.Errorf(
				"provisional node %s has no parent: %w", oldID, model.ErrInvalidInput)
		}

		seq, err := s.nextFreeChildSeq(ctx, newParent, taken)
		if err != nil {
			return err
		}
		newID := model.BuildID(n.Project, newParent, seq)

		n.ID = newID
		n.Seq = seq
		rebase[oldID] = newID
		report.Remaps = append(report.Remaps, ImportRemapEntry{
			UID: n.UID, OldPath: oldID, NewPath: newID,
		})
	}
	return nil
}

// provisionalNodeOrder returns the indices of provisional incoming nodes,
// shallowest-first (then by id for determinism), so a renumbered parent is
// processed before its children (ADR-003 §6).
func provisionalNodeOrder(data *ExportData) []int {
	var order []int
	for i := range data.Nodes {
		if model.IsProvisional(data.Nodes[i].ID) {
			order = append(order, i)
		}
	}
	sort.SliceStable(order, func(a, b int) bool {
		na, nb := data.Nodes[order[a]], data.Nodes[order[b]]
		if na.Depth != nb.Depth {
			return na.Depth < nb.Depth
		}
		return na.ID < nb.ID
	})
	return order
}

// applyRebase rewrites a display_path that sits at or under any renumbered
// ancestor recorded in rebase (ADR-003 §6). It checks the exact path first, then
// the longest matching ancestor prefix, so deep descendants follow correctly.
func applyRebase(rebase map[string]string, path string) string {
	if path == "" {
		return ""
	}
	if mapped, ok := rebase[path]; ok {
		return mapped
	}
	best, bestNew := "", ""
	for old, newPath := range rebase {
		prefix := old + "."
		if strings.HasPrefix(path, prefix) && len(old) > len(best) {
			best, bestNew = old, newPath
		}
	}
	if best == "" {
		return path
	}
	return bestNew + path[len(best):]
}

// nextFreeChildSeq returns the lowest sibling sequence (1-based) under parentID
// that is free both in the local store and in the in-flight plan, recording it
// as taken (ADR-003 §6 — deterministic clean renumber). The store lookup matches
// live and soft-deleted siblings so a freed number is not reused.
func (s *Store) nextFreeChildSeq(
	ctx context.Context,
	parentID string,
	taken map[string]map[int]bool,
) (int, error) {
	if taken[parentID] == nil {
		taken[parentID] = make(map[int]bool)
	}
	for seq := 1; ; seq++ {
		if taken[parentID][seq] {
			continue
		}
		candidate := model.BuildID("", parentID, seq)
		exists, err := s.nodeIDExists(ctx, candidate)
		if err != nil {
			return 0, err
		}
		if !exists {
			taken[parentID][seq] = true
			return seq, nil
		}
	}
}

// nodeIDExists reports whether any node row (live or soft-deleted) holds the
// given display_path. Soft-deleted rows count because they still own the id.
func (s *Store) nodeIDExists(ctx context.Context, id string) (bool, error) {
	var one int
	err := s.readDB.QueryRowContext(ctx,
		`SELECT 1 FROM nodes WHERE id = ? LIMIT 1`, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check node id %s: %w", id, err)
	}
	return true, nil
}

// storeIsEmpty reports whether the nodes table holds no rows, used to decide
// whether a renumbering import needs explicit confirmation (ADR-003 §6): a fresh
// store has nothing to clobber, so confirmation is unnecessary.
func (s *Store) storeIsEmpty(ctx context.Context) (bool, error) {
	var count int
	if err := s.readDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM nodes`).Scan(&count); err != nil {
		return false, fmt.Errorf("count nodes: %w", err)
	}
	return count == 0, nil
}
