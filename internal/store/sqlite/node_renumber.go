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

// likeEscaper escapes the LIKE metacharacters (\, %, _) so an id prefix
// matches LITERALLY under `ESCAPE '\'`. Project prefixes may legally contain
// '_' (a LIKE single-char wildcard); without escaping, a pattern like
// 'DEP_ADD-1.%' would cross-match an unrelated same-length prefix such as
// 'DEPXADD-1.2' and corrupt it during a renumber (MTIX-33). Apply this to the
// id PREFIX only — the trailing ".%" descendant wildcard stays unescaped.
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// escapeLIKEPrefix returns s with LIKE metacharacters backslash-escaped.
func escapeLIKEPrefix(s string) string { return likeEscaper.Replace(s) }

// RenumberSubtree atomically changes node id's trailing sibling number to
// newSeq and recomputes the display path of the ENTIRE subtree — all
// descendants at any depth, recursively — within a SINGLE transaction
// (ADR-003 §5, audit finding F-2).
//
// The display path (the dot-notation node id) is a derived attribute keyed by
// the durable uid (ADR-003 §2, §3). Renumbering therefore rewrites the nodes
// table only: it updates nodes.id, every descendant's id, and every child's
// parent_id consistently, and updates the dependency rows that reference those
// ids so no foreign key dangles. Because sync events key on the node's uid
// (MTIX-30.6), this rewrite touches ZERO sync events — there is no cross-replica
// event remap (the rejected "Tier A" hazard, ADR-003 §10).
//
// Atomicity and isolation (ADR-003 §5, F-2): the whole subtree is rewritten
// inside one transaction, so no external read may observe the node moved while a
// descendant has not (or vice versa). The store runs SQLite in WAL mode, so a
// concurrent reader sees the last committed snapshot — all-old before commit,
// all-new after — never a mix. A number bound during the rename never resolves
// to two nodes because the target sibling namespace is verified free before any
// row is written.
//
// Behavior:
//   - newSeq equal to the node's current sibling number is a clean no-op.
//   - A non-positive newSeq returns model.ErrInvalidInput (numbers are 1-based,
//     ADR-003 §4); an empty id returns model.ErrInvalidInput.
//   - A missing (or soft-deleted) node returns model.ErrNotFound.
//   - A newSeq already taken by a live sibling — or whose target namespace is
//     otherwise occupied — returns model.ErrAlreadyExists and changes nothing.
func (s *Store) RenumberSubtree(ctx context.Context, id string, newSeq int) error {
	if id == "" {
		return fmt.Errorf("renumber: id is required: %w", model.ErrInvalidInput)
	}
	if newSeq <= 0 {
		return fmt.Errorf("renumber %s: target number %d must be positive: %w",
			id, newSeq, model.ErrInvalidInput)
	}

	return s.WithTx(ctx, func(tx *sql.Tx) error {
		node, err := lockNodeForRenumber(ctx, tx, id)
		if err != nil {
			return err
		}

		// Idempotent: renumbering to the current number changes nothing.
		if node.seq == newSeq {
			return nil
		}

		newID := model.BuildID(node.project, node.parentID, newSeq)
		if err := assertTargetNamespaceFree(ctx, tx, newID); err != nil {
			return err
		}

		// Defer FK enforcement to commit time so the parent's id can change
		// before its children's parent_id are rewritten within this single
		// statement set, without tripping the parent_id -> nodes(id) FK
		// mid-transaction. defer_foreign_keys resets automatically at commit,
		// where all constraints are re-checked (FK-safe per ADR-003 §5).
		if _, err := tx.ExecContext(ctx, `PRAGMA defer_foreign_keys = ON`); err != nil {
			return fmt.Errorf("renumber %s: enable deferred FK: %w", id, err)
		}

		if err := rewriteSubtreeIDs(ctx, tx, id, newID, newSeq); err != nil {
			return err
		}
		return rewriteDependencyRefs(ctx, tx, id, newID)
	})
}

// renumberTarget holds the columns of the node being renumbered.
type renumberTarget struct {
	project, parentID string
	seq               int
}

// lockNodeForRenumber reads the renumber target inside the transaction.
// Returns model.ErrNotFound if no live node carries the id.
func lockNodeForRenumber(ctx context.Context, tx *sql.Tx, id string) (renumberTarget, error) {
	var n renumberTarget
	var parent sql.NullString
	err := tx.QueryRowContext(ctx,
		`SELECT project, parent_id, seq FROM nodes WHERE id = ? AND deleted_at IS NULL`,
		id).Scan(&n.project, &parent, &n.seq)
	if err == sql.ErrNoRows {
		return n, fmt.Errorf("renumber %s: %w", id, model.ErrNotFound)
	}
	if err != nil {
		return n, fmt.Errorf("renumber %s: load node: %w", id, err)
	}
	n.parentID = parent.String
	return n, nil
}

// assertTargetNamespaceFree rejects the renumber if newID — or any path under
// newID — is already occupied by another node (live or soft-deleted). This is
// the guarantee that a number never binds to two nodes (ADR-003 §5): the entire
// destination namespace must be empty before any row moves. Soft-deleted rows
// count because they still hold the primary-key id.
func assertTargetNamespaceFree(ctx context.Context, tx *sql.Tx, newID string) error {
	var existing string
	err := tx.QueryRowContext(ctx,
		`SELECT id FROM nodes WHERE id = ? OR id LIKE ? ESCAPE '\' LIMIT 1`,
		newID, escapeLIKEPrefix(newID)+".%").Scan(&existing)
	if err == nil {
		return fmt.Errorf("renumber target %s is already taken by %s: %w",
			newID, existing, model.ErrAlreadyExists)
	}
	if err != sql.ErrNoRows {
		return fmt.Errorf("renumber: check target %s: %w", newID, err)
	}
	return nil
}

// rewriteSubtreeIDs rewrites the id (and seq for the root of the move) and every
// descendant's id and parent_id in a single statement set. The old id is a
// prefix of every descendant id and of every descendant parent_id, so the
// rewrite is a deterministic prefix substitution (oldID -> newID) that preserves
// each descendant's relative suffix, depth, and seq (ADR-003 §5).
func rewriteSubtreeIDs(ctx context.Context, tx *sql.Tx, oldID, newID string, newSeq int) error {
	// Descendants: id and parent_id both begin with oldID + "." so substituting
	// the oldID prefix with newID rewrites both consistently. SUBSTR is 1-based.
	descLike := escapeLIKEPrefix(oldID) + ".%"
	prefixLen := len(oldID)
	if _, err := tx.ExecContext(ctx, `
		UPDATE nodes
		   SET id        = ? || SUBSTR(id, ?),
		       parent_id = ? || SUBSTR(parent_id, ?)
		 WHERE id LIKE ? ESCAPE '\'`,
		newID, prefixLen+1, newID, prefixLen+1, descLike,
	); err != nil {
		return fmt.Errorf("renumber: rewrite descendants of %s: %w", oldID, err)
	}

	// The renumbered node itself: id and seq change; parent_id is unchanged.
	if _, err := tx.ExecContext(ctx,
		`UPDATE nodes SET id = ?, seq = ? WHERE id = ?`, newID, newSeq, oldID,
	); err != nil {
		return fmt.Errorf("renumber: rewrite node %s: %w", oldID, err)
	}
	return nil
}

// rewriteDependencyRefs updates dependency endpoints that point into the moved
// subtree so no FK reference dangles (ADR-003 §5, FK-safe). Each from_id/to_id
// that equals oldID or sits under it is rewritten by the same oldID -> newID
// prefix substitution used for the nodes table.
func rewriteDependencyRefs(ctx context.Context, tx *sql.Tx, oldID, newID string) error {
	prefixLen := len(oldID)
	like := escapeLIKEPrefix(oldID) + ".%"
	for _, col := range []string{"from_id", "to_id"} {
		// Column names are package-internal constants, not user input, so the
		// fmt.Sprintf only interpolates a fixed identifier; all values are
		// bound parameters (SQL Rule #1).
		stmt := fmt.Sprintf(`
			UPDATE dependencies
			   SET %[1]s = ? || SUBSTR(%[1]s, ?)
			 WHERE %[1]s = ? OR %[1]s LIKE ? ESCAPE '\'`, col)
		if _, err := tx.ExecContext(ctx, stmt,
			newID, prefixLen+1, oldID, like,
		); err != nil {
			return fmt.Errorf("renumber: rewrite dependency %s of %s: %w", col, oldID, err)
		}
	}
	return nil
}
