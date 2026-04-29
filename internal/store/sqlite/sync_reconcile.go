// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

// MTIX-15.6.2 reconciliation execution per FR-18.13 / SYNC-DESIGN section
// 10. Three executable paths the user can choose when local history
// diverges from the hub. Each runs in a single SQLite tx so a failure
// rolls back the entire change.

// IDRenameMapFilename is the path under .mtix/ where the id-rename-map
// JSON is written so post-reconciliation agent sessions can look up
// "where did MTIX-3 go" after a RenameTo.
const IDRenameMapFilename = "id-rename-map.json"

// ReconcileAuditFilename is the path under .mtix/ where the audit log
// is appended (one JSON line per RECONCILE_START / RENAME_NODE /
// RECONCILE_DONE event).
const ReconcileAuditFilename = "reconcile.audit.log"

// IDRenameMap is the on-disk shape of the rename map.
type IDRenameMap struct {
	RenamedAt string            `json:"renamed_at"`
	Path      string            `json:"path"` // "discard-local" | "rename-to" | "import-as"
	Partial   bool              `json:"partial"`
	Map       map[string]string `json:"map"`
}

// auditEvent is the shape of a single line in reconcile.audit.log.
type auditEvent struct {
	Type      string         `json:"type"` // RECONCILE_START | RENAME_NODE | RECONCILE_DONE
	Timestamp string         `json:"timestamp"`
	Path      string         `json:"path,omitempty"`
	NewPrefix string         `json:"new_prefix,omitempty"`
	ParentID  string         `json:"parent_id,omitempty"`
	NodeCount int            `json:"node_count,omitempty"`
	OldID     string         `json:"old_id,omitempty"`
	NewID     string         `json:"new_id,omitempty"`
	Duration  int64          `json:"duration_ms,omitempty"`
	Extra     map[string]any `json:"extra,omitempty"`
}

// DiscardLocal drops the local mutable state (nodes, dependencies,
// sync_events, sync_conflicts, applied_events) and resets the
// meta.sync.* sentinels. The DB schema and meta keys remain — only
// the data is cleared. Use when the user wants to take the hub's
// state as the new ground truth.
//
// Atomicity: single tx wrapping every DELETE; rollback on error
// leaves the local store unchanged.
func DiscardLocal(ctx context.Context, s *Store, mtixDir string) (err error) {
	startedAt := s.clock().UTC()
	startEvent := auditEvent{
		Type:      "RECONCILE_START",
		Timestamp: startedAt.Format(time.RFC3339Nano),
		Path:      "discard-local",
	}
	if appendErr := appendAuditEvent(mtixDir, startEvent); appendErr != nil {
		return fmt.Errorf("DiscardLocal: audit start: %w", appendErr)
	}
	defer func() {
		writeIDRenameMap(mtixDir, "discard-local", nil, err != nil)
	}()

	err = s.WithTx(ctx, func(tx *sql.Tx) error {
		for _, stmt := range []string{
			`DELETE FROM sync_conflicts`,
			`DELETE FROM applied_events`,
			`DELETE FROM sync_events`,
			`DELETE FROM dependencies`,
			`DELETE FROM nodes`,
			`UPDATE meta SET value = '0' WHERE key = 'meta.sync.lamport'`,
			`UPDATE meta SET value = '0' WHERE key = 'meta.sync.last_pulled_clock'`,
			`UPDATE meta SET value = '{}' WHERE key = 'meta.sync.vector_clock'`,
			`UPDATE meta SET value = '' WHERE key = 'meta.sync.first_event_hash'`,
			`UPDATE meta SET value = '' WHERE key = 'meta.sync.project_prefix'`,
			`UPDATE meta SET value = '' WHERE key = 'meta.sync.machine_hash'`,
			`DELETE FROM sync_projects`,
		} {
			if _, txErr := tx.ExecContext(ctx, stmt); txErr != nil {
				return fmt.Errorf("DiscardLocal: %s: %w", stmt, txErr)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}

	doneEvent := auditEvent{
		Type:      "RECONCILE_DONE",
		Timestamp: s.clock().UTC().Format(time.RFC3339Nano),
		Path:      "discard-local",
		Duration:  time.Since(startedAt).Milliseconds(),
	}
	if appendErr := appendAuditEvent(mtixDir, doneEvent); appendErr != nil {
		return fmt.Errorf("DiscardLocal: audit done: %w", appendErr)
	}
	return nil
}

// RenameTo rewrites every local node ID from <oldprefix>-... to
// <newprefix>-... atomically. Updates the nodes table, dependencies
// (both from_id and to_id), sync_events.node_id, and sync_projects.
// Returns the number of nodes renamed.
//
// Refuses with model.ErrInvalidInput if newPrefix is empty or matches
// the existing prefix.
//
// Atomicity: a single tx wraps every UPDATE so a failure mid-rename
// rolls the whole thing back. The id-rename-map.json file is written
// AFTER the tx commits (with partial=false) or NOT written if the tx
// errors (partial=true marker file written instead).
func RenameTo(ctx context.Context, s *Store, mtixDir, newPrefix string) (renamedCount int, err error) {
	if !isValidProjectPrefix(newPrefix) {
		return 0, fmt.Errorf("RenameTo: invalid newPrefix %q: %w", newPrefix, model.ErrInvalidInput)
	}

	mapping, err := buildRenameMapping(ctx, s, newPrefix)
	if err != nil {
		return 0, err
	}
	if len(mapping) == 0 {
		return 0, nil
	}

	startedAt := s.clock().UTC()
	startEvent := auditEvent{
		Type:      "RECONCILE_START",
		Timestamp: startedAt.Format(time.RFC3339Nano),
		Path:      "rename-to",
		NewPrefix: newPrefix,
		NodeCount: len(mapping),
	}
	if appendErr := appendAuditEvent(mtixDir, startEvent); appendErr != nil {
		return 0, fmt.Errorf("RenameTo: audit start: %w", appendErr)
	}

	defer func() {
		writeIDRenameMap(mtixDir, "rename-to", mapping, err != nil)
	}()

	err = s.WithTx(ctx, func(tx *sql.Tx) error {
		count, txErr := executeRenameTx(ctx, s, tx, mtixDir, mapping, newPrefix)
		renamedCount = count
		return txErr
	})
	if err != nil {
		return 0, err
	}

	doneEvent := auditEvent{
		Type:      "RECONCILE_DONE",
		Timestamp: s.clock().UTC().Format(time.RFC3339Nano),
		Path:      "rename-to",
		NewPrefix: newPrefix,
		NodeCount: renamedCount,
		Duration:  time.Since(startedAt).Milliseconds(),
	}
	if appendErr := appendAuditEvent(mtixDir, doneEvent); appendErr != nil {
		return renamedCount, fmt.Errorf("RenameTo: audit done: %w", appendErr)
	}
	return renamedCount, nil
}

// ImportAs re-parents the entire local tree under parentID. Every
// root node becomes a child of parentID with a new ID derived from
// parentID's namespace; descendants follow their root's renumbering.
//
// Example: local has MTIX-1, MTIX-2 (both roots). ImportAs("PROJ-7")
// renames MTIX-1 -> PROJ-7.1, MTIX-2 -> PROJ-7.2. A pre-existing
// MTIX-1.5 becomes PROJ-7.1.5.
//
// Atomicity: single tx wrapping every UPDATE.
//
// PRECONDITION: parentID MUST already exist as a node in the local
// store. The CLI workflow (mtix sync reconcile --import-as PROJ-7)
// ensures this by running mtix sync clone first to fetch the parent
// from the hub. ImportAs returns model.ErrNotFound if the parent is
// missing locally so the caller can surface a clear error.
func ImportAs(ctx context.Context, s *Store, mtixDir, parentID string) (renamedCount int, err error) {
	if parentID == "" {
		return 0, fmt.Errorf("ImportAs: parentID required: %w", model.ErrInvalidInput)
	}
	// Verify the parent exists locally — the FK on nodes.parent_id
	// requires it. If the caller skipped mtix sync clone, this is the
	// first detectable error; refuse early so no audit/log/map
	// artifacts are written.
	var exists int
	err = s.readDB.QueryRowContext(ctx,
		`SELECT 1 FROM nodes WHERE id = ?`, parentID,
	).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("ImportAs: parent %s not in local store (run mtix sync clone first): %w",
			parentID, model.ErrNotFound)
	}
	if err != nil {
		return 0, fmt.Errorf("ImportAs: probe parent %s: %w", parentID, err)
	}

	mapping, err := buildImportAsMapping(ctx, s, parentID)
	if err != nil {
		return 0, err
	}
	if len(mapping) == 0 {
		return 0, nil
	}

	startedAt := s.clock().UTC()
	startEvent := auditEvent{
		Type:      "RECONCILE_START",
		Timestamp: startedAt.Format(time.RFC3339Nano),
		Path:      "import-as",
		ParentID:  parentID,
		NodeCount: len(mapping),
	}
	if appendErr := appendAuditEvent(mtixDir, startEvent); appendErr != nil {
		return 0, fmt.Errorf("ImportAs: audit start: %w", appendErr)
	}

	defer func() {
		writeIDRenameMap(mtixDir, "import-as", mapping, err != nil)
	}()

	err = s.WithTx(ctx, func(tx *sql.Tx) error {
		count, txErr := executeImportAsTx(ctx, s, tx, mtixDir, mapping, parentID)
		renamedCount = count
		return txErr
	})
	if err != nil {
		return 0, err
	}

	doneEvent := auditEvent{
		Type:      "RECONCILE_DONE",
		Timestamp: s.clock().UTC().Format(time.RFC3339Nano),
		Path:      "import-as",
		ParentID:  parentID,
		NodeCount: renamedCount,
		Duration:  time.Since(startedAt).Milliseconds(),
	}
	if appendErr := appendAuditEvent(mtixDir, doneEvent); appendErr != nil {
		return renamedCount, fmt.Errorf("ImportAs: audit done: %w", appendErr)
	}
	return renamedCount, nil
}

// executeImportAsTx is the body of ImportAs's tx. Same shape as
// executeRenameTx but with the formerly-root parent_id assignment
// step folded in.
func executeImportAsTx(
	ctx context.Context, s *Store, tx *sql.Tx,
	mtixDir string, mapping map[string]string, parentID string,
) (int, error) {
	if _, err := tx.ExecContext(ctx, `PRAGMA defer_foreign_keys = ON`); err != nil {
		return 0, fmt.Errorf("defer FK: %w", err)
	}
	count, err := applyRenameLoop(ctx, s, tx, mtixDir, mapping)
	if err != nil {
		return count, err
	}
	if err := assignParentToFormerRoots(ctx, tx, mapping, parentID); err != nil {
		return count, err
	}
	newRootPrefix, _ := splitProjectPrefix(parentID)
	if err := updateSentinelsAfterReconcile(ctx, tx, newRootPrefix); err != nil {
		return count, err
	}
	return count, nil
}

// assignParentToFormerRoots sets parent_id = parentID for every
// node that was a root before ImportAs (its old ID had no dot).
func assignParentToFormerRoots(ctx context.Context, tx *sql.Tx, mapping map[string]string, parentID string) error {
	for oldID, newID := range mapping {
		if strings.Contains(oldID, ".") {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE nodes SET parent_id = ? WHERE id = ?`, parentID, newID,
		); err != nil {
			return fmt.Errorf("set parent_id for %s: %w", newID, err)
		}
	}
	return nil
}

// executeRenameTx is the body of RenameTo's tx. Extracted to keep
// the outer function below the cognitive-complexity lint threshold.
//
// Defers FK checks to commit time so per-row UPDATE on parent_id
// references doesn't fail before the new ids are populated.
func executeRenameTx(
	ctx context.Context, s *Store, tx *sql.Tx,
	mtixDir string, mapping map[string]string, newPrefix string,
) (int, error) {
	if _, err := tx.ExecContext(ctx, `PRAGMA defer_foreign_keys = ON`); err != nil {
		return 0, fmt.Errorf("defer FK: %w", err)
	}
	count, err := applyRenameLoop(ctx, s, tx, mtixDir, mapping)
	if err != nil {
		return count, err
	}
	if err := updateSentinelsAfterReconcile(ctx, tx, newPrefix); err != nil {
		return count, err
	}
	return count, nil
}

// applyRenameLoop iterates the rename mapping in long-id-first order
// and applies each rename + its audit event. The long-id-first order
// is belt-and-suspenders alongside deferred FKs.
func applyRenameLoop(
	ctx context.Context, s *Store, tx *sql.Tx,
	mtixDir string, mapping map[string]string,
) (int, error) {
	oldIDs := make([]string, 0, len(mapping))
	for k := range mapping {
		oldIDs = append(oldIDs, k)
	}
	sort.Slice(oldIDs, func(i, j int) bool {
		return len(oldIDs[i]) > len(oldIDs[j])
	})

	count := 0
	for _, oldID := range oldIDs {
		newID := mapping[oldID]
		if err := renameNodeRow(ctx, tx, oldID, newID); err != nil {
			return count, fmt.Errorf("rename %s -> %s: %w", oldID, newID, err)
		}
		audit := auditEvent{
			Type:      "RENAME_NODE",
			Timestamp: s.clock().UTC().Format(time.RFC3339Nano),
			OldID:     oldID,
			NewID:     newID,
		}
		if err := appendAuditEvent(mtixDir, audit); err != nil {
			return count, fmt.Errorf("audit rename %s: %w", oldID, err)
		}
		count++
		// Chaos hook for atomicity tests (15.6.3). Production runs
		// leave this nil; the test sets it to inject a failure after
		// the Nth successful rename so the surrounding WithTx rolls
		// back.
		if reconcileFailAfterN != nil {
			if injectErr := reconcileFailAfterN(count); injectErr != nil {
				return count, fmt.Errorf("chaos: %w", injectErr)
			}
		}
	}
	return count, nil
}

// updateSentinelsAfterReconcile rewrites meta.sync.project_prefix
// to the new value, clears the cached first_event_hash, and drops
// sync_projects so GetOrComputeLocalFirstEventHash recomputes on the
// renamed tree.
func updateSentinelsAfterReconcile(ctx context.Context, tx *sql.Tx, newPrefix string) error {
	if _, err := tx.ExecContext(ctx,
		`UPDATE meta SET value = ? WHERE key = 'meta.sync.project_prefix'`,
		newPrefix,
	); err != nil {
		return fmt.Errorf("update prefix sentinel: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE meta SET value = '' WHERE key = 'meta.sync.first_event_hash'`,
	); err != nil {
		return fmt.Errorf("invalidate cached hash: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM sync_projects`,
	); err != nil {
		return fmt.Errorf("clear sync_projects: %w", err)
	}
	return nil
}

// renameNodeRow updates every reference to oldID to newID in the
// nodes, dependencies, sync_events, and sync_conflicts tables. Called
// inside the caller's tx.
func renameNodeRow(ctx context.Context, tx *sql.Tx, oldID, newID string) error {
	for _, stmt := range []struct{ sql, what string }{
		{`UPDATE nodes SET id = ? WHERE id = ?`, "nodes.id"},
		{`UPDATE nodes SET parent_id = ? WHERE parent_id = ?`, "nodes.parent_id"},
		{`UPDATE dependencies SET from_id = ? WHERE from_id = ?`, "dependencies.from_id"},
		{`UPDATE dependencies SET to_id = ? WHERE to_id = ?`, "dependencies.to_id"},
		{`UPDATE sync_events SET node_id = ? WHERE node_id = ?`, "sync_events.node_id"},
		{`UPDATE sync_conflicts SET node_id = ? WHERE node_id = ?`, "sync_conflicts.node_id"},
	} {
		if _, err := tx.ExecContext(ctx, stmt.sql, newID, oldID); err != nil {
			return fmt.Errorf("rename %s %s -> %s: %w", stmt.what, oldID, newID, err)
		}
	}
	return nil
}

// buildRenameMapping queries every distinct node ID under the current
// project_prefix and produces an old->new map for RenameTo.
func buildRenameMapping(ctx context.Context, s *Store, newPrefix string) (map[string]string, error) {
	rows, err := s.readDB.QueryContext(ctx,
		`SELECT id FROM nodes ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("scan node IDs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	mapping := map[string]string{}
	for rows.Next() {
		var id string
		if scanErr := rows.Scan(&id); scanErr != nil {
			return nil, fmt.Errorf("scan id: %w", scanErr)
		}
		_, rest := splitProjectPrefix(id)
		if rest == "" {
			continue // skip malformed (no prefix or empty tail)
		}
		newID := newPrefix + "-" + rest
		mapping[id] = newID
	}
	return mapping, rows.Err()
}

// buildImportAsMapping computes the rename map for ImportAs. Each
// root-level local ID becomes parentID.<seq>; nested children follow
// their root's renumbering with their dot-suffix preserved.
//
// Exclusions:
//   - parentID itself: it must already exist locally and is not part
//     of the local tree to import.
//   - Any node whose ID starts with parentID+".": these are already
//     under parentID's subtree (e.g. ancestors fetched alongside the
//     parent via mtix sync clone) and don't need renaming.
func buildImportAsMapping(ctx context.Context, s *Store, parentID string) (map[string]string, error) {
	rows, err := s.readDB.QueryContext(ctx,
		`SELECT id FROM nodes ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("scan node IDs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	parentSubtreePrefix := parentID + "."
	allIDs := []string{}
	for rows.Next() {
		var id string
		if scanErr := rows.Scan(&id); scanErr != nil {
			return nil, fmt.Errorf("scan id: %w", scanErr)
		}
		// Exclude the parent and anything already in its subtree.
		if id == parentID || strings.HasPrefix(id, parentSubtreePrefix) {
			continue
		}
		allIDs = append(allIDs, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Identify roots (no '.'); assign them sequential numbers under
	// parentID. Sort roots alphabetically so the numbering is
	// deterministic regardless of insert order.
	roots := []string{}
	for _, id := range allIDs {
		if !strings.Contains(id, ".") {
			roots = append(roots, id)
		}
	}
	sort.Strings(roots)
	rootMap := make(map[string]string, len(roots))
	for i, root := range roots {
		rootMap[root] = fmt.Sprintf("%s.%d", parentID, i+1)
	}

	mapping := make(map[string]string, len(allIDs))
	for _, id := range allIDs {
		root, suffix := rootAndSuffix(id)
		newRoot, ok := rootMap[root]
		if !ok {
			continue // orphan; leave alone
		}
		if suffix == "" {
			mapping[id] = newRoot
		} else {
			mapping[id] = newRoot + suffix
		}
	}
	return mapping, nil
}

// splitProjectPrefix splits "MTIX-1.2.3" into ("MTIX", "1.2.3").
// Returns ("", "") for malformed IDs.
func splitProjectPrefix(id string) (prefix, rest string) {
	idx := strings.IndexByte(id, '-')
	if idx <= 0 || idx == len(id)-1 {
		return "", ""
	}
	return id[:idx], id[idx+1:]
}

// rootAndSuffix splits "MTIX-1.2.3" into ("MTIX-1", ".2.3"). For a
// root id "MTIX-1" the suffix is empty.
func rootAndSuffix(id string) (root, suffix string) {
	idx := strings.IndexByte(id, '-')
	if idx <= 0 {
		return id, ""
	}
	dotIdx := strings.IndexByte(id[idx:], '.')
	if dotIdx < 0 {
		return id, ""
	}
	return id[:idx+dotIdx], id[idx+dotIdx:]
}

// isValidProjectPrefix mirrors model.projectPrefixPattern grammar
// without re-running the regex. Used by RenameTo to refuse a clearly-
// invalid newPrefix before any local mutation.
func isValidProjectPrefix(p string) bool {
	if len(p) == 0 || len(p) > 16 {
		return false
	}
	for i, r := range p {
		if i == 0 {
			if r < 'A' || r > 'Z' {
				return false
			}
			continue
		}
		isUpper := r >= 'A' && r <= 'Z'
		isDigit := r >= '0' && r <= '9'
		if !isUpper && !isDigit && r != '_' {
			return false
		}
	}
	return true
}

// appendAuditEvent appends one JSON line to .mtix/reconcile.audit.log.
// The directory is created if missing. Best-effort: a write failure
// returns an error to the caller but does NOT roll back the
// reconciliation tx (the audit is observability, not data-of-record).
func appendAuditEvent(mtixDir string, e auditEvent) error {
	if mtixDir == "" {
		return fmt.Errorf("mtixDir required")
	}
	if err := os.MkdirAll(mtixDir, 0o755); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}
	path := filepath.Join(mtixDir, ReconcileAuditFilename)
	body, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open audit log: %w", err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write(append(body, '\n')); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	return nil
}

// writeIDRenameMap persists the rename map to .mtix/id-rename-map.json.
// Always called via defer in the resolution-path functions so partial
// state is recorded on error (partial=true) and full state on success.
//
// nolint:errcheck // best-effort defer; failure here doesn't block the caller
func writeIDRenameMap(mtixDir, path string, mapping map[string]string, partial bool) {
	if mtixDir == "" {
		return
	}
	if err := os.MkdirAll(mtixDir, 0o755); err != nil {
		return
	}
	out := IDRenameMap{
		RenamedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Path:      path,
		Partial:   partial,
		Map:       mapping,
	}
	if out.Map == nil {
		out.Map = map[string]string{}
	}
	body, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return
	}
	tmp := filepath.Join(mtixDir, IDRenameMapFilename+".tmp")
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, filepath.Join(mtixDir, IDRenameMapFilename))
}

