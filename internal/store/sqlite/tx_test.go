// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWithTx_Success_Commits verifies successful transactions are committed.
func TestWithTx_Success_Commits(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Use WithTx to insert a meta value.
	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(
			"INSERT OR REPLACE INTO meta (key, value) VALUES (?, ?)",
			"test_key", "test_value",
		)
		return err
	})
	require.NoError(t, err)

	// Verify the value was committed by reading from the store.
	var value string
	err = s.QueryRow(ctx,
		"SELECT value FROM meta WHERE key = ?", "test_key",
	).Scan(&value)
	require.NoError(t, err)
	assert.Equal(t, "test_value", value)
}

// TestWithTx_Error_Rollbacks verifies failed transactions are rolled back.
func TestWithTx_Error_Rollbacks(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	testErr := errors.New("intentional error")

	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(
			"INSERT OR REPLACE INTO meta (key, value) VALUES (?, ?)",
			"rollback_key", "should_not_exist",
		)
		require.NoError(t, err)
		return testErr // Return error to trigger rollback.
	})
	require.ErrorIs(t, err, testErr)

	// Verify the value was NOT committed.
	var value string
	err = s.QueryRow(ctx,
		"SELECT value FROM meta WHERE key = ?", "rollback_key",
	).Scan(&value)
	assert.ErrorIs(t, err, sql.ErrNoRows, "rolled back value should not exist")
}

// TestWithTx_Panic_RollbacksAndRePanics verifies panics trigger rollback.
func TestWithTx_Panic_RollbacksAndRePanics(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	assert.Panics(t, func() {
		_ = s.WithTx(ctx, func(tx *sql.Tx) error {
			_, err := tx.Exec(
				"INSERT OR REPLACE INTO meta (key, value) VALUES (?, ?)",
				"panic_key", "should_not_exist",
			)
			require.NoError(t, err)
			panic("intentional panic")
		})
	})

	// Verify the value was NOT committed.
	var value string
	err := s.QueryRow(ctx,
		"SELECT value FROM meta WHERE key = ?", "panic_key",
	).Scan(&value)
	assert.ErrorIs(t, err, sql.ErrNoRows, "panicked transaction should be rolled back")
}

// TestWithTx_CallbackAndRollbackFail_PreservesBothErrors verifies that the
// callback error and the later rollback error both remain discoverable.
func TestWithTx_CallbackAndRollbackFail_PreservesBothErrors(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	callbackErr := errors.New("callback failed")

	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		// End the real transaction so WithTx's rollback returns ErrTxDone.
		require.NoError(t, tx.Rollback())
		return callbackErr
	})

	assert.ErrorIs(t, err, callbackErr, "the callback error must remain in the chain")
	assert.ErrorIs(t, err, sql.ErrTxDone, "the rollback error must remain in the chain")
	require.NoError(t, s.WithTx(ctx, func(*sql.Tx) error { return nil }),
		"the writer must remain usable after both errors")
}
