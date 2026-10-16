// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"fmt"
)

// PermissionPublicationExists includes retained ordinary events and imported
// annotations, so an expired caller receipt cannot reuse an old publication id.
func (s *Store) PermissionPublicationExists(ctx context.Context, id string) (bool, error) {
	var exists bool
	err := s.readDB.QueryRowContext(ctx, `SELECT
		EXISTS(SELECT 1 FROM sync_events WHERE event_id = ?) OR
		EXISTS(SELECT 1 FROM nodes, json_each(COALESCE(nodes.annotations, '[]')) AS annotation
		WHERE json_extract(annotation.value, '$.id') = ?)`, id, id).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("read permission publication identity: %w", err)
	}
	return exists, nil
}
