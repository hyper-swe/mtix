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
		err = advanceChildCounters(ctx, tx, newID, newID+".")
	}
	if err != nil {
		return fmt.Errorf("renumber to %s: %w", newID, err)
	}
	return nil
}

// idRange returns the bounds [lo, hi) of the ids that start with prefix
// under SQLite's binary collation (MTIX-95.38): lo is prefix, and hi is
// prefix with its last byte incremented ('P-' gives 'P.', 'P-1.' gives
// 'P-1/'). The match is exact and case-sensitive, has no wildcard, and can
// use the primary-key index on nodes.id. Ids and prefixes are ASCII (the
// id grammar), and prefix is never empty.
func idRange(prefix string) (lo, hi string) {
	last := len(prefix) - 1
	return prefix, prefix[:last] + string(rune(prefix[last]+1))
}

// advanceRootCounter raises the counter of the root namespace of prefix,
// '{prefix}:', to the highest number a root id '{prefix}-<digits>' holds,
// live or soft-deleted, counting only numbers up to maxSequence
// (MTIX-95.38). The prefix is matched exactly and case-sensitively; ids of
// children, and of another prefix that starts the same way, do not count.
func advanceRootCounter(ctx context.Context, tx *sql.Tx, prefix string) error {
	ns := prefix + "-"
	lo, hi := idRange(ns)
	// The highest number among the ids in [lo, hi) whose rest, from the
	// first digit, is all digits and at most maxSequence. It creates the
	// root counter at that number, or raises it; no row when there is none.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO sequences (key, value)
		SELECT ?, n FROM (
		  SELECT MAX(CAST(d AS INTEGER)) AS n FROM (
		    SELECT SUBSTR(id, ?) AS d FROM nodes WHERE id >= ? AND id < ?)
		   WHERE d <> '' AND d NOT GLOB '*[^0-9]*' AND CAST(d AS INTEGER) <= ?)
		 WHERE n IS NOT NULL
		ON CONFLICT(key) DO UPDATE SET value = max(value, excluded.value)`,
		sequenceKey(prefix, ""), len(ns)+1, lo, hi, maxSequence,
	); err != nil {
		return fmt.Errorf("advance the %s root counter: %w", prefix, err)
	}
	return nil
}

// advanceChildCounters raises the child-namespace counter of the node
// parentID and of every node whose id starts with subtreePrefix ("" selects
// none) to the highest number an id '<parent>.<digits>' holds, whatever its
// parent_id, counting only numbers up to maxSequence (MTIX-95.38). The key
// names the parent's project column, as a create under that parent does.
func advanceChildCounters(ctx context.Context, tx *sql.Tx, parentID, subtreePrefix string) error {
	lo, hi := "", "" // an empty range selects no node
	if subtreePrefix != "" {
		lo, hi = idRange(subtreePrefix)
	}
	// For each selected parent p, the highest number among the ids in its
	// namespace [p.id || '.', p.id || '/') whose rest is all digits, live or
	// soft-deleted; a parent with none produces no row. It creates each
	// counter at that number, or raises it.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO sequences (key, value)
		SELECT p.project || ':' || p.id, MAX(CAST(SUBSTR(c.id, LENGTH(p.id) + 2) AS INTEGER))
		  FROM nodes p JOIN nodes c ON c.id > p.id || '.' AND c.id < p.id || '/'
		 WHERE (p.id = ? OR (p.id >= ? AND p.id < ?))
		   AND SUBSTR(c.id, LENGTH(p.id) + 2) NOT GLOB '*[^0-9]*'
		   AND CAST(SUBSTR(c.id, LENGTH(p.id) + 2) AS INTEGER) <= ?
		 GROUP BY p.id
		ON CONFLICT(key) DO UPDATE SET value = max(value, excluded.value)`,
		parentID, lo, hi, maxSequence,
	); err != nil {
		return fmt.Errorf("advance child counters: %w", err)
	}
	return nil
}

// skipSQL moves a counter that fell behind past the highest number the ids
// of its namespace hold: the ids in [lo, hi) whose rest, from a given
// 1-based position, is all digits and at most maxSequence. The counter
// becomes one more than that number or than itself, whichever is higher,
// so it never drops below a number already handed out. It writes nothing,
// and returns no row, when the new value would pass maxSequence.
const skipSQL = `
	WITH taken(n) AS (
	  SELECT COALESCE(MAX(CAST(d AS INTEGER)), 0) FROM (
	    SELECT SUBSTR(id, ?) AS d FROM nodes WHERE id >= ? AND id < ?)
	   WHERE d <> '' AND d NOT GLOB '*[^0-9]*' AND CAST(d AS INTEGER) <= ?)
	UPDATE sequences SET value = max(value, (SELECT n FROM taken)) + 1
	 WHERE key = ? AND max(value, (SELECT n FROM taken)) < ?
	RETURNING value`

// skipTakenSequence checks value, the number NextSequence just took from
// the counter key, against the nodes (MTIX-95.38). When no node holds the
// id it names, value is returned unchanged. When a node, live or
// soft-deleted, holds it, the counter fell behind: in one statement under
// the write lock (skipSQL) it is moved past the highest number the ids of
// key's namespace hold, '{project}-<digits>' for a root key and
// '<parent>.<digits>' for a child key, whatever the project, seq and
// parent_id columns say, and the new value is returned. It skips once and
// does not check the new number again. When the skip would pass
// maxSequence it writes nothing and fails with an error naming the limit.
func (s *Store) skipTakenSequence(ctx context.Context, key string, value int) (int, error) {
	project, parentID, _ := strings.Cut(key, ":")
	var taken bool
	// Does a node, live or soft-deleted, hold the id the number names?
	if err := s.writeDB.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM nodes WHERE id = ?)`,
		model.BuildID(project, parentID, value)).Scan(&taken); err != nil {
		return 0, fmt.Errorf("next sequence for %s: check number %d: %w", key, value, err)
	}
	if !taken {
		return value, nil
	}
	ns := project + "-"
	if parentID != "" {
		ns = parentID + "."
	}
	lo, hi := idRange(ns)
	next := value
	err := s.writeDB.QueryRowContext(ctx, skipSQL, len(ns)+1, lo, hi, maxSequence, key, maxSequence).Scan(&next)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, sequenceLimitError(key)
	}
	if err != nil {
		return 0, s.classifyWriteError(fmt.Errorf("next sequence for %s: skip taken number %d: %w", key, value, err))
	}
	return next, nil
}
