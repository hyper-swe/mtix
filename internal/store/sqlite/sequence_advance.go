// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/hyper-swe/mtix/internal/model"
)

// Sequence counters that stay ahead of the nodes (MTIX-95.38).
//
// A new node's number comes from the counter of its parent key,
// '{project}:{parent_dotpath}' (FR-2.7). A counter below a number a node
// already holds under that parent makes a local create pick a taken id and
// fail with "already exists". Every path that writes a node's number keeps
// its counter at or above it: a local create takes the number from the
// counter (NextSequence), an applied create_node advances it
// (advanceSequence), a renumber advances every counter it touches
// (advanceRenumberedSequences), and an import rebuilds them
// (rebuildSequences). A counter that is behind anyway, for example after an
// import that stopped before its rebuild, is corrected by NextSequence when
// it reaches a taken number (skipTakenSequence).

// advanceSequence raises the counter of the parentID key in project to at
// least seq, inside tx, and never lowers it (MTIX-95.38). The apply of a
// pulled or cloned create_node calls it in its own transaction, so the
// counter commits or rolls back with the node.
func advanceSequence(ctx context.Context, tx *sql.Tx, project, parentID string, seq int) error {
	key := sequenceKey(project, parentID)
	// Create the parent's counter at seq, or raise it to seq; a counter
	// already above seq keeps its value.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO sequences (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = max(value, excluded.value)`,
		key, seq,
	); err != nil {
		return fmt.Errorf("advance sequence %s to %d: %w", key, seq, err)
	}
	return nil
}

// advanceRenumberedSequences raises, inside the renumber's transaction tx,
// the counters of every parent a renumber touched to at least the highest
// number a node holds under it (MTIX-95.38): the parent of the moved node,
// whose children now include the new number, and every parent inside the
// moved subtree now under newID, whose ids are new. Soft-deleted nodes
// count, as they keep their ids. No counter is lowered.
func advanceRenumberedSequences(ctx context.Context, tx *sql.Tx, node renumberTarget, newID string) error {
	// For each parent key among the moved node's siblings (itself
	// included) and the moved subtree's nodes, raise the counter to the
	// highest number under that parent.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO sequences (key, value)
		SELECT project || ':' || COALESCE(parent_id, ''), MAX(seq)
		  FROM nodes
		 WHERE (project = ? AND parent_id IS ?) OR id LIKE ? ESCAPE '\'
		 GROUP BY project, COALESCE(parent_id, '')
		ON CONFLICT(key) DO UPDATE SET value = max(value, excluded.value)`,
		node.project, nullableString(node.parentID), escapeLIKEPrefix(newID)+".%",
	); err != nil {
		return fmt.Errorf("renumber to %s: advance sequences: %w", newID, err)
	}
	return nil
}

// skipTakenSequence checks value, the number NextSequence just took from
// the counter key, against the nodes (MTIX-95.38). When no node holds the
// id it names, value is returned unchanged. When a node, live or
// soft-deleted, holds it, the counter fell behind: it is moved past the
// highest number under key's parent, in one statement under the write
// lock, and the new value is returned. It skips once and does not check
// the new number again; a store whose seq column disagrees with its ids
// can still fail the insert with ErrAlreadyExists, and a retry then takes
// the next number.
func (s *Store) skipTakenSequence(ctx context.Context, key string, value int) (int, error) {
	project, parentID, _ := strings.Cut(key, ":")
	next := value
	// When a node holds the id the number names: raise the counter to
	// the highest number any node holds under the parent (or keep it, if
	// already higher), add one, and return it.
	err := s.writeDB.QueryRowContext(ctx, `
		UPDATE sequences
		   SET value = max(value, (SELECT COALESCE(MAX(seq), 0) FROM nodes
		                           WHERE project = ? AND parent_id IS ?)) + 1
		 WHERE key = ? AND EXISTS (SELECT 1 FROM nodes WHERE id = ?)
		RETURNING value`,
		project, nullableString(parentID), key, model.BuildID(project, parentID, value),
	).Scan(&next)
	if errors.Is(err, sql.ErrNoRows) {
		return value, nil
	}
	if err != nil {
		return 0, s.classifyWriteError(fmt.Errorf("next sequence for %s: skip taken number %d: %w", key, value, err))
	}
	return next, nil
}
