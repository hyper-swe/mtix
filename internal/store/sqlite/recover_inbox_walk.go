// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
)

// Bound failed probes, not healthy rows. A broken tail has no trustworthy upper
// bound; reaching this budget is explicitly incomplete, never guessed success.
const inboxSalvageProbeLimit = 10000

type inboxRowidWalk struct {
	next, tail    int64
	bounded, done bool
	failed        int
	err           error
}

// read returns the next readable row in table order without any secondary index.
func (w *inboxRowidWalk) read(ctx context.Context, db *sql.DB) (salvageMail, string, bool) {
	for !w.done && (!w.bounded || w.next <= w.tail) {
		row := salvageMail{seq: w.next}
		e := &row.e
		var op string
		err := db.QueryRowContext(ctx, salvageInboxSQL, w.next).Scan(&e.EventID, &e.Project, &e.NodeID, &e.UID, &e.Author, &e.Payload, &e.CreatedAt, &row.seq, &op)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err == nil {
			w.advance(row.seq)
			return row, op, true
		}
		if ctx.Err() != nil {
			w.err = errors.Join(w.err, ctx.Err())
			break
		}
		if !w.failedRead(ctx, db, err) {
			break
		}
	}
	w.done = true
	return salvageMail{}, "", false
}

func (w *inboxRowidWalk) advance(seq int64) {
	if seq == math.MaxInt64 {
		w.done = true
		return
	}
	w.next = seq + 1
}

// failedRead probes the next position rather than abandoning a damaged range.
func (w *inboxRowidWalk) failedRead(ctx context.Context, db *sql.DB, readErr error) bool {
	w.failed++
	if w.err == nil {
		w.err = readErr
	}
	if w.failed == 1 {
		boundErr := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(rowid),0) FROM sync_events NOT INDEXED`).Scan(&w.tail)
		w.bounded = boundErr == nil
		if boundErr != nil {
			w.err = errors.Join(w.err, boundErr, fmt.Errorf("journal upper rowid bound unreadable; lost record count unknown"))
			if !isCorruptionError(boundErr) {
				return false
			}
		}
	}
	if w.failed >= inboxSalvageProbeLimit {
		w.err = errors.Join(w.err, fmt.Errorf("journal incomplete: error-probe limit %d reached at rowid %d; unprobed tail and lost record count unknown", inboxSalvageProbeLimit, w.next))
		return false
	}
	w.advance(w.next)
	return !w.done
}

func (w *inboxRowidWalk) resultError() error {
	if w.failed == 0 {
		return w.err
	}
	return errors.Join(w.err, fmt.Errorf("journal incomplete: %d unreadable rowid positions probed; lost record count unknown (at most this many within the probed range)", w.failed))
}
