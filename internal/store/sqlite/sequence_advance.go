// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"

	"github.com/hyper-swe/mtix/internal/model"
)

// Sequence counters that stay ahead of the nodes (MTIX-95.38).
//
// A new node's number comes from the counter of its parent key,
// '{project}:{parent_dotpath}' (FR-2.7). A counter below a number a node
// already holds under that parent makes a local create pick a taken id and
// fail with "already exists". These paths keep the counters they touch at
// or above the numbers in their namespaces, in the transaction that writes
// the ids, and never lower a counter:
//   - a local create takes its number from the counter (NextSequence);
//   - the apply of a pulled or cloned create_node (advanceAppliedSequence);
//   - a renumber: local, after a hub rejection, when a provisional id
//     settles, and a merge import's move of a local task
//     (advanceRenumberedSequences);
//   - mtix sync reconcile --rename-to and --import-as
//     (advanceRenamedCounters, advanceImportedCounters).
//
// An import rebuilds every counter from the nodes after its transaction
// (rebuildSequences), which can lower one. A counter that is behind anyway
// is corrected by NextSequence when it reaches a taken number
// (skipTakenSequence). The taken numbers are read from the ids themselves:
// reconcile leaves the project, seq and depth columns stale (MTIX-107.58).

// maxSequence is the highest number a sequence counter hands out or counts
// (MTIX-95.38): the int32 maximum. A pulled task numbered above it is
// applied but does not advance a counter, the counters ignore such numbers,
// and a namespace whose counter reaches it fails the next allocation with
// an error instead of overflowing the 64-bit counter.
const maxSequence = math.MaxInt32

// sequenceLimitError reports that key has no number left at or below
// maxSequence (MTIX-95.38).
func sequenceLimitError(key string) error {
	return fmt.Errorf("next sequence for %s: no number left, mtix numbers tasks up to %d: %w",
		key, maxSequence, model.ErrInvalidInput)
}

// advanceSequence raises the counter of the parentID key in project to at
// least seq, inside tx, and never lowers it (MTIX-95.38).
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

// advanceAppliedSequence raises the counter of the parentID key of the
// pulled or cloned create_node e to the number of its node, in the apply's
// transaction tx (MTIX-95.38). It runs whether or not the insert wrote the
// node: the number is taken either way. A number above maxSequence is not
// counted, and a warning names the node.
func advanceAppliedSequence(ctx context.Context, tx *sql.Tx, e *model.SyncEvent, parentID string) error {
	seq := deriveSeq(e.NodeID)
	if seq > maxSequence {
		slog.Default().Warn("sync apply: task number above the supported maximum; local ID counter not advanced",
			"node_id", e.NodeID, "event_id", e.EventID, "max", maxSequence)
		return nil
	}
	if err := advanceSequence(ctx, tx, e.ProjectPrefix, parentID, seq); err != nil {
		return fmt.Errorf("apply create_node %s: %w", e.EventID, err)
	}
	return nil
}

// advanceRenumberedSequences raises, inside the renumber's transaction tx,
// the counters of every namespace the move touched (MTIX-95.38): the one
// that now holds newID (the root namespace of its prefix, or the children
// of its parent) and the child namespace of newID and of every node under
// it.
func advanceRenumberedSequences(ctx context.Context, tx *sql.Tx, node renumberTarget, newID string) error {
	var err error
	if node.parentID == "" {
		err = advanceRootCounter(ctx, tx, model.ParseIDProject(newID))
	} else {
		err = advanceChildCounters(ctx, tx, node.parentID, "")
	}
	if err == nil {
		err = advanceChildCounters(ctx, tx, newID, escapeLIKEPrefix(newID)+".%")
	}
	if err != nil {
		return fmt.Errorf("renumber to %s: %w", newID, err)
	}
	return nil
}

// advanceRootCounter raises the counter of the root namespace of prefix,
// '{prefix}:', to the highest number a root id '{prefix}-<digits>' holds,
// counting only numbers up to maxSequence (MTIX-95.38). Ids of children,
// and of another prefix that starts with prefix, do not match.
func advanceRootCounter(ctx context.Context, tx *sql.Tx, prefix string) error {
	start := len(prefix) + 2 // 1-based index of the first digit
	// The highest number among the ids '{prefix}-<digits>', live or
	// soft-deleted, at most maxSequence; no row when there is none. It
	// creates the root counter at that number, or raises it.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO sequences (key, value)
		SELECT ?, n FROM (
		  SELECT MAX(CAST(SUBSTR(id, ?) AS INTEGER)) AS n FROM nodes
		   WHERE id LIKE ? ESCAPE '\' AND SUBSTR(id, ?) <> ''
		     AND SUBSTR(id, ?) NOT GLOB '*[^0-9]*' AND CAST(SUBSTR(id, ?) AS INTEGER) <= ?)
		 WHERE n IS NOT NULL
		ON CONFLICT(key) DO UPDATE SET value = max(value, excluded.value)`,
		sequenceKey(prefix, ""), start, escapeLIKEPrefix(prefix+"-")+"%", start, start, start, maxSequence,
	); err != nil {
		return fmt.Errorf("advance the %s root counter: %w", prefix, err)
	}
	return nil
}

// advanceChildCounters raises the child-namespace counter of the node
// parentID and of every node whose id matches the LIKE pattern subtreeLike
// ("" matches none) to the highest number among its children's ids, the
// digits after '<parent>.', counting only numbers up to maxSequence
// (MTIX-95.38). The key names the parent's project column, as a create
// under that parent does.
func advanceChildCounters(ctx context.Context, tx *sql.Tx, parentID, subtreeLike string) error {
	// For each selected parent p, the highest number the ids of its
	// children hold, live or soft-deleted; parents with no numbered child
	// produce no row. It creates each counter at that number, or raises it.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO sequences (key, value)
		SELECT p.project || ':' || p.id, MAX(CAST(SUBSTR(c.id, LENGTH(p.id) + 2) AS INTEGER))
		  FROM nodes p JOIN nodes c ON c.parent_id = p.id
		 WHERE (p.id = ? OR p.id LIKE ? ESCAPE '\')
		   AND SUBSTR(c.id, 1, LENGTH(p.id) + 1) = p.id || '.'
		   AND SUBSTR(c.id, LENGTH(p.id) + 2) <> ''
		   AND SUBSTR(c.id, LENGTH(p.id) + 2) NOT GLOB '*[^0-9]*'
		   AND CAST(SUBSTR(c.id, LENGTH(p.id) + 2) AS INTEGER) <= ?
		 GROUP BY p.id
		ON CONFLICT(key) DO UPDATE SET value = max(value, excluded.value)`,
		parentID, subtreeLike, maxSequence,
	); err != nil {
		return fmt.Errorf("advance child counters: %w", err)
	}
	return nil
}

// skipRootSQL moves a root counter past a taken number: when a node holds
// the id the allocated number names, the counter becomes one more than
// the highest number among the ids '{prefix}-<digits>' (at most
// maxSequence) or than itself, whichever is higher. A counter past
// maxSequence is left as it is.
const skipRootSQL = `
	UPDATE sequences
	   SET value = CASE WHEN value > ? THEN value ELSE max(value, (
	         SELECT COALESCE(MAX(CAST(SUBSTR(id, ?) AS INTEGER)), 0) FROM nodes
	          WHERE id LIKE ? ESCAPE '\' AND SUBSTR(id, ?) <> ''
	            AND SUBSTR(id, ?) NOT GLOB '*[^0-9]*' AND CAST(SUBSTR(id, ?) AS INTEGER) <= ?)) + 1 END
	 WHERE key = ? AND EXISTS (SELECT 1 FROM nodes WHERE id = ?)
	RETURNING value`

// skipChildSQL is skipRootSQL for a child namespace: the numbers are the
// digits after '<parent>.' in the ids of the parent's children.
const skipChildSQL = `
	UPDATE sequences
	   SET value = CASE WHEN value > ? THEN value ELSE max(value, (
	         SELECT COALESCE(MAX(CAST(SUBSTR(id, LENGTH(parent_id) + 2) AS INTEGER)), 0) FROM nodes
	          WHERE parent_id = ?
	            AND SUBSTR(id, 1, LENGTH(parent_id) + 1) = parent_id || '.'
	            AND SUBSTR(id, LENGTH(parent_id) + 2) <> ''
	            AND SUBSTR(id, LENGTH(parent_id) + 2) NOT GLOB '*[^0-9]*'
	            AND CAST(SUBSTR(id, LENGTH(parent_id) + 2) AS INTEGER) <= ?)) + 1 END
	 WHERE key = ? AND EXISTS (SELECT 1 FROM nodes WHERE id = ?)
	RETURNING value`

// skipTakenSequence checks value, the number NextSequence just took from
// the counter key, against the nodes (MTIX-95.38). When no node holds the
// id it names, value is returned unchanged. When a node, live or
// soft-deleted, holds it, the counter fell behind: in one statement under
// the write lock it is moved past the highest number the ids of key's
// namespace hold, and the new value is returned. The numbers come from the
// ids, never from the project or seq columns. It skips once and does not
// check the new number again. A skip past maxSequence fails with an error
// naming the limit.
func (s *Store) skipTakenSequence(ctx context.Context, key string, value int) (int, error) {
	project, parentID, _ := strings.Cut(key, ":")
	taken := model.BuildID(project, parentID, value)
	query, args := skipChildSQL, []any{maxSequence, parentID, maxSequence, key, taken}
	if parentID == "" {
		start := len(project) + 2 // 1-based index of the first digit
		query = skipRootSQL
		args = []any{maxSequence, start, escapeLIKEPrefix(project+"-") + "%", start, start, start,
			maxSequence, key, taken}
	}
	next := value
	err := s.writeDB.QueryRowContext(ctx, query, args...).Scan(&next)
	if errors.Is(err, sql.ErrNoRows) {
		return value, nil
	}
	if err != nil {
		return 0, s.classifyWriteError(fmt.Errorf("next sequence for %s: skip taken number %d: %w", key, value, err))
	}
	if next > maxSequence {
		return 0, sequenceLimitError(key)
	}
	return next, nil
}
