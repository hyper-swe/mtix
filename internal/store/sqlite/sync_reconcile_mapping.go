// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

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
