// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Creation errors must remain observable and leave no committed partial schema.
func TestInitializeSchema_ClosedWriter_ReturnsError(t *testing.T) {
	s := newInternalTestStore(t)
	require.NoError(t, s.writeDB.Close())
	assert.Error(t, s.initializeSchema(t.Context()))
}

func TestPopulateSchema_CanceledRead_ReturnsCause(t *testing.T) {
	db, err := openDB(t.Context(), filepath.Join(t.TempDir(), "store.db"), true, false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	tx, err := db.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	s := &Store{writeDB: db, logger: slog.Default()}
	err = s.populateSchema(ctx, tx, uuid.NewRandom)
	assert.ErrorIs(t, err, context.Canceled)
	require.NoError(t, tx.Rollback())
}

func TestPopulateSchema_RandomFailure_RollsBackSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	db, err := openDB(t.Context(), path, true, false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	tx, err := db.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	cause := errors.New("random unavailable")
	s := &Store{writeDB: db, logger: slog.Default()}
	err = s.populateSchema(t.Context(), tx, func() (uuid.UUID, error) { return uuid.Nil, cause })
	assert.ErrorIs(t, err, cause)
	require.NoError(t, tx.Rollback())
	var count int
	require.NoError(t, db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table'").Scan(&count))
	assert.Zero(t, count)
}

func TestPopulateSchema_IncompatibleExistingTable_ReturnsError(t *testing.T) {
	db, err := openDB(t.Context(), filepath.Join(t.TempDir(), "store.db"), true, false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	_, err = db.Exec("CREATE TABLE nodes(unrelated TEXT)")
	require.NoError(t, err)
	tx, err := db.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	s := &Store{writeDB: db, logger: slog.Default()}
	assert.Error(t, s.populateSchema(t.Context(), tx, uuid.NewRandom))
	require.NoError(t, tx.Rollback())
}

func TestAttachMetadata_MissingAndClosedStore_DistinguishesErrors(t *testing.T) {
	s := newInternalTestStore(t)
	_, err := s.writeDB.Exec("DELETE FROM meta WHERE key='min_dispatch_protocol'")
	require.NoError(t, err)
	value, err := s.MinDispatchProtocol(t.Context())
	require.NoError(t, err)
	assert.Zero(t, value)
	_, err = s.writeDB.Exec("DELETE FROM meta WHERE key='store_instance_id'")
	require.NoError(t, err)
	id, err := s.StoreInstanceID(t.Context())
	require.NoError(t, err)
	assert.Empty(t, id)
	require.NoError(t, s.Close())
	_, err = s.StoreInstanceID(t.Context())
	assert.Error(t, err)
	_, err = s.MinDispatchProtocol(t.Context())
	assert.Error(t, err)
	assert.Error(t, s.SetMinDispatchProtocol(t.Context(), 2))
}

func TestPopulateSchema_IdentityWriteRefused_ReturnsError(t *testing.T) {
	db, err := openDB(t.Context(), filepath.Join(t.TempDir(), "store.db"), true, false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	tx, err := db.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	s := &Store{writeDB: db, logger: slog.Default()}
	err = s.populateSchema(t.Context(), tx, func() (uuid.UUID, error) {
		_, err := tx.Exec("CREATE TRIGGER refuse_identity BEFORE INSERT ON meta WHEN NEW.key='store_instance_id' BEGIN SELECT RAISE(ABORT,'identity write refused'); END")
		require.NoError(t, err)
		return uuid.NewRandom()
	})
	assert.ErrorContains(t, err, "identity write refused")
	require.NoError(t, tx.Rollback())
}

func TestSetMinDispatchProtocol_WriteRefused_PreservesIdentity(t *testing.T) {
	s := newInternalTestStore(t)
	before, err := s.StoreInstanceID(t.Context())
	require.NoError(t, err)
	_, err = s.writeDB.Exec("CREATE TRIGGER refuse_protocol BEFORE INSERT ON meta WHEN NEW.key='min_dispatch_protocol' BEGIN SELECT RAISE(ABORT,'protocol write refused'); END")
	require.NoError(t, err)
	assert.ErrorContains(t, s.SetMinDispatchProtocol(t.Context(), 2), "protocol write refused")
	after, err := s.StoreInstanceID(t.Context())
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

func TestCreateSchema_ClosedWriter_ReturnsError(t *testing.T) {
	s := newInternalTestStore(t)
	require.NoError(t, s.writeDB.Close())
	assert.Error(t, s.createSchema(t.Context()))
}

func TestSchemaTransaction_CallbackFailure_RollsBack(t *testing.T) {
	s := newInternalTestStore(t)
	cause := errors.New("operation refused")
	err := s.withSchemaTx(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.Exec("CREATE TABLE initialization_probe(value TEXT)")
		require.NoError(t, err)
		return cause
	})
	assert.ErrorIs(t, err, cause)
	var count int
	require.NoError(t, s.readDB.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='initialization_probe'").Scan(&count))
	assert.Zero(t, count)
}
func TestSchemaTransaction_CommitFailure_ReturnsCause(t *testing.T) {
	s := newInternalTestStore(t)
	err := s.withSchemaTx(t.Context(), func(tx *sql.Tx) error { require.NoError(t, tx.Rollback()); return nil })
	assert.ErrorIs(t, err, sql.ErrTxDone)
}
func TestSchemaTransaction_RollbackFailure_PreservesBothErrors(t *testing.T) {
	s := newInternalTestStore(t)
	cause := errors.New("operation refused")
	err := s.withSchemaTx(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.Exec("ROLLBACK")
		require.NoError(t, err)
		return cause
	})
	assert.ErrorIs(t, err, cause)
	assert.ErrorContains(t, err, "rollback schema transaction")
}
