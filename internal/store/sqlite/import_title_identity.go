// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/hyper-swe/mtix/internal/model"
)

// ImportTitleMismatch is a local task that a merge renumbers because the
// file holds its id with another title while one of the two has no uid to
// compare (MTIX-95.31.9, FR-7.8).
type ImportTitleMismatch struct {
	// ID is the id both tasks hold.
	ID string `json:"id"`
	// LocalTitle is the local task's title.
	LocalTitle string `json:"local_title"`
	// FileTitle is the title of the file's task.
	FileTitle string `json:"file_title"`
}

// differentTitleNoUID reports whether the local node and the file's node
// under the same id are different tasks by the rule for a pair with no uid
// to compare (MTIX-95.31.9, FR-7.8): one of them has no uid (a board
// written before uids were shared, or a local task imported from one), so
// the titles decide, and different titles are different tasks. Two nodes
// that both carry a uid are told apart by differentIdentity instead.
func differentTitleNoUID(local, in *exportNode) bool {
	return (local.UID == "" || in.UID == "") && local.Title != in.Title
}

// differentTaskAt reports whether the file's node f, at local node l's
// final id, is a different task (MTIX-95.31.4, MTIX-95.31.9): by the
// titles when either has no uid (loadTitlesIfUIDless read l's title), and
// by differentIdentity otherwise.
func (p *localMovePlan) differentTaskAt(l *localNode, f *exportNode) bool {
	if l.uid == "" || f.UID == "" {
		return l.title != f.Title
	}
	return differentIdentity(taskIdentity{l.uid, l.createdAt}, taskIdentity{f.UID, f.CreatedAt})
}

// noteTitleMismatch records in the report a local task that renumber moves
// off the id of file node f because they have different titles and no uid
// to compare (MTIX-95.31.9).
func (p *localMovePlan) noteTitleMismatch(l *localNode, f *exportNode) {
	if l.uid != "" && f.UID != "" {
		return
	}
	p.report.TitleMismatches = append(p.report.TitleMismatches,
		ImportTitleMismatch{ID: l.final, LocalTitle: l.title, FileTitle: f.Title})
}

// loadTitlesIfUIDless reads the title of every local node when a local
// node or a file node has no uid, the only case the plan compares titles
// in (differentTaskAt, MTIX-95.31.9). A merge between a store and a board
// that both carry every uid reads no title.
func (s *Store) loadTitlesIfUIDless(ctx context.Context, p *localMovePlan) error {
	uidless := false
	for _, l := range p.nodes {
		uidless = uidless || l.uid == ""
	}
	for i := range p.data.Nodes {
		uidless = uidless || p.data.Nodes[i].UID == ""
	}
	if !uidless {
		return nil
	}
	// Every local node's title, deleted rows included.
	rows, err := s.readDB.QueryContext(ctx, `SELECT id, title FROM nodes`)
	if err != nil {
		return fmt.Errorf("read the local titles for the merge plan: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			s.logger.Error("failed to close merge plan title rows", "error", closeErr)
		}
	}()
	for rows.Next() {
		var id, title string
		if err := rows.Scan(&id, &title); err != nil {
			return fmt.Errorf("read the local titles for the merge plan: %w", err)
		}
		if l := p.byID[id]; l != nil {
			l.title = title
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read the local titles for the merge plan: %w", err)
	}
	return nil
}

// stampMissingUIDs gives every local node whose own number the merge
// changes and that has no uid a backfill uid, as BackfillUIDs gives one to
// a node without a create event (MTIX-95.31.9): the renumber finds each
// node by its uid. The write sets it first (stampNewUIDs); the report shows
// it as uid=(new), since a later run mints another.
func (p *localMovePlan) stampMissingUIDs() error {
	for _, l := range p.nodes {
		if l.uid != "" || l.finalSeq == l.seq {
			continue
		}
		uid, err := model.NewBackfillUID()
		if err != nil {
			return fmt.Errorf("mint a uid for local task %s before it is renumbered: %w", l.id, err)
		}
		l.uid, l.stamp = uid, true
	}
	return nil
}

// stampNewUIDs writes, inside the import's transaction tx, the uid the
// plan minted for each local node to renumber that had none
// (stampMissingUIDs, MTIX-95.31.9). A node that no longer sits at its
// planned id without a uid means the store changed after the plan
// (ErrConflict), and the transaction writes nothing.
func stampNewUIDs(ctx context.Context, tx *sql.Tx, moves []localRenumber) error {
	for _, m := range moves {
		if !m.stamp {
			continue
		}
		// Give the node its minted uid, only while it still has none.
		res, err := tx.ExecContext(ctx,
			`UPDATE nodes SET uid = ? WHERE id = ? AND COALESCE(uid, '') = ''`, m.uid, m.oldID)
		if err != nil {
			return fmt.Errorf("give local task %s a uid: %w", m.oldID, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("give local task %s a uid: %w", m.oldID, err)
		}
		if n != 1 {
			return fmt.Errorf("give local task %s a uid: it is no longer there without one; the store changed "+
				"after the import was planned, run the import again: %w", m.oldID, model.ErrConflict)
		}
	}
	return nil
}

// refuseTitleMismatch returns ErrConflict when a merge would overwrite the
// local node with the file's node under the same id although they have
// different titles and no uid to compare (MTIX-95.31.9). ImportReconcile
// renumbers such a local task first, so a merge it runs never reaches this.
func refuseTitleMismatch(local, in *exportNode) error {
	if !differentTitleNoUID(local, in) {
		return nil
	}
	return fmt.Errorf("merge node %s: the file holds a task with another title under this id (%q, local %q) and "+
		"no uid to compare; mtix import renumbers the local task first: %w", in.ID, in.Title, local.Title,
		model.ErrConflict)
}

// shownUID is how a report shows the uid of a renumbered local task
// (MTIX-95.31.9): uid=(new) for one this import minted, which a later run
// mints again.
func shownUID(m ImportRemapEntry) string {
	if m.NewUID {
		return "(new)"
	}
	return m.UID
}

// writeTitleMismatches renders, after the renumbered local tasks, those
// renumbered because the file holds their id with another title and no uid
// to compare, with both titles (MTIX-95.31.9).
func writeTitleMismatches(b *strings.Builder, mismatches []ImportTitleMismatch) {
	if len(mismatches) == 0 {
		return
	}
	fmt.Fprintf(b, "  of these, a task under this id with a different title and no uid to compare: %d\n",
		len(mismatches))
	for _, m := range mismatches {
		fmt.Fprintf(b, "    - %s (local %q, file %q)\n", m.ID, m.LocalTitle, m.FileTitle)
	}
}
