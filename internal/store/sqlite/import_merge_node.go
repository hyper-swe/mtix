// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
)

// mergeAllData merges imported data with existing using content_hash per
// FR-7.8, inside the import's transaction. Whether the file carries the
// columns schema 2.0.0 added decides how their absence is read
// (MTIX-95.31.1, carriesNodeColumns). Before it writes anything it settles
// the workflow conflicts, and returns *WorkflowConflictError, writing
// nothing, when any is left (MTIX-95.31.13).
func mergeAllData(ctx context.Context, tx *sql.Tx, data *ExportData, policy workflowPolicy) (ImportResult, error) {
	var result ImportResult
	settled, err := planWorkflow(ctx, tx, data, policy)
	if err != nil {
		return result, err
	}
	fileCarriesAllColumns := carriesNodeColumns(data.SchemaVersion)
	var touched []*exportNode // nodes the merge created or updated, rolled up once all are written (FR-5.7)
	for i := range data.Nodes {
		res := settled[data.Nodes[i].ID]
		action, mergeErr := mergeImportNode(ctx, tx, &data.Nodes[i], fileCarriesAllColumns, res)
		if mergeErr != nil {
			return result, mergeErr
		}
		switch action {
		case importActionCreated:
			result.NodesCreated++
		case importActionUpdated, importActionRolledUp:
			result.NodesUpdated++
		case importActionSkipped:
			result.NodesSkipped++
		}
		if action != importActionSkipped {
			touched = append(touched, &data.Nodes[i])
		}
		if res != nil {
			result.WorkflowResolved = append(result.WorkflowResolved, *res)
		}
	}

	for _, d := range data.Dependencies {
		if err := insertExportDep(ctx, tx, &d); err != nil {
			return result, fmt.Errorf("insert dep %s->%s: %w", d.FromID, d.ToID, err)
		}
		result.DepsImported++
	}
	if err := rollUpMerged(ctx, tx, touched); err != nil {
		return result, err
	}
	return result, nil
}

// importAction represents the result of merging a single node.
type importAction int

const (
	importActionCreated importAction = iota
	importActionUpdated
	importActionSkipped
	// importActionRolledUp is an update that changed the node's status,
	// deletion state or progress, so its ancestors' progress must be
	// recalculated (MTIX-95.31.13, FR-5.7).
	importActionRolledUp
)

// markRollUp turns an update that changed the node's status, deletion state
// or progress into importActionRolledUp (MTIX-95.31.13).
func markRollUp(action importAction, local, merged *exportNode) importAction {
	if action == importActionUpdated && (local.Status != merged.Status || local.Progress != merged.Progress ||
		local.DeletedAt != merged.DeletedAt) {
		return importActionRolledUp
	}
	return action
}

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
func mergeImportNode(ctx context.Context, tx *sql.Tx, n *exportNode, fileCarriesAllColumns bool,
	res *WorkflowConflict) (importAction, error) {
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
	if differentErr := refuseDifferentTask(&local, n); differentErr != nil {
		return 0, differentErr
	}
	merged := *n // merge into a copy: the caller's export must still verify
	if !fileCarriesAllColumns {
		keepLocalNodeColumns(&merged, &local)
	}
	if !copyIsCurrent(&local, n, fileCarriesAllColumns) { // MTIX-95.31.4
		if keepErr := keepLocalBlankedFields(&merged, &local); keepErr != nil {
			return 0, fmt.Errorf("merge node %s: %w", n.ID, keepErr)
		}
	}
	streamsChanged, choiceErr := mergeChoiceAndStreams(&merged, &local, n, fileCarriesAllColumns, res)
	if choiceErr != nil {
		return 0, choiceErr
	}

	if existingHash.Valid && existingHash.String == n.ContentHash {
		write := unchangedWrite{streams: streamsChanged, workflow: res != nil && res.Choice == WorkflowTheirs}
		action, err := mergeUnchangedContent(ctx, tx, &merged, local.UID, write)
		return markRollUp(action, &local, &merged), err
	}

	if updateErr := updateExportNode(ctx, tx, &merged); updateErr != nil {
		return 0, fmt.Errorf("update node %s: %w", n.ID, updateErr)
	}
	if carryErr := carryIfAdopted(ctx, tx, &merged, &local); carryErr != nil {
		return 0, carryErr
	}
	return markRollUp(importActionUpdated, &local, &merged), nil
}

// mergeChoiceAndStreams applies the caller's choice for a node's differing
// workflow values, if any, and merges the annotation and activity streams;
// it reports whether the streams changed (MTIX-95.31.13). A choice is
// recorded in the node's activity, so it always changes the streams.
func mergeChoiceAndStreams(merged, local, file *exportNode, fileCarriesAll bool, res *WorkflowConflict) (bool, error) {
	if res == nil {
		return mergeNodeStreams(merged, local), nil
	}
	applyWorkflowChoice(merged, local, file, res.Choice, fileCarriesAll)
	mergeNodeStreams(merged, local)
	if err := recordWorkflowChoice(merged, res); err != nil {
		return false, err
	}
	return true, nil
}

// rollUpMerged recalculates progress, in the import's transaction, for every
// node the merge created or updated (MTIX-95.31.13, FR-5.7): a node written
// from the file keeps the file's progress, which its live children may
// contradict, so a node with live children is recomputed from them and then
// its ancestors; a leaf's ancestors are recomputed from its parent. Deepest
// first, each start once, so an ancestor is settled after everything below.
func rollUpMerged(ctx context.Context, tx *sql.Tx, touched []*exportNode) error {
	sorted := append([]*exportNode(nil), touched...)
	sort.SliceStable(sorted, func(a, b int) bool { return sorted[a].Depth > sorted[b].Depth })
	started := make(map[string]bool, len(sorted))
	for _, n := range sorted {
		var children int
		// Count the node's live children: only a parent is recomputed from them.
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM nodes WHERE parent_id = ? AND deleted_at IS NULL`, n.ID).Scan(&children); err != nil {
			return fmt.Errorf("count children of %s: %w", n.ID, err)
		}
		start := n.ParentID
		if children > 0 {
			start = n.ID
		}
		if start == "" || started[start] {
			continue
		}
		started[start] = true
		if err := recalculateProgress(ctx, tx, start); err != nil {
			return fmt.Errorf("recalculate progress of %s: %w", start, err)
		}
	}
	return nil
}

// carryIfAdopted moves the task's unpushed events with a uid the merge
// adopted for it (MTIX-95.31.16).
func carryIfAdopted(ctx context.Context, tx *sql.Tx, merged, local *exportNode) error {
	if merged.UID == "" || merged.UID == local.UID {
		return nil
	}
	return carryAdoptedUID(ctx, tx, merged.ID, local.UID, merged.UID)
}
