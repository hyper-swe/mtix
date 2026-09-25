// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/hyper-swe/mtix/internal/model"
)

// DecodeExportData streams an ExportData from r using a json.Decoder rather than
// reading the whole file into a byte slice and json.Unmarshal-ing it (MTIX-2.3.1).
// The decoder pulls from the reader incrementally, so a large export is not held
// as both raw bytes AND a parsed tree at peak — it removes the redundant
// whole-file copy the ReadFile+Unmarshal path kept alive during parsing.
//
// It preserves json.Unmarshal's strictness: content after the top-level JSON
// value (other than trailing whitespace) is rejected, so a truncated or
// concatenated file does not silently import its first object and ignore the
// rest.
func DecodeExportData(r io.Reader) (*ExportData, error) {
	dec := json.NewDecoder(bufio.NewReader(r))
	var data ExportData
	if err := dec.Decode(&data); err != nil {
		return nil, fmt.Errorf("parse export data: %w", err)
	}
	// Reject trailing non-whitespace, matching json.Unmarshal. Token() skips
	// whitespace and returns io.EOF when only whitespace remains; anything else
	// (another value, a stray token, a syntax error) means the file is not a
	// single clean export object.
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		if err != nil {
			return nil, fmt.Errorf("parse export data: trailing content after export object: %w", err)
		}
		return nil, fmt.Errorf("unexpected trailing content after export object: %w", model.ErrInvalidInput)
	}
	return &data, nil
}

// ErrImportIncomplete marks an import error raised after the import's
// transaction committed, while rebuilding the sequence counters or the
// search index (MTIX-95.31.1). Every other import error leaves the store
// unchanged.
var ErrImportIncomplete = errors.New("import applied, but rebuilding its indexes failed")

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
	NodesCreated int  `json:"nodes_created"`
	NodesUpdated int  `json:"nodes_updated"`
	NodesSkipped int  `json:"nodes_skipped"`
	DepsImported int  `json:"deps_imported"`
	FTSRebuilt   bool `json:"fts_rebuilt"`
	// UIDAdoptions lists the local tasks a merge gave the file's uid; only
	// ImportReconcile fills it (MTIX-95.31.6).
	UIDAdoptions []ImportUIDAdoption `json:"uid_adoptions,omitempty"`
}

// ValidateExport runs the checks every import makes before it writes
// anything (FR-7.8): node_count matches the nodes, the checksum verifies,
// and every time value can be read back (MTIX-95.31.1). The automatic
// import runs it before it takes its pre-import backup, so a file that
// would be rejected never costs a backup (MTIX-95.31.2).
func ValidateExport(data *ExportData) error {
	if data == nil {
		return fmt.Errorf("import data is nil: %w", model.ErrInvalidInput)
	}

	// Verify node count per FR-7.8.
	if data.NodeCount != len(data.Nodes) {
		return fmt.Errorf(
			"node count mismatch: declared %d, actual %d: %w",
			data.NodeCount, len(data.Nodes), model.ErrInvalidInput)
	}

	// Verify checksum per FR-7.8.
	valid, err := VerifyExportChecksum(data)
	if err != nil {
		return fmt.Errorf("verify checksum: %w", err)
	}
	if !valid {
		return fmt.Errorf("checksum verification failed: %w", model.ErrInvalidInput)
	}

	// Reject a time value no reader could parse back (not RFC 3339, or a
	// UTC year outside 1..9999, model.IsStorableTime) before anything is
	// written (MTIX-95.31.1).
	return validateExportTimes(data)
}

// Import loads data from an ExportData structure per FR-7.8.
// Verifies node_count, checksum and every time value (ValidateExport,
// MTIX-95.31.1) before importing, so a rejected file writes nothing.
// Supports replace and merge modes. Rebuilds sequences and FTS index after
// bulk import. If force is false, importing zero nodes into a non-empty
// database is rejected.
// MTIX-95.31.4: one transaction writes everything, after it re-checks the
// store (IfStoreUnchanged) and renumbers the local tasks ImportReconcile
// planned to move (renumberLocalFirst).
func (s *Store) Import(
	ctx context.Context,
	data *ExportData,
	mode ImportMode,
	force bool,
	opts ...ImportOption,
) (*ImportResult, error) {
	if err := ValidateExport(data); err != nil {
		return nil, err
	}
	if err := s.refuseEmptyImport(ctx, data, force); err != nil {
		return nil, err
	}
	cfg := collectImportOptions(opts)

	var result ImportResult
	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		// MTIX-95.31.4: the store must still be the one the caller checked.
		if err := s.verifyStoreUnchanged(ctx, tx, cfg); err != nil {
			return err
		}
		// MTIX-95.31.4: move each local task the file's task takes the id of.
		if err := applyLocalRenumbers(ctx, tx, cfg.localMoves); err != nil {
			return err
		}
		var applyErr error
		if mode == ImportModeReplace {
			result, applyErr = replaceAllData(ctx, tx, data)
		} else {
			result, applyErr = mergeAllData(ctx, tx, data)
		}
		return applyErr
	})
	if err != nil {
		return nil, err
	}

	// Rebuild sequences from imported data per FR-7.8.x.
	if err := s.rebuildSequences(ctx); err != nil {
		return nil, fmt.Errorf("rebuild sequences: %w: %w", ErrImportIncomplete, err)
	}

	// Rebuild FTS index after bulk import per FR-7.8.
	if err := s.rebuildFTS(ctx); err != nil {
		return nil, fmt.Errorf("rebuild FTS: %w: %w", ErrImportIncomplete, err)
	}
	result.FTSRebuilt = true

	return &result, nil
}

// replaceAllData drops all data and reimports from export per FR-7.8,
// inside the import's transaction.
func replaceAllData(ctx context.Context, tx *sql.Tx, data *ExportData) (ImportResult, error) {
	if err := clearAllTables(ctx, tx); err != nil {
		return ImportResult{}, err
	}
	return insertAllExportData(ctx, tx, data)
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

// mergeAllData merges imported data with existing using content_hash per
// FR-7.8, inside the import's transaction. Whether the file carries the
// columns schema 2.0.0 added decides how their absence is read
// (MTIX-95.31.1, carriesNodeColumns).
func mergeAllData(ctx context.Context, tx *sql.Tx, data *ExportData) (ImportResult, error) {
	var result ImportResult
	fileCarriesAllColumns := carriesNodeColumns(data.SchemaVersion)
	for i := range data.Nodes {
		action, mergeErr := mergeImportNode(ctx, tx, &data.Nodes[i], fileCarriesAllColumns)
		if mergeErr != nil {
			return result, mergeErr
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
			return result, fmt.Errorf("insert dep %s->%s: %w", d.FromID, d.ToID, err)
		}
		result.DepsImported++
	}
	return result, nil
}

// importAction represents the result of merging a single node.
type importAction int

const (
	importActionCreated importAction = iota
	importActionUpdated
	importActionSkipped
)

// mergeImportNode merges one imported node into the store (FR-7.8). A node
// new to the store is inserted as exported; one the store holds as a
// different task is never overwritten (refuseDifferentTask), and a local
// value that a stale copy leaves empty is kept (keepLocalBlankedFields,
// MTIX-95.31.4). For a node the store holds,
// annotations and the activity stream merge as a union that never drops a
// local entry (mergeNodeStreams, MTIX-95.31.1): an incoming node without
// annotations keeps the local ones. The other columns take the incoming
// values only when the content hash differs, and a file older than schema
// 2.0.0 carries none of the 2.0.0 columns, so for those the local values
// stand (keepLocalNodeColumns).
func mergeImportNode(ctx context.Context, tx *sql.Tx, n *exportNode, fileCarriesAllColumns bool) (importAction, error) {
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

	local, err := scanExportNode(tx.QueryRowContext(ctx, exportNodeSelectSQL+" WHERE id = ?", n.ID))
	if err != nil {
		return 0, fmt.Errorf("read local node %s: %w", n.ID, err)
	}
	if err := refuseDifferentTask(&local, n); err != nil {
		return 0, err
	}
	merged := *n // merge into a copy: the caller's export must still verify
	if !fileCarriesAllColumns {
		keepLocalNodeColumns(&merged, &local)
	}
	if !copyIsCurrent(&local, n, fileCarriesAllColumns) { // MTIX-95.31.4
		if err := keepLocalBlankedFields(&merged, &local); err != nil {
			return 0, fmt.Errorf("merge node %s: %w", n.ID, err)
		}
	}
	streamsChanged := mergeNodeStreams(&merged, &local)

	if existingHash.Valid && existingHash.String == n.ContentHash {
		return mergeUnchangedContent(ctx, tx, &merged, local.UID, streamsChanged)
	}

	if err := updateExportNode(ctx, tx, &merged); err != nil {
		return 0, fmt.Errorf("update node %s: %w", n.ID, err)
	}
	return importActionUpdated, nil
}

// insertExportNode inserts a node from export data, writing every exported
// column, annotations and the activity stream included (MTIX-95.31.1), so
// a replace import restores exactly the store the export was taken from.
// node_type is derived from depth (not trusted from the file) for
// tamper resistance and cross-version compatibility. The durable uid
// is persisted so re-import stays idempotent and import-boundary uid
// validation can run (ADR-003 §6, §7; audit F-3).
func insertExportNode(ctx context.Context, tx *sql.Tx, n *exportNode) error {
	n.NodeType = string(model.NodeTypeForDepth(n.Depth))
	cols, err := encodeNodeColumns(n)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO nodes (id, parent_id, depth, seq, project,
		  title, description, prompt, acceptance, node_type,
		  issue_type, priority, labels, status, progress,
		  assignee, creator, agent_state, weight, content_hash,
		  created_at, updated_at, closed_at, defer_until, deleted_at,
		  uid, previous_status, estimate_min, actual_min, code_refs,
		  commit_refs, annotations, invalidated_at, invalidated_by, invalidation_reason,
		  activity, deleted_by, metadata, session_id)
		 VALUES (?,?,?,?,?, ?,?,?,?,?, ?,?,?,?,?, ?,?,?,?,?, ?,?,?,?,?,
		         ?,?,?,?,?, ?,?,?,?,?, ?,?,?,?)`,
		n.ID, nullStr(n.ParentID), n.Depth, n.Seq, n.Project,
		n.Title, nullStr(n.Description), nullStr(n.Prompt),
		nullStr(n.Acceptance), n.NodeType,
		nullStr(n.IssueType), n.Priority, n.Labels, n.Status, n.Progress,
		nullStr(n.Assignee), nullStr(n.Creator), nullStr(n.AgentState),
		n.Weight, nullStr(n.ContentHash),
		n.CreatedAt, n.UpdatedAt, nullStr(n.ClosedAt),
		nullStr(n.DeferUntil), nullStr(n.DeletedAt),
		nullStr(n.UID), nullStr(n.PreviousStatus), cols.estimateMin, cols.actualMin, cols.codeRefs,
		cols.commitRefs, cols.annotations, nullStr(n.InvalidatedAt),
		nullStr(n.InvalidatedBy), nullStr(n.InvalidationReason),
		cols.activity, nullStr(n.DeletedBy), nullStr(n.Metadata), nullStr(n.SessionID),
	)
	return err
}

// updateExportNode updates an existing node from export data, writing every
// exported column (MTIX-95.31.1); merge import has already set n's
// annotations and activity to their union with the local ones.
// node_type is derived from depth for consistency. The durable uid is
// preserved only when the export carries one: a pre-v3 export (empty uid)
// must not blank an already-backfilled local uid (ADR-003 §6, §7).
func updateExportNode(ctx context.Context, tx *sql.Tx, n *exportNode) error {
	n.NodeType = string(model.NodeTypeForDepth(n.Depth))
	cols, err := encodeNodeColumns(n)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx,
		`UPDATE nodes SET
		  parent_id=?, depth=?, seq=?, project=?,
		  title=?, description=?, prompt=?, acceptance=?, node_type=?,
		  issue_type=?, priority=?, labels=?, status=?, progress=?,
		  assignee=?, creator=?, agent_state=?, weight=?, content_hash=?,
		  created_at=?, updated_at=?, closed_at=?, defer_until=?, deleted_at=?,
		  uid=COALESCE(?, uid), previous_status=?, estimate_min=?, actual_min=?,
		  code_refs=?, commit_refs=?, annotations=?, invalidated_at=?,
		  invalidated_by=?, invalidation_reason=?, activity=?, deleted_by=?,
		  metadata=?, session_id=?
		 WHERE id=?`,
		nullStr(n.ParentID), n.Depth, n.Seq, n.Project,
		n.Title, nullStr(n.Description), nullStr(n.Prompt),
		nullStr(n.Acceptance), n.NodeType,
		nullStr(n.IssueType), n.Priority, n.Labels, n.Status, n.Progress,
		nullStr(n.Assignee), nullStr(n.Creator), nullStr(n.AgentState),
		n.Weight, nullStr(n.ContentHash),
		n.CreatedAt, n.UpdatedAt, nullStr(n.ClosedAt),
		nullStr(n.DeferUntil), nullStr(n.DeletedAt),
		nullStr(n.UID), nullStr(n.PreviousStatus), cols.estimateMin, cols.actualMin,
		cols.codeRefs, cols.commitRefs, cols.annotations, nullStr(n.InvalidatedAt),
		nullStr(n.InvalidatedBy), nullStr(n.InvalidationReason), cols.activity, nullStr(n.DeletedBy),
		nullStr(n.Metadata), nullStr(n.SessionID),
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
// Runs outside WithTx and can be large after a bulk import, so it carries
// its own NFR-2.8 guards.
func (s *Store) rebuildFTS(ctx context.Context) error {
	if err := s.preflightWrite(); err != nil {
		return err
	}

	// Rebuild the FTS5 index from the content table.
	_, err := s.writeDB.ExecContext(ctx,
		"INSERT INTO nodes_fts(nodes_fts) VALUES('rebuild')")
	if err != nil {
		return s.classifyWriteError(fmt.Errorf("rebuild FTS: %w", err))
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
