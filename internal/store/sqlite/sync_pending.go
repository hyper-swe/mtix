// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// ReadPendingEvents returns up to limit events awaiting push, in lamport
// order. Reads via readDB — no write tx needed.
//
// This lives in the store rather than in cmd/mtix so there is exactly ONE
// pending-queue projection. It used to be duplicated: cmd/mtix had the
// production copy and e2e had a hand-maintained "mirror" that had drifted
// to include a column production omitted (MTIX-91). The harness was more
// correct than the shipping code, so e2e exercised a path the CLI never
// took and the defect passed every test. Both callers now share this.
//
// uid is load-bearing on the wire, not decoration. The hub resolves a
// registered create's EFFECTIVE uid as its stored uid, falling back to its
// event_id when that is NULL (transport/registry.go). Dropping uid here
// made every hub row NULL, so the effective uid was always an event_id and
// a re-emitted create — which carries the node's real, stable uid — could
// never match it. The hub then classified the same logical node as a
// DIFFERENT one and demanded a renumber, making the ADR-003 §6/§9
// same-node no-op unreachable for every pushed create.
func (s *Store) ReadPendingEvents(ctx context.Context, limit int) ([]*model.SyncEvent, error) {
	rows, err := s.readDB.QueryContext(ctx, `
		SELECT event_id, project_prefix, node_id, uid, op_type, payload,
		       wall_clock_ts, lamport_clock, vector_clock,
		       author_id, author_machine_hash
		FROM sync_events
		WHERE sync_status = 'pending'
		ORDER BY lamport_clock ASC
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("read pending events: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]*model.SyncEvent, 0, limit)
	for rows.Next() {
		var e model.SyncEvent
		var opType, payload, vc string
		var uid sql.NullString
		if scanErr := rows.Scan(
			&e.EventID, &e.ProjectPrefix, &e.NodeID, &uid, &opType, &payload,
			&e.WallClockTS, &e.LamportClock, &vc,
			&e.AuthorID, &e.AuthorMachineHash,
		); scanErr != nil {
			return nil, fmt.Errorf("scan pending event: %w", scanErr)
		}
		// A NULL uid is legitimate for a pre-ADR-003 row that BackfillUIDs
		// has not reached; apply falls back to node_id in that case.
		e.UID = uid.String
		e.OpType = model.OpType(opType)
		e.Payload = json.RawMessage(payload)
		if err := json.Unmarshal([]byte(vc), &e.VectorClock); err != nil {
			return nil, fmt.Errorf("decode VC for %s: %w", e.EventID, err)
		}
		out = append(out, &e)
	}
	return out, rows.Err()
}
