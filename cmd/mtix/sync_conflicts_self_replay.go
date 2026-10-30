// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// selfReplayCheckName is the name of the doctor's self-replay conflict
// rows check.
const selfReplayCheckName = "self-replay conflict rows"

// selfReplayFix is the check's fix: nothing to run. The rows are excluded
// from the unresolved count at read time (readConflicts, MTIX-216).
const selfReplayFix = "none required; inspect with mtix sync conflicts list --all"

// appendSelfReplayConflictCheck adds the doctor's informational
// "self-replay conflict rows" check (MTIX-216). It is local only, always
// passes when the store can be read, and fails like the other local checks
// when there is no local store.
func appendSelfReplayConflictCheck(ctx context.Context, r DoctorReport, st *sqlite.Store) DoctorReport {
	if st == nil {
		return appendCheck(r, selfReplayCheckName, false, "local store not initialized")
	}
	rows, err := readConflicts(ctx, st, "", true)
	if err != nil {
		return appendCheck(r, selfReplayCheckName, false, err.Error())
	}
	n := 0
	for _, row := range rows {
		if row.SelfReplay {
			n++
		}
	}
	if n == 0 {
		return appendCheck(r, selfReplayCheckName, true, "ok: no self-replay conflict rows")
	}
	return appendDoctorCheck(r, DoctorCheck{
		Name: selfReplayCheckName,
		Pass: true,
		Detail: fmt.Sprintf("%d conflict rows were written before 0.5.4, when a pull replayed this store's own "+
			"pushed events; both events were emitted here, so they are not conflicts and are not counted as "+
			"unresolved", n),
		Fix: selfReplayFix,
	})
}

// readConflictsSQL is the one query behind readConflicts (MTIX-216,
// MTIX-95.7). Each row carries its flags. A row is an own pair when its
// winner and loser event ids are both in sync_events with a sync_status
// other than applied (emitted by this store). self_replay is an lww own
// pair. An lww row that is not self-replay is unresolved until a manual row
// for the same node and field (NULL fields pair with each other) follows it
// in conflict_id order, ignoring manual rows that are own pairs (decisions
// taken on phantom rows); a later lww row for the pair is unresolved again.
// Tombstone, manual and self-replay rows are never unresolved. Parameter 1
// filters by node (” for every node); parameter 2 chooses every row or
// only the unresolved ones. A constant: no input is ever spliced into it.
const readConflictsSQL = `
	SELECT conflict_id, event_id_winner, event_id_loser, node_id,
	       field_name, resolution, resolved_at, resolved_by, unresolved, self_replay
	FROM (
	    SELECT c.conflict_id, c.event_id_winner, c.event_id_loser, c.node_id,
	           COALESCE(c.field_name, '') AS field_name, c.resolution, c.resolved_at,
	           COALESCE(c.resolved_by, '') AS resolved_by, c.self_replay,
	           (c.resolution = 'lww' AND NOT c.self_replay AND NOT EXISTS (
	               SELECT 1 FROM sync_conflicts m
	               WHERE m.resolution = 'manual'
	                 AND m.node_id = c.node_id
	                 AND m.field_name IS c.field_name
	                 AND m.conflict_id > c.conflict_id
	                 AND NOT (EXISTS (SELECT 1 FROM sync_events mw
	                                  WHERE mw.event_id = m.event_id_winner AND mw.sync_status <> 'applied')
	                      AND EXISTS (SELECT 1 FROM sync_events ml
	                                  WHERE ml.event_id = m.event_id_loser AND ml.sync_status <> 'applied'))
	           )) AS unresolved
	    FROM (
	        SELECT sc.*, (sc.resolution = 'lww'
	            AND EXISTS (SELECT 1 FROM sync_events w
	                        WHERE w.event_id = sc.event_id_winner AND w.sync_status <> 'applied')
	            AND EXISTS (SELECT 1 FROM sync_events l
	                        WHERE l.event_id = sc.event_id_loser AND l.sync_status <> 'applied')
	        ) AS self_replay
	        FROM sync_conflicts sc
	        WHERE (?1 = '' OR sc.node_id = ?1)
	    ) c
	)
	WHERE ?2 OR unresolved
	ORDER BY conflict_id`
