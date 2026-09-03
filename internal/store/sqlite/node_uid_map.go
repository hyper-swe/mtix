// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"fmt"
)

// NodeUIDsByProject returns every node's durable uid keyed by project
// prefix and then by display path: {"MTIX": {"MTIX-1": uid, ...}}.
// Soft-deleted nodes are included: their create_node row is on the hub
// like any other and needs the same repair (MTIX-92). Nodes with no uid
// are omitted; the caller cannot stamp what it does not know.
func (s *Store) NodeUIDsByProject(ctx context.Context) (map[string]map[string]string, error) {
	rows, err := s.readDB.QueryContext(ctx,
		`SELECT project, id, uid FROM nodes WHERE uid IS NOT NULL AND uid <> ''`)
	if err != nil {
		return nil, fmt.Errorf("read node uids: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := map[string]map[string]string{}
	for rows.Next() {
		var project, id, uid string
		if err := rows.Scan(&project, &id, &uid); err != nil {
			return nil, fmt.Errorf("scan node uid: %w", err)
		}
		if out[project] == nil {
			out[project] = map[string]string{}
		}
		out[project][id] = uid
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate node uids: %w", err)
	}
	return out, nil
}
