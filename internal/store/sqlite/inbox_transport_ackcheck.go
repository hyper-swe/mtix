// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"errors"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// Read the acknowledgment keys and row pointers through the primary-key
// autoindex, the path the export join takes. The index is covering, so the
// rowid comes from the index cell and no table row is read here.
const ackKeysViaIndexSQL = `SELECT rowid, agent_id, event_seq
 FROM agent_inbox_ack INDEXED BY sqlite_autoindex_agent_inbox_ack_1
 ORDER BY agent_id, event_seq`

// Read the same keys and rowids from the table b-tree only, never touching the
// index, so the two reads disagree exactly when the index no longer matches the
// table, including a wrong rowid pointer that would make the join read acked_at
// from another row.
const ackKeysViaTableSQL = `SELECT rowid, agent_id, event_seq
 FROM agent_inbox_ack NOT INDEXED
 ORDER BY agent_id, event_seq`

// ackRepairHint names the commands this build has for the damage. The refusal
// itself is the confirmation: doctor does not run an index/table comparison.
const ackRepairHint = "the refusal itself confirms the damage, so do not export again until the store is repaired: " +
	"restore the store from a good backup, or run `mtix recover` to salvage it into an importable export"

type ackKey struct {
	rowid int64
	agent string
	seq   int64
}

// readAckKeys loads the acknowledgment keys with one of the fixed queries above.
func readAckKeys(ctx context.Context, q rowQuerier, query string) (keys []ackKey, err error) {
	rows, err := q.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var k ackKey
		if err = rows.Scan(&k.rowid, &k.agent, &k.seq); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		keys = append(keys, k)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate: %w", err)
	}
	return keys, nil
}

// compareAckKeys refuses unless both reads hold the same rows, in order.
func compareAckKeys(viaIndex, viaTable []ackKey) error {
	if len(viaIndex) != len(viaTable) {
		return fmt.Errorf("acknowledgment index holds %d keys but the table holds %d: %s: %w",
			len(viaIndex), len(viaTable), ackRepairHint, model.ErrConflict)
	}
	for i := range viaTable {
		if viaIndex[i] != viaTable[i] {
			return fmt.Errorf("acknowledgment index disagrees with its table (index %s/%d row %d, table %s/%d row %d): %s: %w",
				viaIndex[i].agent, viaIndex[i].seq, viaIndex[i].rowid,
				viaTable[i].agent, viaTable[i].seq, viaTable[i].rowid, ackRepairHint, model.ErrConflict)
		}
	}
	return nil
}

// ackReadError classifies a failed check read. A cancelled or expired context
// is returned as the context error and is never reported as index damage.
func ackReadError(ctx context.Context, what string, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("read addressed journal: acknowledgment check interrupted: %w", ctxErr)
	}
	return fmt.Errorf("read addressed journal: acknowledgment %s unreadable: %s: %w", what, ackRepairHint, errors.Join(model.ErrConflict, err))
}

// checkAckIndexAgreesWithTable refuses an export when the acknowledgment
// autoindex does not hold exactly the keys and row pointers of the table. The
// export join reads coverage through that index, so a damaged index would
// otherwise mark the wrong mail acknowledged (FR-19.1, MTIX-112.49).
func checkAckIndexAgreesWithTable(ctx context.Context, q rowQuerier) error {
	viaIndex, err := readAckKeys(ctx, q, ackKeysViaIndexSQL)
	if err != nil {
		return ackReadError(ctx, "index", err)
	}
	viaTable, err := readAckKeys(ctx, q, ackKeysViaTableSQL)
	if err != nil {
		return ackReadError(ctx, "table", err)
	}
	return compareAckKeys(viaIndex, viaTable)
}
