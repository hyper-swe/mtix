// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// errDryRun rolls back the transaction of a dry-run import
// (countImportChanges).
var errDryRun = errors.New("dry run: roll back")

// ErrStoreChangedSinceCheck is returned by Store.Import, which then writes
// nothing, when the caller passed IfStoreUnchanged and the store changed
// after the caller checked it (MTIX-95.31.4): the automatic import compares
// the file with the store and must not replace a store that has changed
// since. Running the check again decides anew.
var ErrStoreChangedSinceCheck = errors.New("the local store changed after the import was checked")

// ImportOption adjusts what Store.Import does inside its transaction
// (MTIX-95.31.4).
type ImportOption func(*importConfig)

// importConfig collects the ImportOptions of one import.
type importConfig struct {
	// checkStore and storeChecksum: IfStoreUnchanged was passed with
	// storeChecksum.
	checkStore    bool
	storeChecksum string
	// localMoves are the local tasks to renumber before a merge
	// (renumberLocalFirst).
	localMoves []localRenumber
}

// IfStoreUnchanged makes the import write nothing, and return
// ErrStoreChangedSinceCheck, unless the store's nodes and dependencies
// still have the export checksum checksum: the Checksum of the store's
// Export the caller checked (MTIX-95.31.4). The check runs inside the
// import's transaction, which holds the write lock from its start (BEGIN
// IMMEDIATE), so no write can land between the check and the import.
func IfStoreUnchanged(checksum string) ImportOption {
	return func(c *importConfig) {
		c.checkStore, c.storeChecksum = true, checksum
	}
}

// renumberLocalFirst makes the import move the planned local tasks, inside
// its transaction and before it writes the file's nodes (MTIX-95.31.4,
// planLocalRenumbers).
func renumberLocalFirst(moves []localRenumber) ImportOption {
	return func(c *importConfig) { c.localMoves = moves }
}

// collectImportOptions applies opts, skipping nil ones.
func collectImportOptions(opts []ImportOption) importConfig {
	var cfg importConfig
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	return cfg
}

// verifyStoreUnchanged returns ErrStoreChangedSinceCheck when cfg asks for
// the check (IfStoreUnchanged) and the store's nodes and dependencies, read
// through the import's transaction tx, no longer have the checksum the
// caller checked (MTIX-95.31.4). It computes the checksum exactly as
// Export does.
func (s *Store) verifyStoreUnchanged(ctx context.Context, tx *sql.Tx, cfg importConfig) error {
	if !cfg.checkStore {
		return nil
	}
	current, err := s.storeChecksumTx(ctx, tx)
	if err != nil {
		return fmt.Errorf("re-check the store before the import: %w", err)
	}
	if current != cfg.storeChecksum {
		return fmt.Errorf("nothing was imported: %w", ErrStoreChangedSinceCheck)
	}
	return nil
}

// storeChecksumTx returns the export checksum of the store's nodes and
// dependencies, read through tx and computed exactly as Export computes it.
func (s *Store) storeChecksumTx(ctx context.Context, tx *sql.Tx) (string, error) {
	nodes, err := s.exportNodes(ctx, tx)
	if err != nil {
		return "", err
	}
	deps, err := s.exportDependencies(ctx, tx)
	if err != nil {
		return "", err
	}
	sortForChecksum(nodes, deps)
	return computeExportChecksum(nodes, deps)
}

// refuseEmptyImport rejects importing zero nodes into a non-empty database
// unless forced (FR-7.8).
func (s *Store) refuseEmptyImport(ctx context.Context, data *ExportData, force bool) error {
	if len(data.Nodes) > 0 || force {
		return nil
	}
	var existingCount int
	if countErr := s.readDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM nodes").Scan(&existingCount); countErr == nil && existingCount > 0 {
		return fmt.Errorf(
			"import contains zero nodes but database has %d \u2014 use --force to confirm: %w",
			existingCount, model.ErrInvalidInput)
	}
	return nil
}

// countImportChanges runs the import in a transaction it rolls back, and
// returns how many rows it would insert, update or delete (SQLite's
// total_changes over the transaction; MTIX-95.31.4), and the store's export
// checksum read at the start of that transaction: the real import passes it
// to IfStoreUnchanged, so a write committed between the two makes the
// import write nothing. It works on a copy of the file's node list, because
// writing a node normalizes its node_type, and the real import must still
// verify the file's checksum.
func (s *Store) countImportChanges(ctx context.Context, data *ExportData, mode ImportMode,
	moves []localRenumber) (int64, string, error) {
	dry := *data
	dry.Nodes = append([]exportNode(nil), data.Nodes...)
	var changes int64
	var checksum string
	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		var before, after int64
		var sumErr error
		if checksum, sumErr = s.storeChecksumTx(ctx, tx); sumErr != nil {
			return fmt.Errorf("read the store before the import: %w", sumErr)
		}
		// Rows this connection has changed so far.
		if err := tx.QueryRowContext(ctx, `SELECT total_changes()`).Scan(&before); err != nil {
			return fmt.Errorf("count the import's changes: %w", err)
		}
		if err := applyLocalRenumbers(ctx, tx, moves); err != nil {
			return err
		}
		var applyErr error
		if mode == ImportModeReplace {
			_, applyErr = replaceAllData(ctx, tx, &dry)
		} else {
			_, applyErr = mergeAllData(ctx, tx, &dry)
		}
		if applyErr != nil {
			return applyErr
		}
		// Rows changed by the import.
		if err := tx.QueryRowContext(ctx, `SELECT total_changes()`).Scan(&after); err != nil {
			return fmt.Errorf("count the import's changes: %w", err)
		}
		changes = after - before
		return errDryRun
	})
	if !errors.Is(err, errDryRun) {
		return 0, "", err
	}
	return changes, checksum, nil
}
