// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// stampRangeSQL counts the create events whose restore epoch lies below 0
// or above the hub's current epoch, which the hub never stamps, reads that
// epoch, and builds the table owner's UPDATE that sets each such stamp to
// the current epoch, with the schema quoted server-side by format() (SQL
// Rule 1a) and the UPDATE reading the epoch when it runs (MTIX-95.1.7).
// $1 is the create operation's op_type.
const stampRangeSQL = `
	SELECT (SELECT pg_catalog.count(*) FROM sync_events e CROSS JOIN sync_hub_state s
	        WHERE s.id AND e.op_type = $1 AND (e.restore_epoch < 0 OR e.restore_epoch > s.restore_epoch)),
	       COALESCE((SELECT s.restore_epoch FROM sync_hub_state s WHERE s.id), 0),
	       pg_catalog.format('UPDATE %1$I.sync_events SET restore_epoch = (SELECT s.restore_epoch FROM ' ||
	           '%1$I.sync_hub_state s WHERE s.id) WHERE op_type = %2$L AND (restore_epoch < 0 OR restore_epoch > ' ||
	           '(SELECT s.restore_epoch FROM %1$I.sync_hub_state s WHERE s.id));', n.nspname, $1::text)
	FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
	WHERE c.oid = pg_catalog.to_regclass('sync_events')`

// readStampRange returns how many create events carry a restore epoch
// outside 0 to the hub's current epoch, that epoch, and the table owner's
// UPDATE that sets each to the current epoch (MTIX-95.1.7). The recorder
// and the push classifier treat such a stamp as not earlier than the
// current epoch.
func readStampRange(ctx context.Context, pool *transport.Pool) (outside, epoch int64, fix string, err error) {
	// The count, the current epoch and the UPDATE (stampRangeSQL).
	err = pool.Inner().QueryRow(ctx, stampRangeSQL, string(model.OpCreateNode)).Scan(&outside, &epoch, &fix)
	if err != nil {
		return 0, 0, "", fmt.Errorf("read the hub's restore epoch stamps: %w", err)
	}
	return outside, epoch, fix, nil
}
