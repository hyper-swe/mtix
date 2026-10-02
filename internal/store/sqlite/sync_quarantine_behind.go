// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// Events behind a quarantined creation (MTIX-95.37).
//
// INVARIANT: a pulled event about a task whose create_node is in
// sync_quarantine is never applied before that creation, and never applied
// as a no-op. Some appliers treat "no such task" as nothing to do (a delete,
// SYNC-DESIGN §8.3); for an event behind a quarantined creation that would
// record the event as applied and lose it, and the retry of the creation
// would then bring back a task the teammate deleted. So such an event is
// itself quarantined, and the retry, which runs in Lamport order, applies it
// after the creation, or keeps it queued while the creation cannot apply.

// ErrBehindQuarantinedCreate is the refusal of an event whose task, parent or
// link target belongs to a creation that is quarantined.
var ErrBehindQuarantinedCreate = errors.New("depends on a quarantined create")

// QuarantinedCreateBehind returns the id of a quarantined pulled creation
// that e depends on, or "" when it depends on none, in the caller's
// transaction. e depends on a creation when it carries the creation's uid
// (the uid its event carries, else the event's own id); when it is a
// creation, when its parent_id; when it is a link or unlink, when its target;
// when it has no uid, when its own number, is the creation's number or lies
// below it. The creation's own event never depends on itself.
func QuarantinedCreateBehind(ctx context.Context, tx *sql.Tx, e *model.SyncEvent) (string, error) {
	numbers := dependencyNumbers(e)
	arg, err := json.Marshal(numbers)
	if err != nil {
		return "", fmt.Errorf("event %s: encode numbers: %w", e.EventID, err)
	}
	var id string
	// The first quarantined pull or sweep creation (never a held push event)
	// whose uid is the event's uid, or whose number is one the event depends on.
	err = tx.QueryRowContext(ctx, `
		SELECT q.event_id FROM sync_quarantine q
		 WHERE q.source <> 'push' AND q.event_id <> ? AND json_valid(q.raw_event)
		   AND json_extract(q.raw_event, '$.op_type') = 'create_node'
		   AND ((? <> '' AND COALESCE(NULLIF(json_extract(q.raw_event, '$.uid'), ''), q.event_id) = ?)
		        OR json_extract(q.raw_event, '$.node_id') IN (SELECT value FROM json_each(?)))
		 ORDER BY q.event_id LIMIT 1`,
		e.EventID, e.UID, e.UID, string(arg)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("event %s: check quarantined creations: %w", e.EventID, err)
	}
	return id, nil
}

// dependencyNumbers returns the numbers whose quarantined creation e depends
// on by number: the parent of a creation and each task above it, the target
// of a link or unlink and each task above it, and, for an event without a uid,
// its own number and each task above it. A creation's own number is left out.
func dependencyNumbers(e *model.SyncEvent) []string {
	var out []string
	switch e.OpType {
	case model.OpCreateNode:
		var p model.CreateNodePayload
		if json.Unmarshal(e.Payload, &p) == nil {
			out = append(out, nodeLineOf(p.ParentID)...)
		}
		return out
	case model.OpLinkDep, model.OpUnlinkDep:
		out = append(out, nodeLineOf(linkTarget(e))...)
	}
	if e.UID == "" {
		out = append(out, nodeLineOf(e.NodeID)...)
	}
	return out
}
