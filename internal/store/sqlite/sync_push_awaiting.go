// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hyper-swe/mtix/internal/model"
)

// The creation gate of push (MTIX-95.37).
//
// INVARIANT: an event is not sent while the creation of the task it is
// about, of any task above it, or of the task a dependency link points to,
// is still pending. The hub may reject that creation as a renumber, and the
// rejected task then moves to another number (RenumberForHubRejection):
// an event sent before that, under the old number, would reach the
// teammate's task that holds it. After the creation is on the hub, under a
// number the hub accepted, every later event of the task is safe.

// EventsAwaitingCreation returns the ids of the events among events that
// must wait for a pending creation: an event about a task whose create_node
// is pending, or about a task below one, or a link or unlink whose target is
// such a task. The creation event itself waits only for the creations of the
// tasks above it. A task is found by the uid its event carries (so a number
// change does not matter), else by the number the event names. The read is
// two queries for the whole slice: the current numbers of the uids, then
// which of the tasks involved have a pending creation (the event's primary
// key; idx_nodes_uid).
func (s *Store) EventsAwaitingCreation(ctx context.Context, events []*model.SyncEvent) (map[string]struct{}, error) {
	waiting := make(map[string]struct{})
	if len(events) == 0 {
		return waiting, nil
	}
	current, err := s.currentNumbersByUID(ctx, events)
	if err != nil {
		return nil, err
	}
	lines := make(map[string][]string, len(events))
	var all []string
	seen := map[string]struct{}{}
	for _, e := range events {
		line := awaitingLine(e, current)
		lines[e.EventID] = line
		for _, id := range line {
			if _, ok := seen[id]; !ok {
				seen[id] = struct{}{}
				all = append(all, id)
			}
		}
	}
	pending, err := pendingCreationIDs(ctx, s.readDB, all)
	if err != nil {
		return nil, err
	}
	for id, line := range lines {
		for _, n := range line {
			if _, ok := pending[n]; ok {
				waiting[id] = struct{}{}
				break
			}
		}
	}
	return waiting, nil
}

// awaitingLine returns the numbers whose pending creation makes e wait: the
// task e is about and the tasks above it (only those above it for a
// creation), and, for a link or unlink, the target task and the tasks above
// it. current maps a uid to the current number of its node.
func awaitingLine(e *model.SyncEvent, current map[string]string) []string {
	subject := e.NodeID
	if e.UID != "" {
		subject = current[e.UID] // "" when no node has the uid: nothing to wait for
	}
	var line []string
	for i, id := range nodeLineOf(subject) {
		if i == 0 && e.OpType == model.OpCreateNode {
			continue // a creation does not wait for itself
		}
		line = append(line, id)
	}
	if e.OpType == model.OpLinkDep || e.OpType == model.OpUnlinkDep {
		line = append(line, nodeLineOf(linkTarget(e))...)
	}
	return line
}

// linkTarget returns the number a link or unlink event points to, or "" when
// its payload does not decode (the hub's validation holds such an event).
func linkTarget(e *model.SyncEvent) string {
	var p struct {
		DependsOn string `json:"depends_on_node_id"`
	}
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return ""
	}
	return p.DependsOn
}

// nodeLineOf returns id and its ancestors, deepest first: the dot-path
// prefixes of the number ("P-1.2.3", "P-1.2", "P-1"). Empty for "".
func nodeLineOf(id string) []string {
	var line []string
	for id != "" {
		line = append(line, id)
		i := strings.LastIndex(id, ".")
		if i < 0 {
			break
		}
		id = id[:i]
	}
	return line
}

// currentNumbersByUID maps each uid carried by events to the current number
// of the node that has it, soft-deleted or not, in one query.
func (s *Store) currentNumbersByUID(ctx context.Context, events []*model.SyncEvent) (map[string]string, error) {
	var uids []string
	for _, e := range events {
		if e.UID != "" {
			uids = append(uids, e.UID)
		}
	}
	out := make(map[string]string, len(uids))
	if len(uids) == 0 {
		return out, nil
	}
	arg, err := json.Marshal(uids)
	if err != nil {
		return nil, fmt.Errorf("read current numbers: %w", err)
	}
	// The current number of the node of each uid in the bound JSON array;
	// "uid <> ''" lets the partial idx_nodes_uid serve it (MTIX-95.47).
	rows, err := s.Query(ctx,
		`SELECT uid, id FROM nodes WHERE uid IN (SELECT value FROM json_each(?)) AND uid <> ''`, string(arg))
	if err != nil {
		return nil, fmt.Errorf("read current numbers: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var uid, id string
		if err := rows.Scan(&uid, &id); err != nil {
			return nil, fmt.Errorf("read current numbers: %w", err)
		}
		out[uid] = id
	}
	return out, rows.Err()
}

// pendingCreationSQL selects, from the numbers in the bound JSON array, the
// tasks whose own create_node event is still pending: the one predicate of
// "this task's number is not granted yet", shared by push (which waits for
// those creations) and pull (which refuses to apply a teammate's change to
// such a task, MTIX-95.37). A task made here has its creation's event id as
// its uid (ADR-003 §2), found by the primary key; a task that adopted another
// uid (a merge import) is found by the event's own uid (idx_sync_events_uid).
// Each branch starts from the nodes' primary key and joins an index, so the
// cost is that of the numbers asked about, not of the log; the unary plus
// keeps the planner from starting at idx_sync_events_op_type, which would read
// every create_node row. The plan is pinned by a test.
const pendingCreationSQL = `
	SELECT n.id FROM nodes n JOIN sync_events c ON c.event_id = n.uid
	 WHERE n.id IN (SELECT value FROM json_each(?)) AND n.uid <> ''
	   AND +c.op_type = 'create_node' AND +c.sync_status = 'pending'
	UNION
	SELECT n.id FROM nodes n JOIN sync_events c ON c.uid = n.uid AND c.uid <> ''
	 WHERE n.id IN (SELECT value FROM json_each(?)) AND n.uid <> ''
	   AND +c.op_type = 'create_node' AND +c.sync_status = 'pending'`

// pendingCreationQuerier is what pendingCreationIDs reads through: the store
// (push) or a transaction (pull apply).
type pendingCreationQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// pendingCreationIDs returns which of the numbers ids belong to a task whose
// create_node event is pending, in one query (pendingCreationSQL).
func pendingCreationIDs(ctx context.Context, q pendingCreationQuerier, ids []string) (map[string]struct{}, error) {
	out := make(map[string]struct{})
	if len(ids) == 0 {
		return out, nil
	}
	arg, err := json.Marshal(ids)
	if err != nil {
		return nil, fmt.Errorf("read pending creations: %w", err)
	}
	rows, err := q.QueryContext(ctx, pendingCreationSQL, string(arg), string(arg))
	if err != nil {
		return nil, fmt.Errorf("read pending creations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("read pending creations: %w", err)
		}
		out[id] = struct{}{}
	}
	return out, rows.Err()
}
