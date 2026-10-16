// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/hyper-swe/mtix/internal/model"
)

// maxRefusedImportIDs caps how many refused ids an import error names.
const maxRefusedImportIDs = 5

// refuseInvalidImportIDs refuses an import file that names a task id outside
// the node id grammar (model.ValidateSyncNodeID, MTIX-187), as a node, a
// parent or a dependency endpoint, unless the store already holds a task
// with that id. mtix never creates such an id, but a sync from before
// MTIX-187 could bring one in; a store that holds it can still import a
// file that holds it, such as its own export, while a store that does not
// hold it never takes it in. It reads through the import's transaction tx,
// before the import writes anything.
func refuseInvalidImportIDs(ctx context.Context, tx *sql.Tx, data *ExportData) error {
	var refused []string
	for _, id := range invalidExportIDs(data) {
		var n int
		// One primary-key probe: does this store hold the task?
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM nodes WHERE id = ?`, id).Scan(&n); err != nil {
			return fmt.Errorf("check imported id %q: %w", id, err)
		}
		if n == 0 {
			refused = append(refused, id)
		}
	}
	if len(refused) == 0 {
		return nil
	}
	named := make([]string, 0, maxRefusedImportIDs)
	for i, id := range refused {
		if i == maxRefusedImportIDs {
			break
		}
		named = append(named, fmt.Sprintf("%q", id))
	}
	return fmt.Errorf(
		"nothing was imported: invalid node id: the file holds %d task ids outside the node id grammar that this store does not hold (%s): %w",
		len(refused), strings.Join(named, ", "), model.ErrInvalidInput)
}

// invalidExportIDs returns, sorted and without repeats, every node id, parent
// id and dependency endpoint in data that is outside the node id grammar.
func invalidExportIDs(data *ExportData) []string {
	seen := map[string]bool{}
	check := func(id string) {
		if !seen[id] && model.ValidateSyncNodeID(id) != nil {
			seen[id] = true
		}
	}
	for i := range data.Nodes {
		check(data.Nodes[i].ID)
		if data.Nodes[i].ParentID != "" {
			check(data.Nodes[i].ParentID)
		}
	}
	for i := range data.Dependencies {
		check(data.Dependencies[i].FromID)
		check(data.Dependencies[i].ToID)
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
