// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"fmt"
	"strings"
)

// ImportUIDAdoption is one local task that a merge import gives the file's
// uid (MTIX-95.31.6, FR-7.8): the file holds the same id under another uid,
// and the two count as one task (differentIdentity), so the local task
// takes the file's uid. Two different tasks that the identity rule cannot
// tell apart (two clones created the id in the same second, and at least
// one clone assigned its uid after the task was created: at upgrade, or as
// a backfill uid on import or open, as for a task whose create event its
// event log lacks, for example created before 0.2) are adopted too: the
// titles are the sign, and the backup taken before the merge holds the
// local task.
type ImportUIDAdoption struct {
	// ID is the id both copies hold.
	ID string `json:"id"`
	// LocalUID is the uid the local task gives up (empty when it had none).
	LocalUID string `json:"local_uid"`
	// FileUID is the uid the local task takes from the file.
	FileUID string `json:"file_uid"`
	// LocalTitle is the title of the local copy before the merge.
	LocalTitle string `json:"local_title"`
	// FileTitle is the title of the file's copy.
	FileTitle string `json:"file_title"`
}

// localIdentity is what the adoption plan reads of one local node.
type localIdentity struct{ id, uid, title string }

// planUIDAdoptions records in report.UIDAdoptions every local task a merge
// import will give the file's uid (MTIX-95.31.6, FR-7.8), as the merge
// writes it (mergeImportNode): a file node that carries a uid, at an id
// where a local node holding another uid, or none, ends once the planned
// moves and renumbers (report.Moved, report.LocalRenumbers) are applied.
// Run after every rewrite of the file (re-stamps and provisional
// renumbers), so the ids and uids are the ones written. A replace adopts
// nothing: it writes the file's store. Nothing is written.
func (s *Store) planUIDAdoptions(ctx context.Context, data *ExportData, mode ImportMode,
	report *ImportReconcileReport) error {
	if mode != ImportModeMerge {
		return nil
	}
	local, err := s.loadLocalIdentities(ctx)
	if err != nil {
		return err
	}
	moved := make(map[string]string, len(report.Moved)+len(report.LocalRenumbers))
	for _, list := range [][]ImportRemapEntry{report.Moved, report.LocalRenumbers} {
		for _, m := range list {
			moved[m.OldPath] = m.NewPath
		}
	}
	at := make(map[string]localIdentity, len(local)) // the id each local node ends at
	for _, l := range local {
		final := l.id
		if newID, ok := moved[l.id]; ok {
			final = newID
		}
		at[final] = l
	}
	for i := range data.Nodes {
		f := &data.Nodes[i]
		l, held := at[f.ID]
		if f.UID == "" || !held || l.uid == f.UID {
			continue
		}
		report.UIDAdoptions = append(report.UIDAdoptions, ImportUIDAdoption{
			ID: f.ID, LocalUID: l.uid, FileUID: f.UID, LocalTitle: l.title, FileTitle: f.Title,
		})
	}
	return nil
}

// loadLocalIdentities reads the id, uid and title of every local node,
// soft-deleted ones included: a merge adopts a uid for them too.
func (s *Store) loadLocalIdentities(ctx context.Context) ([]localIdentity, error) {
	// Every local node's id, uid and title, deleted rows included.
	rows, err := s.readDB.QueryContext(ctx, `SELECT id, COALESCE(uid, ''), title FROM nodes`)
	if err != nil {
		return nil, fmt.Errorf("read the local nodes for the uid adoptions: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			s.logger.Error("failed to close uid adoption rows", "error", closeErr)
		}
	}()
	var nodes []localIdentity
	for rows.Next() {
		var n localIdentity
		if err := rows.Scan(&n.id, &n.uid, &n.title); err != nil {
			return nil, fmt.Errorf("read the local nodes for the uid adoptions: %w", err)
		}
		nodes = append(nodes, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the local nodes for the uid adoptions: %w", err)
	}
	return nodes, nil
}

// writeUIDAdoptions renders the uid adoptions of an import report
// (MTIX-95.31.6): each one with the id, both uids and both titles, and,
// when the titles of one differ, that the two may be different tasks and
// where the local one is kept.
func writeUIDAdoptions(b *strings.Builder, adoptions []ImportUIDAdoption) {
	if len(adoptions) == 0 {
		return
	}
	fmt.Fprintf(b, "  uids adopted from the file (the same task under another uid): %d\n", len(adoptions))
	titlesDiffer := false
	for _, a := range adoptions {
		local := a.LocalUID
		if local == "" {
			local = "(none)"
		}
		fmt.Fprintf(b, "    - %s local uid=%s -> file uid=%s (local %q, file %q)\n",
			a.ID, local, a.FileUID, a.LocalTitle, a.FileTitle)
		titlesDiffer = titlesDiffer || a.LocalTitle != a.FileTitle
	}
	if titlesDiffer {
		b.WriteString("  a task above whose titles differ may be two different tasks created in the same second " +
			"whose create event at least one clone's event log lacks (for example, created before 0.2): the " +
			"merge keeps the file's under the id, and the backup taken before the merge holds the local one\n")
	}
}
