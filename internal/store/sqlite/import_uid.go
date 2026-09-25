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
	// BeforeWrite, when set, runs once every check has passed (the file's
	// count, checksum and times included), just before the import writes
	// (MTIX-95.31.4): mtix import --mode merge takes the verified pre-import
	// backup there. An error from it stops the import, which writes nothing.
	BeforeWrite func() error
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
	// LocalRenumbers are local tasks, with their subtrees, that a merge
	// renumbers because the file holds a different task (another uid)
	// under their id, keyed by uid (MTIX-95.31.4). The file's task keeps
	// the id; the local one moves to the next number free in both.
	LocalRenumbers []ImportRemapEntry
	// Moved are local tasks, with their subtrees, that a merge moves to the
	// id the file holds them under (the same uid): another clone renumbered
	// them (MTIX-95.31.4). Following the published board needs no
	// confirmation.
	Moved []ImportRemapEntry
	// UIDAdoptions are the local tasks a merge gives the file's uid, the
	// same task under another uid, with both titles (MTIX-95.31.6).
	UIDAdoptions []ImportUIDAdoption
	// TitleMismatches are the local tasks of LocalRenumbers whose id the
	// file holds with another title while one of the two has no uid to
	// compare (MTIX-95.31.9).
	TitleMismatches []ImportTitleMismatch
	// Idempotent counts incoming nodes that were an exact uid+display_path
	// no-op against the local store (ADR-003 §6).
	Idempotent int
	// Applied is true once the (possibly rewritten) import has been committed.
	Applied bool
}

// String renders a loud, human-readable summary of the reconciliation
// (ADR-003 §6). Every conflict, remap, re-stamp and uid adoption
// (MTIX-95.31.6) is listed so the operator can audit exactly what the import
// did or would do.
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
	if len(r.LocalRenumbers) > 0 {
		fmt.Fprintf(&b, "  local tasks renumbered, the file holds a different task under their id: %d\n",
			len(r.LocalRenumbers))
		for _, m := range r.LocalRenumbers {
			fmt.Fprintf(&b, "    - uid=%s %s -> %s\n", m.UID, m.OldPath, m.NewPath)
		}
		if !r.Applied {
			b.WriteString("  not applied: review the renumbering above, then rerun the import with --confirm\n")
		}
	}
	writeTitleMismatches(&b, r.TitleMismatches) // MTIX-95.31.9
	if len(r.Moved) > 0 {
		fmt.Fprintf(&b, "  local tasks moved to the id the file holds them under (renumbered elsewhere): %d\n",
			len(r.Moved))
		for _, m := range r.Moved {
			fmt.Fprintf(&b, "    - uid=%s %s -> %s\n", m.UID, m.OldPath, m.NewPath)
		}
	}
	writeUIDAdoptions(&b, r.UIDAdoptions) // MTIX-95.31.6
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
// Every uid a merge adopts (the same task under another uid, FR-7.8) is
// listed in the report, applied or not, and in the result (MTIX-95.31.6).
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

	// Steps 1-2: validate every incoming uid, within the export and against
	// the local store (ADR-003 §6, F-3).
	if err := s.validateImportUIDs(ctx, data, opts, report); err != nil {
		return report, nil, err
	}

	// Step 3a (MTIX-95.31.4): a merge moves a local task the file holds
	// under another id there, and never overwrites a local task that is a
	// different task than the file's under the same id: plan to renumber
	// the local task and its subtree to the next number free in the store
	// and the file. Planned first, so the provisional renumbers avoid its
	// numbers (taken). MTIX-95.31.9: when either task has no uid to
	// compare, different titles are different tasks (report.TitleMismatches).
	taken := make(map[string]map[int]bool)
	moves, planErr := s.planLocalRenumbers(ctx, data, opts.Mode, report, taken)
	if planErr != nil {
		return report, nil, planErr
	}
	if err := rejectConflicts(report); err != nil {
		return report, nil, err
	}

	// Step 3b: plan deterministic renumbers for incoming provisional nodes
	// so they settle into clean local numbers (ADR-003 §6). The plan
	// rewrites the in-memory export; nothing is written to the store yet.
	if err := s.planProvisionalRemaps(ctx, data, report, taken); err != nil {
		return report, nil, err
	}

	// Step 3c (MTIX-95.31.6): list every uid the merge adopts, with the id,
	// both uids and both titles, before any confirmation is asked for: two
	// different tasks the identity rule treats as one are then visible.
	if err := s.planUIDAdoptions(ctx, data, opts.Mode, report); err != nil {
		return report, nil, err
	}

	// Step 4: live-store safety. Renumbering touches an existing store's
	// namespace, so require confirmation unless the store is empty (ADR-003
	// §6); renumbering a local task always does (MTIX-95.31.4).
	if err := s.requireConfirmation(ctx, report, opts.Confirm); err != nil {
		return report, nil, err
	}

	// Step 5: the reconcile rewrote node ids/uids/seqs in place, so the
	// original checksum no longer matches the (now-clean) content. Recompute it
	// over the rewritten content before the integrity-checked apply: the import
	// attests to what is actually being written (ADR-003 §6). Then run the
	// caller's step before any write, once every check passed (MTIX-95.31.4).
	writeOpts, err := s.prepareWrite(ctx, data, opts, report, moves)
	if err != nil {
		return report, nil, err
	}

	// Step 6: apply the (validated, rewritten) import via the existing
	// path, which first moves the planned local tasks and, after a dry run,
	// re-checks the store it read (MTIX-95.31.4).
	result, err := s.Import(ctx, data, opts.Mode, opts.Force, writeOpts...)
	if err != nil {
		return report, nil, err
	}
	report.Applied = true
	result.UIDAdoptions = report.UIDAdoptions // MTIX-95.31.6: --json lists them too
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
	taken map[string]map[int]bool,
) error {
	order := shallowestFirst(data, func(n *exportNode) bool { return model.IsProvisional(n.ID) })
	// taken tracks sibling sequences already claimed under each parent during
	// this plan (the local renumbers of a merge included, MTIX-95.31.4), so
	// two nodes under one parent get distinct numbers even before either is
	// written.
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

// shallowestFirst returns the indices of the incoming nodes keep accepts,
// shallowest-first (then by id for determinism), so a renumbered parent is
// processed before its children (ADR-003 §6; the provisional nodes, and
// for MTIX-95.31.4 every node).
func shallowestFirst(data *ExportData, keep func(*exportNode) bool) []int {
	var order []int
	for i := range data.Nodes {
		if keep(&data.Nodes[i]) {
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
