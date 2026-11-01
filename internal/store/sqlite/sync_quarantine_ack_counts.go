// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"fmt"
)

// QuarantineCounts distinguishes retained pulled rows from unresolved rows.
type QuarantineCounts struct {
	Total, Acknowledged, Unacknowledged int
}

// CountQuarantineAcknowledgements counts current rows, never audit history.
// EXISTS avoids multiplying rows when an id has several historical receipts.
func (s *Store) CountQuarantineAcknowledgements(ctx context.Context) (QuarantineCounts, error) {
	var counts QuarantineCounts
	available, err := s.quarantineAcknowledgementsAvailable(ctx)
	if err != nil {
		return counts, err
	}
	if !available {
		counts.Total, err = s.CountQuarantined(ctx)
		counts.Unacknowledged = counts.Total
		return counts, err
	}
	err = s.QueryRow(ctx, `SELECT COUNT(*), COALESCE(SUM(CASE WHEN q.source IN ('pull', 'sweep') AND EXISTS (
		SELECT 1 FROM sync_quarantine_acknowledgements a
		WHERE a.event_id = q.event_id AND a.raw_event = q.raw_event
	) THEN 1 ELSE 0 END), 0) FROM sync_quarantine q WHERE q.source <> 'push'`).Scan(&counts.Total, &counts.Acknowledged)
	if err != nil {
		return counts, fmt.Errorf("count quarantined events: %w", err)
	}
	counts.Unacknowledged = counts.Total - counts.Acknowledged
	return counts, nil
}
