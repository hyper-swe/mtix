// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"fmt"
)

// missingLocalEventIDsSQL expands the bound hex-encoded JSON array (hexIDList)
// and keeps, in page order, the ids found in none of the local tables (all
// keyed by event_id).
const missingLocalEventIDsSQL = `
	SELECT h.id FROM (SELECT key, CAST(unhex(value) AS TEXT) AS id FROM json_each(?)) AS h
	WHERE NOT EXISTS (SELECT 1 FROM sync_events s WHERE s.event_id = h.id)
	  AND NOT EXISTS (SELECT 1 FROM applied_events a WHERE a.event_id = h.id)
	  AND NOT EXISTS (SELECT 1 FROM sync_quarantine q WHERE q.event_id = h.id)
	ORDER BY h.key`

// MissingLocalEventIDs returns, in the given order, the ids this store holds
// in neither sync_events (its own events and every mirrored one) nor
// applied_events, and has not quarantined (sync_quarantine, MTIX-95.11): a
// quarantined event is already stored locally and the quarantine retries it on
// every pull, so the sweep never fetches it again. Ids are matched byte for
// byte, whatever bytes they hold (MTIX-187.7.15).
func (s *Store) MissingLocalEventIDs(ctx context.Context, ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	page, err := hexIDList(ids)
	if err != nil {
		return nil, fmt.Errorf("encode hub id page: %w", err)
	}
	rows, err := s.Query(ctx, missingLocalEventIDsSQL, page)
	if err != nil {
		return nil, fmt.Errorf("diff hub ids against local events: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var missing []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("diff hub ids against local events: %w", err)
		}
		missing = append(missing, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("diff hub ids against local events: %w", err)
	}
	return missing, nil
}
