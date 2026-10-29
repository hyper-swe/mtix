// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

// dependencyRemovalKey encodes an edge without delimiter ambiguity.
func dependencyRemovalKey(from, to, typ string) string {
	return from + "\x00" + to + "\x00" + typ
}

func dependencyRemovalText(to, typ string) string {
	return fmt.Sprintf("Removed dependency: %s %s", typ, to)
}

// recordDependencyRemoval makes a direct source mutation exportable (FR-15.2i).
func (s *Store) recordDependencyRemoval(ctx context.Context, tx *sql.Tx, from, to string, typ model.DepType) error {
	if err := s.touchHistory(ctx, tx, from); err != nil {
		return err
	}
	now := s.clock().UTC()
	if err := appendActivityEntry(ctx, tx, from, model.ActivityEntry{
		ID: fmt.Sprintf("act-%d", now.UnixNano()), Type: model.ActivityTypeSystem,
		Author: "system", Text: dependencyRemovalText(to, string(typ)), CreatedAt: now,
	}); err != nil {
		return fmt.Errorf("record dependency removal: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE nodes SET updated_at = ? WHERE id = ?", now.Format(time.RFC3339Nano), from); err != nil {
		return fmt.Errorf("update dependency source timestamp: %w", err)
	}
	return nil
}

// provenDependencyRemovals requires the real descent and local activity checks
// (fileDescends/copyIsCurrent) on every path, including base resolution, plus
// a new entry for this edge. ResolveWithBase's field verdict does not prove
// removals. A previously seen removal cannot justify omitting an edge re-added
// locally.
func provenDependencyRemovals(local *ExportData, idx *replaceIndex, current func(*exportNode, *exportNode) bool) map[string]bool {
	proven := make(map[string]bool)
	sources := make(map[string]*exportNode, len(local.Nodes))
	for i := range local.Nodes {
		sources[local.Nodes[i].ID] = &local.Nodes[i]
	}
	for _, dep := range local.Dependencies {
		source := sources[dep.FromID]
		if source == nil {
			continue
		}
		incoming, _ := idx.match(source)
		if incoming == nil || !current(source, incoming) {
			continue
		}
		for _, entry := range entriesMissingFrom(source.Activity, incoming.Activity) {
			if entry.Type == model.ActivityTypeSystem && entry.Author == "system" && entry.Text == dependencyRemovalText(dep.ToID, dep.DepType) {
				proven[dependencyRemovalKey(dep.FromID, dep.ToID, dep.DepType)] = true
			}
		}
	}
	return proven
}
