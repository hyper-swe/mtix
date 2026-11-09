// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"errors"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// Read the acknowledgment keys through the primary-key autoindex, the path the
// export join takes. The index is covering, so no table row is read here.
const ackKeysViaIndexSQL = `SELECT agent_id, event_seq
 FROM agent_inbox_ack INDEXED BY sqlite_autoindex_agent_inbox_ack_1
 ORDER BY agent_id, event_seq`

// Read the same keys from the table b-tree only, never touching the index, so
// the two reads disagree exactly when the index no longer matches the table.
const ackKeysViaTableSQL = `SELECT agent_id, event_seq
 FROM agent_inbox_ack NOT INDEXED
 ORDER BY agent_id, event_seq`

type ackKey struct {
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
		if err = rows.Scan(&k.agent, &k.seq); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		keys = append(keys, k)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate: %w", err)
	}
	return keys, nil
}

// checkAckIndexAgreesWithTable refuses an export when the acknowledgment
// autoindex does not hold exactly the keys of the table. The export join reads
// coverage through that index, so a damaged index would otherwise mark the wrong
// mail acknowledged (FR-19.1, MTIX-112.49).
func checkAckIndexAgreesWithTable(ctx context.Context, q rowQuerier) error {
	hint := "run `mtix doctor` and repair the store (restore from a good backup or recover it) before exporting"
	viaIndex, err := readAckKeys(ctx, q, ackKeysViaIndexSQL)
	if err != nil {
		return fmt.Errorf("read addressed journal: acknowledgment index unreadable: %s: %w", hint, errors.Join(model.ErrConflict, err))
	}
	viaTable, err := readAckKeys(ctx, q, ackKeysViaTableSQL)
	if err != nil {
		return fmt.Errorf("read addressed journal: acknowledgment table unreadable: %s: %w", hint, errors.Join(model.ErrConflict, err))
	}
	if len(viaIndex) != len(viaTable) {
		return fmt.Errorf("acknowledgment index holds %d keys but the table holds %d: %s: %w",
			len(viaIndex), len(viaTable), hint, model.ErrConflict)
	}
	for i := range viaTable {
		if viaIndex[i] != viaTable[i] {
			return fmt.Errorf("acknowledgment index disagrees with its table (index key %s/%d, table key %s/%d): %s: %w",
				viaIndex[i].agent, viaIndex[i].seq, viaTable[i].agent, viaTable[i].seq, hint, model.ErrConflict)
		}
	}
	return nil
}
