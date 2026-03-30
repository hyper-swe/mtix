// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// ImportMode controls how import handles existing data per FR-7.8.
type ImportMode string

const (
	// ImportModeReplace drops all data and reimports from export.
	ImportModeReplace ImportMode = "replace"
	// ImportModeMerge merges imported data with existing, using content_hash
	// comparison to detect changes per FR-7.8.
	ImportModeMerge ImportMode = "merge"
)

// ImportResult holds the outcome of an import operation per FR-7.8.
type ImportResult struct {
	NodesCreated  int `json:"nodes_created"`
	NodesUpdated  int `json:"nodes_updated"`
	NodesSkipped  int `json:"nodes_skipped"`
	DepsImported  int `json:"deps_imported"`
	FTSRebuilt    bool `json:"fts_rebuilt"`
}

// Import loads data from an ExportData structure per FR-7.8.
// Verifies node_count and checksum before importing. Supports replace
// and merge modes. Rebuilds sequences and FTS index after bulk import.
// If force is false, importing zero nodes into a non-empty database is rejected.
func (s *Store) Import(
	ctx context.Context,
	data *ExportData,
	mode ImportMode,
	force bool,
) (*ImportResult, error) {
	if data == nil {
		return nil, fmt.Errorf("import data is nil: %w", model.ErrInvalidInput)
	}

	// Verify node count per FR-7.8.
	if data.NodeCount != len(data.Nodes) {
		return nil, fmt.Errorf(
			"node count mismatch: declared %d, actual %d: %w",
			data.NodeCount, len(data.Nodes), model.ErrInvalidInput)
	}

	// Verify checksum per FR-7.8.
	valid, err := VerifyExportChecksum(data)
	if err != nil {
		return nil, fmt.Errorf("verify checksum: %w", err)
	}
	if !valid {
		return nil, fmt.Errorf("checksum verification failed: %w", model.ErrInvalidInput)
	}

	// Reject zero-node imports into non-empty databases unless forced.
	if len(data.Nodes) == 0 && !force {
		var existingCount int
		if err := s.readDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM nodes").Scan(&existingCount); err == nil && existingCount > 0 {
			return nil, fmt.Errorf(
				"import contains zero nodes but database has %d — use --force to confirm: %w",
				existingCount, model.ErrInvalidInput)
		}
	}

	var result ImportResult

	if mode == ImportModeReplace {
		result, err = s.importReplace(ctx, data)
	} else {
		result, err = s.importMerge(ctx, data)
	}
	if err != nil {
		return nil, err
	}

	// Rebuild sequences from imported data per FR-7.8.x.
	if err := s.rebuildSequences(ctx); err != nil {
		return nil, fmt.Errorf("rebuild sequences: %w", err)
	}

	// Rebuild FTS index after bulk import per FR-7.8.
	if err := s.rebuildFTS(ctx); err != nil {
		return nil, fmt.Errorf("rebuild FTS: %w", err)
	}
	result.FTSRebuilt = true

	return &result, nil
}

// importReplace drops all data and reimports from export per FR-7.8.
func (s *Store) importReplace(ctx context.Context, data *ExportData) (ImportResult, error) {
	var result ImportResult

	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		if err := clearAllTables(ctx, tx); err != nil {
			return err
		}
		var insertErr error
		result, insertErr = insertAllExportData(ctx, tx, data)
		return insertErr
	})

	return result, err
}

// clearAllTables deletes all data from tables in FK-safe order.
func clearAllTables(ctx context.Context, tx *sql.Tx) error {
	tables := []string{"dependencies", "sessions", "agents", "nodes", "sequences"}
	for _, table := range tables {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+table); err != nil {
			return fmt.Errorf("clear table %s: %w", table, err)
		}
	}
	return nil
}

// insertAllExportData inserts nodes, deps, agents, and sessions from export data.
func insertAllExportData(ctx context.Context, tx *sql.Tx, data *ExportData) (ImportResult, error) {
	var result ImportResult

	for i := range data.Nodes {
		if err := insertExportNode(ctx, tx, &data.Nodes[i]); err != nil {
			return result, fmt.Errorf("insert node %s: %w", data.Nodes[i].ID, err)
		}
		result.NodesCreated++
	}

	for i := range data.Dependencies {
		if err := insertExportDep(ctx, tx, &data.Dependencies[i]); err != nil {
			return result, fmt.Errorf("insert dep %s->%s: %w", data.Dependencies[i].FromID, data.Dependencies[i].ToID, err)
		}
		result.DepsImported++
	}

	for i := range data.Agents {
		if err := insertExportAgent(ctx, tx, &data.Agents[i]); err != nil {
			return result, fmt.Errorf("insert agent %s: %w", data.Agents[i].AgentID, err)
		}
	}

	for i := range data.Sessions {
		if err := insertExportSession(ctx, tx, &data.Sessions[i]); err != nil {
			return result, fmt.Errorf("insert session %s: %w", data.Sessions[i].ID, err)
		}
	}

	return result, nil
}

// importMerge merges imported data with existing using content_hash per FR-7.8.
func (s *Store) importMerge(ctx context.Context, data *ExportData) (ImportResult, error) {
	var result ImportResult

	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		for i := range data.Nodes {
			action, mergeErr := mergeImportNode(ctx, tx, &data.Nodes[i])
			if mergeErr != nil {
				return mergeErr
			}
			switch action {
			case importActionCreated:
				result.NodesCreated++
			case importActionUpdated:
				result.NodesUpdated++
			case importActionSkipped:
				result.NodesSkipped++
			}
		}

		for _, d := range data.Dependencies {
			if err := insertExportDep(ctx, tx, &d); err != nil {
				return fmt.Errorf("insert dep %s->%s: %w", d.FromID, d.ToID, err)
			}
			result.DepsImported++
		}

		return nil
	})

	return result, err
}

// importAction represents the result of merging a single node.
type importAction int

const (
	importActionCreated importAction = iota
	importActionUpdated
	importActionSkipped
)

// mergeImportNode handles the merge logic for a single imported node.
func mergeImportNode(ctx context.Context, tx *sql.Tx, n *exportNode) (importAction, error) {
	var existingHash sql.NullString
	err := tx.QueryRowContext(ctx,
		"SELECT content_hash FROM nodes WHERE id = ?", n.ID,
	).Scan(&existingHash)

	if err == sql.ErrNoRows {
		if insertErr := insertExportNode(ctx, tx, n); insertErr != nil {
			return 0, fmt.Errorf("insert node %s: %w", n.ID, insertErr)
		}
		return importActionCreated, nil
	}
	if err != nil {
		return 0, fmt.Errorf("check node %s: %w", n.ID, err)
	}

	if existingHash.Valid && existingHash.String == n.ContentHash {
		return importActionSkipped, nil
	}

	if err := updateExportNode(ctx, tx, n); err != nil {
		return 0, fmt.Errorf("update node %s: %w", n.ID, err)
	}
	return importActionUpdated, nil
}

// insertExportNode inserts a node from export data.
func insertExportNode(ctx context.Context, tx *sql.Tx, n *exportNode) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO nodes (id, parent_id, depth, seq, project,
		  title, description, prompt, acceptance, node_type,
		  issue_type, priority, labels, status, progress,
		  assignee, creator, agent_state, weight, content_hash,
		  created_at, updated_at, closed_at, defer_until, deleted_at)
		 VALUES (?,?,?,?,?, ?,?,?,?,?, ?,?,?,?,?, ?,?,?,?,?, ?,?,?,?,?)`,
		n.ID, nullStr(n.ParentID), n.Depth, n.Seq, n.Project,
		n.Title, nullStr(n.Description), nullStr(n.Prompt),
		nullStr(n.Acceptance), n.NodeType,
		nullStr(n.IssueType), n.Priority, n.Labels, n.Status, n.Progress,
		nullStr(n.Assignee), nullStr(n.Creator), nullStr(n.AgentState),
		n.Weight, nullStr(n.ContentHash),
		n.CreatedAt, n.UpdatedAt, nullStr(n.ClosedAt),
		nullStr(n.DeferUntil), nullStr(n.DeletedAt),
	)
	return err
}

// updateExportNode updates an existing node from export data.
func updateExportNode(ctx context.Context, tx *sql.Tx, n *exportNode) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE nodes SET
		  parent_id=?, depth=?, seq=?, project=?,
		  title=?, description=?, prompt=?, acceptance=?, node_type=?,
		  issue_type=?, priority=?, labels=?, status=?, progress=?,
		  assignee=?, creator=?, agent_state=?, weight=?, content_hash=?,
		  created_at=?, updated_at=?, closed_at=?, defer_until=?, deleted_at=?
		 WHERE id=?`,
		nullStr(n.ParentID), n.Depth, n.Seq, n.Project,
		n.Title, nullStr(n.Description), nullStr(n.Prompt),
		nullStr(n.Acceptance), n.NodeType,
		nullStr(n.IssueType), n.Priority, n.Labels, n.Status, n.Progress,
		nullStr(n.Assignee), nullStr(n.Creator), nullStr(n.AgentState),
		n.Weight, nullStr(n.ContentHash),
		n.CreatedAt, n.UpdatedAt, nullStr(n.ClosedAt),
		nullStr(n.DeferUntil), nullStr(n.DeletedAt),
		n.ID,
	)
	return err
}

// insertExportDep inserts a dependency from export data (INSERT OR IGNORE).
func insertExportDep(ctx context.Context, tx *sql.Tx, d *exportDep) error {
	_, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO dependencies (from_id, to_id, dep_type, created_at)
		 VALUES (?, ?, ?, ?)`,
		d.FromID, d.ToID, d.DepType, d.CreatedAt,
	)
	return err
}

// insertExportAgent inserts an agent from export data per FR-10.1a.
func insertExportAgent(ctx context.Context, tx *sql.Tx, a *exportAgent) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO agents (agent_id, project, state, current_node_id, last_heartbeat, state_changed_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		a.AgentID, a.Project, a.State, nullStr(a.CurrentNodeID),
		nullStr(a.LastHeartbeat), nullStr(a.LastHeartbeat),
	)
	return err
}

// insertExportSession inserts a session from export data per FR-10.5a.
func insertExportSession(ctx context.Context, tx *sql.Tx, s *exportSession) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO sessions (id, agent_id, project, started_at, ended_at, status, summary)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		s.ID, s.AgentID, s.Project, s.StartedAt,
		nullStr(s.EndedAt), s.Status, nullStr(s.Summary),
	)
	return err
}

// rebuildSequences recalculates sequence counters from existing node data per FR-7.8.x.
func (s *Store) rebuildSequences(ctx context.Context) error {
	_, err := s.writeDB.ExecContext(ctx, "DELETE FROM sequences")
	if err != nil {
		return fmt.Errorf("clear sequences: %w", err)
	}

	// Rebuild from max seq per parent per project.
	rows, err := s.readDB.QueryContext(ctx,
		`SELECT project || ':' || COALESCE(parent_id, ''), MAX(seq)
		 FROM nodes
		 GROUP BY project, COALESCE(parent_id, '')`)
	if err != nil {
		return fmt.Errorf("query max sequences: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			s.logger.Error("failed to close sequence rows", "error", closeErr)
		}
	}()

	for rows.Next() {
		var key string
		var maxSeq int
		if err := rows.Scan(&key, &maxSeq); err != nil {
			return fmt.Errorf("scan sequence: %w", err)
		}
		_, err := s.writeDB.ExecContext(ctx,
			"INSERT INTO sequences (key, value) VALUES (?, ?)", key, maxSeq)
		if err != nil {
			return fmt.Errorf("insert sequence %s: %w", key, err)
		}
	}
	return rows.Err()
}

// rebuildFTS rebuilds the FTS5 search index per FR-7.8.
// Drops and recreates the FTS content to ensure consistency after bulk import.
func (s *Store) rebuildFTS(ctx context.Context) error {
	// Rebuild the FTS5 index from the content table.
	_, err := s.writeDB.ExecContext(ctx,
		"INSERT INTO nodes_fts(nodes_fts) VALUES('rebuild')")
	if err != nil {
		return fmt.Errorf("rebuild FTS: %w", err)
	}
	return nil
}

// nullStr returns nil for empty strings to store NULL in SQLite.
func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

