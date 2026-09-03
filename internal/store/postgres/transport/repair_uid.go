// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"context"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"

	"github.com/hyper-swe/mtix/internal/sync/redact"
)

// MTIX-92: one-time repair of hub create rows pushed by a client that
// did not send uid (every release before the MTIX-91 fix).
//
// The hub resolves a registered create's effective uid as its stored uid,
// or its event_id when the stored uid is NULL (lookupRegisteredCreate).
// Rows pushed without a uid therefore register under their event_id, and
// a fixed client re-emitting a create with the node's real uid can never
// match them: the hub sees a different logical node claiming a taken
// number and demands a renumber the client cannot settle. Stamping each
// such row with its node's uid, matched on (project_prefix, node_id),
// makes the next re-push the same-logical-node no-op ADR-003 §6 intends.
//
// Only the client knows the mapping — the hub never received the uid —
// so the repair runs from a CLI holding the local store and passes the
// map in. It never deletes a row and never overwrites a populated uid:
// the UPDATE is guarded by uid IS NULL, which also makes it idempotent.

// UIDRepairReport is the outcome of RepairCreateUIDs for one project.
type UIDRepairReport struct {
	Project string `json:"project"`
	DryRun  bool   `json:"dry_run"`
	// Stamped is the number of hub create rows updated (or, in a dry run,
	// that would be).
	Stamped int `json:"stamped"`
	// AlreadyStamped counts create rows whose stored uid already equals
	// the local node's uid — nothing to do.
	AlreadyStamped int `json:"already_stamped"`
	// Mismatched lists nodes whose hub create carries a DIFFERENT non-NULL
	// uid than the local node. Never overwritten; reported so an operator
	// can look, because it means the hub and this store disagree about
	// which logical node holds the number.
	Mismatched []UIDMismatch `json:"mismatched,omitempty"`
	// HubOnly lists node ids with an unstamped create on the hub and no
	// local node to take the uid from. They stay unrepaired after this
	// run, so a partial repair is visible rather than silent.
	HubOnly []string `json:"hub_only,omitempty"`
}

// UIDMismatch is one node whose hub and local uids disagree.
type UIDMismatch struct {
	NodeID   string `json:"node_id"`
	HubUID   string `json:"hub_uid"`
	LocalUID string `json:"local_uid"`
}

// RepairCreateUIDs stamps every create_node row for projectPrefix whose
// uid is NULL with the uid localUIDs holds for its node_id. With dryRun
// it reports what would change and executes no UPDATE. Rows are never
// deleted and a non-NULL uid is never overwritten. Idempotent: a second
// run stamps nothing.
//
// Runs in one transaction under the same advisory lock as the migration
// sweep, so it cannot interleave with a sweep reading effective uids.
// Parameterized SQL only; errors redact any DSN.
func (p *Pool) RepairCreateUIDs(ctx context.Context, projectPrefix string,
	localUIDs map[string]string, dryRun bool,
) (UIDRepairReport, error) {
	if p == nil || p.p == nil {
		return UIDRepairReport{}, fmt.Errorf("RepairCreateUIDs: pool not open")
	}
	if projectPrefix == "" {
		return UIDRepairReport{}, fmt.Errorf("RepairCreateUIDs: empty project prefix")
	}
	report := UIDRepairReport{Project: projectPrefix, DryRun: dryRun}

	tx, err := p.p.Begin(ctx)
	if err != nil {
		return UIDRepairReport{}, fmt.Errorf("repair uids: begin: %s", redact.DSN(err.Error()))
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after Commit; rolls back a dry run

	if _, lockErr := tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtext($1))`, AdvisoryLockKey,
	); lockErr != nil {
		return UIDRepairReport{}, fmt.Errorf("repair uids: acquire advisory lock: %s", redact.DSN(lockErr.Error()))
	}

	toStamp, err := classifyHubCreates(ctx, tx, projectPrefix, localUIDs, &report)
	if err != nil {
		return UIDRepairReport{}, err
	}

	if dryRun {
		report.Stamped = len(toStamp)
		return report, nil // deferred Rollback discards the (empty) tx
	}

	for _, nodeID := range toStamp {
		tag, execErr := tx.Exec(ctx, `
			UPDATE sync_events SET uid = $1
			 WHERE project_prefix = $2 AND node_id = $3
			   AND op_type = 'create_node' AND uid IS NULL`,
			localUIDs[nodeID], projectPrefix, nodeID)
		if execErr != nil {
			return UIDRepairReport{}, fmt.Errorf("repair uids: stamp %s/%s: %s",
				projectPrefix, nodeID, redact.DSN(execErr.Error()))
		}
		report.Stamped += int(tag.RowsAffected())
	}

	if err := tx.Commit(ctx); err != nil {
		return UIDRepairReport{}, fmt.Errorf("repair uids: commit: %s", redact.DSN(err.Error()))
	}
	return report, nil
}

// classifyHubCreates reads every create_node row for the project and
// sorts each into: needs a stamp (returned), already stamped, mismatched,
// or hub-only. A node with several create rows (a --force re-backfill
// left duplicates) is classified once; the UPDATE by node_id stamps every
// NULL row it has.
func classifyHubCreates(ctx context.Context, tx pgx.Tx, projectPrefix string,
	localUIDs map[string]string, report *UIDRepairReport,
) ([]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT node_id, uid
		  FROM sync_events
		 WHERE project_prefix = $1 AND op_type = 'create_node'
		 ORDER BY node_id, event_id`,
		projectPrefix)
	if err != nil {
		return nil, fmt.Errorf("repair uids: read creates: %s", redact.DSN(err.Error()))
	}
	defer rows.Close()

	seen := map[string]bool{}
	var toStamp []string
	for rows.Next() {
		var nodeID string
		var hubUID *string
		if err := rows.Scan(&nodeID, &hubUID); err != nil {
			return nil, fmt.Errorf("repair uids: scan create: %s", redact.DSN(err.Error()))
		}
		if seen[nodeID] {
			continue
		}
		seen[nodeID] = true

		if classifyOneCreate(nodeID, hubUID, localUIDs, report) {
			toStamp = append(toStamp, nodeID)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("repair uids: iterate creates: %s", redact.DSN(err.Error()))
	}
	sort.Strings(toStamp)
	sort.Strings(report.HubOnly)
	return toStamp, nil
}

// classifyOneCreate files one hub create row into the report and reports
// whether it needs a stamp.
func classifyOneCreate(nodeID string, hubUID *string, localUIDs map[string]string,
	report *UIDRepairReport,
) bool {
	localUID, known := localUIDs[nodeID]
	stamped := hubUID != nil && *hubUID != ""
	switch {
	case !stamped && known:
		return true
	case !stamped: // and not known locally
		report.HubOnly = append(report.HubOnly, nodeID)
	case known && *hubUID == localUID:
		report.AlreadyStamped++
	case known: // and the uids differ
		report.Mismatched = append(report.Mismatched,
			UIDMismatch{NodeID: nodeID, HubUID: *hubUID, LocalUID: localUID})
	}
	// stamped && !known: a node this store never had, already carrying a
	// uid — nothing for this client to say about it.
	return false
}
