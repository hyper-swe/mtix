// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package ingest

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"syscall"

	"github.com/hyper-swe/mtix/internal/model"
)

// sqliteConstraint is SQLITE_CONSTRAINT, the primary result code of a
// uniqueness, foreign-key or check violation: the data conflicts with the
// store, which is a verdict about the record, unlike every other code.
const sqliteConstraint = 19

// isStoreFailure says whether err is a failure of the local store or its
// environment (busy, full disk, I/O, a dropped connection, a cancelled or
// expired context) rather than a verdict that the record is invalid
// (MTIX-303, FR-21 §6.4).
//
// The distinction decides the record's fate. A verdict quarantines the record
// and the cursor moves past it; a store failure must do neither, because the
// record is valid and the next poll will apply it. A cancelled ctx wins over
// whatever text the driver gave the interrupt.
func isStoreFailure(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if ctx.Err() != nil ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, sql.ErrConnDone) || errors.Is(err, sql.ErrTxDone) ||
		errors.Is(err, driver.ErrBadConn) ||
		errors.Is(err, model.ErrDiskFull) || errors.Is(err, model.ErrCorrupted) {
		return true
	}
	// The driver's error type is matched by its Code method, so this file
	// does not import the driver (the single-builder scanner allows that
	// import only in the error-type files of internal/store/sqlite).
	var coded interface{ Code() int }
	if errors.As(err, &coded) {
		return coded.Code()&0xFF != sqliteConstraint
	}
	var pathErr *fs.PathError
	var errno syscall.Errno
	return errors.As(err, &pathErr) || errors.As(err, &errno)
}

// applyEvent runs the shipped apply path in one store transaction. verdict is
// the apply path's own rejection of the record (quarantined by the caller);
// failure is a store error that must fail the poll with the cursor before the
// record. A transaction that fails to begin or commit is always a failure:
// the apply function either never ran or ran fine, so nothing says the
// record is invalid.
func (in *Ingestor) applyEvent(ctx context.Context, e *model.SyncEvent) (verdict, failure error) {
	var applyErr error
	txErr := in.cfg.Store.WithTx(ctx, func(tx *sql.Tx) error {
		applyErr = in.cfg.Store.IdempotentApply(ctx, tx, e)
		return applyErr
	})
	switch {
	case txErr == nil:
		return nil, nil
	case applyErr == nil || isStoreFailure(ctx, applyErr) || isStoreFailure(ctx, txErr):
		return nil, txErr
	default:
		return txErr, nil
	}
}

// failPoll ends the poll with a local store failure met while handling one
// record, leaving the cursor before it. It is applyRecord's stop result.
func (in *Ingestor) failPoll(stats *Stats, peerID string, rs uint64, what string, err error) bool {
	in.log.Error("relay ingest hit a store failure and will retry the record on the next poll",
		slog.String("peer", peerID), slog.Uint64("rs", rs), slog.String("step", what), slog.Any("error", err))
	stats.fatal = fmt.Errorf("%s relay record %s rs %d: %w", what, peerID, rs, err)
	return true
}
