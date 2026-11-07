// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/hyper-swe/mtix/internal/model"
)

// PermissionAddressedAgents reports actual foreign-applied addressed comments,
// including acked comments. A local addressed send or own echo is not evidence.
func (s *Store) PermissionAddressedAgents(ctx context.Context) (out []string, err error) {
	query, predicateErr := restoredInboxQuery(ctx, s.readDB, `SELECT s.payload FROM sync_events s
	 JOIN applied_events a ON a.event_id = s.event_id
	 WHERE s.op_type = ? AND s.sync_status = ?
          AND NOT (0)`, `SELECT s.payload FROM sync_events s
	 JOIN applied_events a ON a.event_id = s.event_id
	 WHERE s.op_type = ? AND s.sync_status = ?
          AND NOT (EXISTS (SELECT 1 FROM inbox_restored_events r WHERE r.event_id=s.event_id))`)
	if predicateErr != nil {
		return nil, predicateErr
	}
	rows, err := s.readDB.QueryContext(ctx, query, model.OpComment, model.SyncStatusApplied)
	if err != nil {
		return nil, fmt.Errorf("read addressed permission evidence: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			err = fmt.Errorf("close permission evidence: %w", closeErr)
		}
	}()
	seen := map[string]bool{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("scan permission evidence: %w", err)
		}
		var payload model.CommentPayload
		if err := json.Unmarshal([]byte(raw), &payload); err != nil {
			return nil, fmt.Errorf("decode permission evidence: %w", err)
		}
		if payload.To != "" {
			seen[payload.To] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate permission evidence: %w", err)
	}
	out = make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}
