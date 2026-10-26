// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// initializeSchema serializes schema detection with creation, including across processes.
// The writer uses BEGIN IMMEDIATE so detection and creation share the same lock.
func (s *Store) initializeSchema(ctx context.Context) error {
	return s.withSchemaTx(ctx, func(tx *sql.Tx) error { return s.populateSchema(ctx, tx, uuid.NewRandom) })
}

// withSchemaTx retains initialization on low-space stores while ordinary writes remain guarded.
func (s *Store) withSchemaTx(ctx context.Context, fn func(*sql.Tx) error) (err error) {
	wtx, err := s.beginWriterTx(ctx) // pinned until Release: MTIX-198
	if err != nil {
		return fmt.Errorf("begin schema transaction: %w", err)
	}
	defer wtx.Release(ctx)
	tx := wtx.Tx
	defer func() {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			s.logger.Error("schema rollback failed", "error", rbErr)
			err = errors.Join(err, fmt.Errorf("rollback schema transaction: %w", rbErr))
		}
	}()
	if callbackErr := fn(tx); callbackErr != nil {
		return callbackErr
	}
	if err = wtx.Commit(); err != nil {
		return wtx.RecoverAfterCommit(ctx, fmt.Errorf("commit schema transaction: %w", err)) // MTIX-198
	}
	return nil
}

// populateSchema assigns identity only when no application table existed before creation.
func (s *Store) populateSchema(ctx context.Context, conn *sql.Tx, randomID func() (uuid.UUID, error)) error {
	var existing int
	if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name NOT GLOB 'sqlite_*'").Scan(&existing); err != nil {
		return fmt.Errorf("inspect existing schema: %w", err)
	}
	if _, err := conn.ExecContext(ctx, schemaSQL+quarantineAcknowledgementDDL); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}
	if existing != 0 {
		return nil
	}
	id, err := randomID()
	if err != nil {
		return fmt.Errorf("generate store instance id: %w", err)
	}
	if _, err = conn.ExecContext(ctx, "INSERT INTO meta(key,value) VALUES('store_instance_id',?)", id.String()); err != nil {
		return fmt.Errorf("write store instance id: %w", err)
	}
	return nil
}
