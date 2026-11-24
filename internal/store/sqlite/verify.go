// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"math"
)

// VerifyResult holds the result of a diagnostic verification per FR-6.3.
type VerifyResult struct {
	IntegrityOK  bool     `json:"integrity_ok"`
	ForeignKeyOK bool     `json:"foreign_key_ok"`
	SequenceOK   bool     `json:"sequence_ok"`
	ProgressOK   bool     `json:"progress_ok"`
	FTSOK        bool     `json:"fts_ok"`
	UIDUniqueOK  bool     `json:"uid_unique_ok"`
	AllPassed    bool     `json:"all_passed"`
	Errors       []string `json:"errors,omitempty"`
}

// Verify runs the full diagnostic suite per FR-6.3.
// Checks: PRAGMA integrity_check, PRAGMA foreign_key_check,
// sequence consistency, progress consistency, and FTS consistency.
func (s *Store) Verify(ctx context.Context) (*VerifyResult, error) {
	result := &VerifyResult{}

	// 1. PRAGMA integrity_check — verifies B-tree structure and page format.
	if err := s.verifyIntegrity(ctx, result); err != nil {
		return nil, fmt.Errorf("integrity check: %w", err)
	}

	// 2. PRAGMA foreign_key_check — verifies all FK constraints.
	if err := s.verifyForeignKeys(ctx, result); err != nil {
		return nil, fmt.Errorf("foreign key check: %w", err)
	}

	// 3. Sequence consistency — max(seq) per parent matches sequences table.
	if err := s.verifySequences(ctx, result); err != nil {
		return nil, fmt.Errorf("sequence check: %w", err)
	}

	// 4. Progress consistency — parent progress matches weighted children.
	if err := s.verifyProgress(ctx, result); err != nil {
		return nil, fmt.Errorf("progress check: %w", err)
	}

	// 5. FTS consistency — FTS5 integrity check.
	if err := s.verifyFTS(ctx, result); err != nil {
		return nil, fmt.Errorf("FTS check: %w", err)
	}

	// 6. No two nodes share a non-empty uid (MTIX-95.31.8).
	if err := s.verifyUIDUnique(ctx, result); err != nil {
		return nil, fmt.Errorf("uid uniqueness check: %w", err)
	}

	result.AllPassed = result.IntegrityOK && result.ForeignKeyOK &&
		result.SequenceOK && result.ProgressOK && result.FTSOK && result.UIDUniqueOK

	return result, nil
}

// verifyIntegrity runs PRAGMA integrity_check.
func (s *Store) verifyIntegrity(ctx context.Context, result *VerifyResult) error {
	var check string
	if err := s.readDB.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&check); err != nil {
		return fmt.Errorf("run integrity_check: %w", err)
	}

	result.IntegrityOK = check == "ok"
	if !result.IntegrityOK {
		result.Errors = append(result.Errors, "integrity_check: "+check)
	}
	return nil
}

// verifyForeignKeys runs PRAGMA foreign_key_check.
func (s *Store) verifyForeignKeys(ctx context.Context, result *VerifyResult) error {
	rows, err := s.readDB.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return fmt.Errorf("run foreign_key_check: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			s.logger.Error("failed to close FK check rows", "error", closeErr)
		}
	}()

	var violations int
	for rows.Next() {
		var table, rowid, parent, fkid string
		if err := rows.Scan(&table, &rowid, &parent, &fkid); err != nil {
			return fmt.Errorf("scan FK violation: %w", err)
		}
		violations++
		result.Errors = append(result.Errors,
			fmt.Sprintf("foreign_key_check: table=%s rowid=%s parent=%s", table, rowid, parent))
	}

	result.ForeignKeyOK = violations == 0
	return rows.Err()
}

// verifySequences checks that sequence counters match max(seq) per parent.
func (s *Store) verifySequences(ctx context.Context, result *VerifyResult) error {
	rows, err := s.readDB.QueryContext(ctx,
		`SELECT project || ':' || COALESCE(parent_id, '') AS key, MAX(seq) AS max_seq
		 FROM nodes WHERE deleted_at IS NULL
		 GROUP BY project, COALESCE(parent_id, '')`)
	if err != nil {
		return fmt.Errorf("query node sequences: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			s.logger.Error("failed to close seq check rows", "error", closeErr)
		}
	}()

	result.SequenceOK = true
	for rows.Next() {
		var key string
		var maxSeq int
		if err := rows.Scan(&key, &maxSeq); err != nil {
			return fmt.Errorf("scan sequence: %w", err)
		}

		// Check against stored sequence value.
		var storedSeq sql.NullInt64
		err := s.readDB.QueryRowContext(ctx,
			"SELECT value FROM sequences WHERE key = ?", key,
		).Scan(&storedSeq)

		if err == sql.ErrNoRows || !storedSeq.Valid {
			result.SequenceOK = false
			result.Errors = append(result.Errors,
				fmt.Sprintf("sequence_check: missing sequence for key %s (max_seq=%d)", key, maxSeq))
			continue
		}
		if err != nil {
			return fmt.Errorf("query sequence %s: %w", key, err)
		}

		if int(storedSeq.Int64) < maxSeq {
			result.SequenceOK = false
			result.Errors = append(result.Errors,
				fmt.Sprintf("sequence_check: key %s stored=%d < max_seq=%d",
					key, storedSeq.Int64, maxSeq))
		}
	}

	return rows.Err()
}

// storedProgress is a parent id with its stored progress.
type storedProgress struct {
	id string
	p  float64
}

// listProgressParents returns live parents that have at least one included child.
func (s *Store) listProgressParents(ctx context.Context) (parents []storedProgress, err error) {
	rows, err := s.readDB.QueryContext(ctx,
		`SELECT p.id, p.progress FROM nodes p
		 WHERE p.deleted_at IS NULL
		   AND EXISTS (SELECT 1 FROM nodes c WHERE c.parent_id = p.id AND c.deleted_at IS NULL
		               AND c.status NOT IN ('cancelled', 'invalidated'))`)
	if err != nil {
		return nil, fmt.Errorf("query progress consistency: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close progress rows: %w", closeErr)
		}
	}()
	for rows.Next() {
		var st storedProgress
		if scanErr := rows.Scan(&st.id, &st.p); scanErr != nil {
			return nil, fmt.Errorf("scan progress: %w", scanErr)
		}
		parents = append(parents, st)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, fmt.Errorf("iterate progress: %w", rowsErr)
	}
	return parents, nil
}

// verifyProgress checks that each parent's stored progress matches the rollup
// definition (computeNodeProgress: FR-5.3 count, FR-5.8 when weighted), the
// same computation the write path uses.
func (s *Store) verifyProgress(ctx context.Context, result *VerifyResult) error {
	parents, err := s.listProgressParents(ctx)
	if err != nil {
		return err
	}

	result.ProgressOK = true
	weighted := s.progressWeightedOn()
	for _, st := range parents {
		calc, err := computeNodeProgress(ctx, s.readDB, weighted, st.id)
		if err != nil {
			return err
		}
		if math.Abs(st.p-calc) > 0.01 {
			result.ProgressOK = false
			result.Errors = append(result.Errors,
				fmt.Sprintf("progress_check: node %s stored=%.4f calculated=%.4f",
					st.id, st.p, calc))
		}
	}
	return nil
}

// verifyFTS checks FTS5 index consistency via integrity-check command.
// The FTS5 integrity-check command succeeds silently when the index is
// consistent, and returns an error if corruption is detected.
func (s *Store) verifyFTS(ctx context.Context, result *VerifyResult) error {
	// FTS5 integrity-check: executed via the write DB since it's a special command.
	// If the index is consistent, this succeeds without error.
	// If corrupt, it returns an error describing the inconsistency.
	_, err := s.writeDB.ExecContext(ctx,
		"INSERT INTO nodes_fts(nodes_fts, rank) VALUES('integrity-check', 1)")
	if err != nil {
		result.FTSOK = false
		result.Errors = append(result.Errors,
			"fts_check: integrity-check failed: "+err.Error())
		return nil
	}

	result.FTSOK = true
	return nil
}
