// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
)

// BoardNodeState is one node of a store as the worktree enrolment compares it
// with the hub (MTIX-112.17.16.6): the node's durable identity, a fingerprint of
// everything an agent can change on it, and the ids of the journal events
// that mention it.
type BoardNodeState struct {
	// UID is the node's durable identity; empty only for a node that has none.
	UID string
	// ID is the display id.
	ID string
	// Deleted reports a soft-deleted row.
	Deleted bool
	// Fingerprint hashes the node's content, status, assignee, annotations,
	// references and outgoing dependencies. Timestamps and derived columns
	// (progress, content_hash) are left out: a replica rebuilt from the same
	// events may stamp them differently.
	Fingerprint string
	// EventIDs are the ids of the journal rows of this node, whatever their
	// sync status, sorted.
	EventIDs []string
}

// Key is the identity the enrolment matches on: the uid when there is one.
func (n BoardNodeState) Key() string {
	if n.UID != "" {
		return n.UID
	}
	return "id:" + n.ID
}

// boardNodeFields is the canonical content the fingerprint covers.
type boardNodeFields struct {
	ID, ParentID, Title, Description, Prompt, Acceptance string
	IssueType, Labels, Status, PreviousStatus, Assignee  string
	AgentState, DeferUntil, CodeRefs, CommitRefs         string
	Annotations, Metadata                                string
	Priority                                             int
	EstimateMin, ActualMin                               int64
	Deleted                                              bool
	Deps                                                 []string
}

// BoardNodeStates returns the state of every node row, soft-deleted ones
// included, sorted by key (MTIX-112.17.16.6, ADR-007 section 4.2). It reads
// one consistent view of the store.
func (s *Store) BoardNodeStates(ctx context.Context) ([]BoardNodeState, error) {
	tx, err := s.readDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin board state read: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	deps, err := boardDependencies(ctx, tx)
	if err != nil {
		return nil, err
	}
	events, err := boardEvents(ctx, tx)
	if err != nil {
		return nil, err
	}
	out, err := scanBoardNodes(ctx, tx, deps, events)
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key() < out[j].Key() })
	return out, nil
}

// scanBoardNodes reads every node row into a state.
func scanBoardNodes(ctx context.Context, tx *sql.Tx, deps, events map[string][]string) ([]BoardNodeState, error) {
	// Every node row, soft-deleted included: a deletion is a state to compare,
	// not a row to skip. NULL columns read as empty text.
	rows, err := tx.QueryContext(ctx, `SELECT id, COALESCE(uid,''), COALESCE(parent_id,''), title,
		COALESCE(description,''), COALESCE(prompt,''), COALESCE(acceptance,''), COALESCE(issue_type,''),
		COALESCE(priority,0), COALESCE(labels,''), COALESCE(status,''), COALESCE(previous_status,''),
		COALESCE(assignee,''), COALESCE(agent_state,''), COALESCE(defer_until,''), COALESCE(estimate_min,0),
		COALESCE(actual_min,0), COALESCE(code_refs,''), COALESCE(commit_refs,''), COALESCE(annotations,''),
		COALESCE(metadata,''), deleted_at IS NOT NULL FROM nodes`)
	if err != nil {
		return nil, fmt.Errorf("read nodes for the board state: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []BoardNodeState
	for rows.Next() {
		var uid string
		var f boardNodeFields
		if scanErr := rows.Scan(&f.ID, &uid, &f.ParentID, &f.Title, &f.Description, &f.Prompt, &f.Acceptance,
			&f.IssueType, &f.Priority, &f.Labels, &f.Status, &f.PreviousStatus, &f.Assignee, &f.AgentState,
			&f.DeferUntil, &f.EstimateMin, &f.ActualMin, &f.CodeRefs, &f.CommitRefs, &f.Annotations,
			&f.Metadata, &f.Deleted); scanErr != nil {
			return nil, fmt.Errorf("scan node for the board state: %w", scanErr)
		}
		f.Deps = deps[f.ID]
		f.Labels, f.CodeRefs, f.CommitRefs = emptyJSON(f.Labels), emptyJSON(f.CodeRefs), emptyJSON(f.CommitRefs)
		f.Annotations, f.Metadata = emptyJSON(f.Annotations), emptyJSON(f.Metadata)
		st := BoardNodeState{UID: uid, ID: f.ID, Deleted: f.Deleted}
		if st.Fingerprint, err = fingerprintBoardNode(&f); err != nil {
			return nil, err
		}
		st.EventIDs = events[st.Key()]
		out = append(out, st)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate nodes for the board state: %w", err)
	}
	return out, nil
}

// emptyJSON reads a column holding an empty JSON array or object as empty text.
// A node written directly stores NULL where the same node rebuilt from the
// journal stores an empty array; both mean nothing is set.
func emptyJSON(v string) string {
	switch v {
	case "[]", "{}", "null":
		return ""
	}
	return v
}

func fingerprintBoardNode(f *boardNodeFields) (string, error) {
	body, err := json.Marshal(f)
	if err != nil {
		return "", fmt.Errorf("encode node %s for the board state: %w", f.ID, err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(body)), nil
}

// boardDependencies maps a node id to its sorted outgoing dependencies.
func boardDependencies(ctx context.Context, tx *sql.Tx) (map[string][]string, error) {
	// Outgoing dependency edges of every node: part of the node's state.
	rows, err := tx.QueryContext(ctx, `SELECT from_id, to_id, dep_type FROM dependencies`)
	if err != nil {
		return nil, fmt.Errorf("read dependencies for the board state: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string][]string{}
	for rows.Next() {
		var from, to, kind string
		if err := rows.Scan(&from, &to, &kind); err != nil {
			return nil, fmt.Errorf("scan dependency for the board state: %w", err)
		}
		out[from] = append(out[from], to+"\x00"+kind)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate dependencies for the board state: %w", err)
	}
	for _, v := range out {
		sort.Strings(v)
	}
	return out, nil
}

// boardEvents maps a node key (uid, else "id:"+display id) to its journal event
// ids, sorted.
func boardEvents(ctx context.Context, tx *sql.Tx) (map[string][]string, error) {
	// Every journal row, whatever its status, with the node it names.
	rows, err := tx.QueryContext(ctx, `SELECT event_id, COALESCE(uid,''), node_id FROM sync_events`)
	if err != nil {
		return nil, fmt.Errorf("read the journal for the board state: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string][]string{}
	for rows.Next() {
		var eventID, uid, nodeID string
		if err := rows.Scan(&eventID, &uid, &nodeID); err != nil {
			return nil, fmt.Errorf("scan journal row for the board state: %w", err)
		}
		key := uid
		if key == "" {
			key = "id:" + nodeID
		}
		out[key] = append(out[key], eventID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate the journal for the board state: %w", err)
	}
	for _, v := range out {
		sort.Strings(v)
	}
	return out, nil
}
