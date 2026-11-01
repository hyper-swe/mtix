// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// writerRecoveryTimeout bounds the post-commit-failure recovery, which runs
// on a context detached from the caller's cancellation (MTIX-198).
const writerRecoveryTimeout = 30 * time.Second

// writerConnPragmas are applied by the driver to EVERY connection the write
// pool opens, so a connection that replaces a discarded one (MTIX-198) has the
// same posture as the first: foreign keys on, busy_timeout 5000, synchronous
// FULL (2) and the pinned autocheckpoint (NFR-2.1, NFR-2.8, SQL rule 2).
const writerConnPragmas = "&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)" +
	"&_pragma=synchronous(2)&_pragma=wal_autocheckpoint(1000)"

// RollbackWriterFunc rolls back whatever transaction is open on the pinned
// writer connection. Store.rollbackWriter is the test seam for it.
type RollbackWriterFunc func(ctx context.Context, conn *sql.Conn) error

// isNoTransaction reports SQLite's answer to a ROLLBACK with nothing to roll
// back, which means the connection is already clean.
func isNoTransaction(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no transaction is active")
}

// defaultRollbackWriter issues ROLLBACK, treating "no transaction is active"
// as success.
func defaultRollbackWriter(ctx context.Context, conn *sql.Conn) error {
	if _, err := conn.ExecContext(ctx, "ROLLBACK"); err != nil && !isNoTransaction(err) {
		return fmt.Errorf("rollback writer connection: %w", err)
	}
	return nil
}

// discardConn closes the pinned connection as bad, so database/sql removes it
// from the pool and opens a fresh one on the next use.
func discardConn(conn *sql.Conn) error {
	if err := conn.Raw(func(any) error { return driver.ErrBadConn }); err != nil && !errors.Is(err, driver.ErrBadConn) {
		return fmt.Errorf("discard writer connection: %w", err)
	}
	if err := conn.Close(); err != nil && !errors.Is(err, sql.ErrConnDone) {
		return fmt.Errorf("close discarded writer connection: %w", err)
	}
	return nil
}

// WriterTx is a transaction on a PINNED writer connection (MTIX-198). The
// connection stays with the transaction until Release, so after a failed
// COMMIT, which leaves SQLite inside the transaction, no other goroutine can
// obtain the stuck connection from the pool: it is rolled back (or discarded)
// first, on this same connection. Use Tx for statements.
type WriterTx struct {
	*sql.Tx
	conn      *sql.Conn
	rollback  RollbackWriterFunc
	logger    *slog.Logger
	committed bool
	settled   bool
	released  bool
}

// BeginWriterTx pins the writer connection of db and begins a transaction on
// it; a nil rollback is the default ROLLBACK (the non-nil form is a test seam).
// The caller MUST defer Release. The single-connection pool means fn code
// must use the transaction, never db, or it would wait on its own connection;
// that already held when the transaction came from the pool.
func BeginWriterTx(ctx context.Context, db *sql.DB, logger *slog.Logger, rollback RollbackWriterFunc) (*WriterTx, error) {
	return beginWriterTx(ctx, db, rollback, logger)
}

func beginWriterTx(ctx context.Context, db *sql.DB, rollback RollbackWriterFunc, logger *slog.Logger) (*WriterTx, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("pin writer connection: %w", err)
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		if cErr := conn.Close(); cErr != nil {
			return nil, fmt.Errorf("%w; release writer connection: %w", err, cErr)
		}
		return nil, err
	}
	if rollback == nil {
		rollback = defaultRollbackWriter
	}
	return &WriterTx{Tx: tx, conn: conn, rollback: rollback, logger: logger}, nil
}

// Commit commits the transaction. After a failure, wrap the error through
// RecoverAfterCommit before returning it.
func (w *WriterTx) Commit() error {
	err := w.Tx.Commit()
	if err == nil {
		w.committed = true
	}
	return err
}

// settle returns the pinned connection to a clean state: ROLLBACK on a context
// detached from the caller's cancellation, and on failure discard it so the
// pool replaces it.
func (w *WriterTx) settle(ctx context.Context) error {
	w.settled = true
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writerRecoveryTimeout)
	defer cancel()
	rbErr := w.rollback(rctx, w.conn)
	if rbErr == nil {
		return nil
	}
	if dErr := discardConn(w.conn); dErr != nil {
		return fmt.Errorf("%w; %w", rbErr, dErr)
	}
	return rbErr
}

// RecoverAfterCommit cleans the pinned connection after a failed COMMIT. It
// returns commitErr unchanged when the connection was left clean, and
// otherwise commitErr joined with the recovery error, both wrapped with %w.
// A bad-connection COMMIT already discards the connection in database/sql;
// recovery errors consisting only of closed-connection causes add no detail.
func (w *WriterTx) RecoverAfterCommit(ctx context.Context, commitErr error) error {
	if recErr := w.settle(ctx); recErr != nil {
		if (errors.Is(commitErr, driver.ErrBadConn) || errors.Is(commitErr, sql.ErrConnDone)) && isOnlyClosedConnError(recErr) {
			return commitErr
		}
		return fmt.Errorf("%w; writer recovery failed: %w", commitErr, recErr)
	}
	return commitErr
}

// Release returns the connection to the pool. A transaction that neither
// committed nor was settled (an error, a failed rollback or a panic) is rolled
// back first, on this connection, so the pool never receives a stuck one.
// Release is idempotent: call it before work that needs the writer again
// (callbacks after commit), and defer it for every other exit.
// Failures are logged: the caller has already decided its own result.
func (w *WriterTx) Release(ctx context.Context) {
	if w.released {
		return
	}
	w.released = true
	// A transaction still live (a panic, an early return) holds the connection
	// lock, and Close would wait on it forever: end it first. ErrTxDone is the
	// normal answer after a commit or an earlier rollback.
	if err := w.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		w.logger.Error("rollback before release failed", "error", err)
	}
	if !w.committed && !w.settled {
		if err := w.settle(ctx); err != nil {
			w.logger.Error("writer connection recovery failed", "error", err)
		}
	}
	if err := w.conn.Close(); err != nil && !errors.Is(err, sql.ErrConnDone) {
		w.logger.Error("release writer connection failed", "error", err)
	}
}

// beginWriterTx pins the store's writer connection (MTIX-198).
func (s *Store) beginWriterTx(ctx context.Context) (*WriterTx, error) {
	return beginWriterTx(ctx, s.writeDB, s.rollbackWriter, s.logger)
}
