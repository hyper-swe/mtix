// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// cancelMode says how a scanner-like writer cancels its context (MTIX-112.17.44).
// Every mode is synchronous, so the test needs no timers.
type cancelMode int

const (
	cancelNever          cancelMode = iota // control: nothing is cancelled
	cancelBeforeBegin                      // context already cancelled when WithTx starts
	cancelAfterStatement                   // cancelled inside fn, after the writes, before COMMIT
	cancelWhileDone                        // cancelled by another goroutine, handed over by channel
	cancelModes
)

const (
	cancelWriters    = 4
	cancelIterations = 60
	cancelMarkKey    = "cancel.mark"
)

// testReadQuerier is the read surface these helpers need; the store's reader
// pool, its writer and a plain *sql.DB all satisfy it.
type testReadQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func cancelKeyExists(ctx context.Context, t *testing.T, db testReadQuerier, key string) bool {
	t.Helper()
	var n int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT COUNT(*) FROM meta WHERE key = ?`, key).Scan(&n))
	return n == 1
}

// cancelledScannerTx runs one scanner-like write: a ledger row plus a mark
// bump in the same transaction, with the caller context cancelled per mode.
// It returns WithTx's error. Statements use the background context so the
// cancellation reaches COMMIT, the case under test.
func cancelledScannerTx(s interface {
	WithTx(context.Context, func(*sql.Tx) error) error
}, mode cancelMode, key string) error {
	bg := context.Background()
	ctx, cancel := context.WithCancel(bg)
	defer cancel()
	if mode == cancelBeforeBegin {
		cancel()
	}
	var handoff chan struct{}
	if mode == cancelWhileDone {
		handoff = make(chan struct{})
		done := make(chan struct{})
		go func() { // cancels when told, so no timing is involved
			<-handoff
			cancel()
			close(done)
		}()
		defer func() { <-done }()
	}
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(bg, `INSERT INTO meta(key,value) VALUES(?, 'x')`, key); err != nil {
			return err
		}
		if _, err := tx.ExecContext(bg, `INSERT INTO meta(key,value) VALUES(?, '1')
			ON CONFLICT(key) DO UPDATE SET value = CAST(value AS INTEGER) + 1`, cancelMarkKey); err != nil {
			return err
		}
		switch mode {
		case cancelAfterStatement:
			cancel()
		case cancelWhileDone:
			close(handoff)
			<-ctx.Done()
		}
		return nil
	})
}

// TestWithTx_CancelledScannerWriters_AllOrNothingAndPoolsAgree pins the
// invariants behind the MTIX-112.2.2.1 review observation that the writer and
// read pool differed after cancelled scans. That observation was NOT
// reproduced (MTIX-112.17.44); this test pins the invariants instead: a
// cancelled WithTx is all-or-nothing against the error it returns, and an
// acknowledged write is never lost while other writers run. Writer and read
// pool agreement is asserted only after the writers are quiet, including after
// cancelled read transactions.
//
// Guard boundary: this test does not detect a no-op settle step, because
// database/sql itself ends a context-cancelled COMMIT. It kills only a
// recovery that drops RecoverAfterCommit's commit error. The settle and
// recovery path for other commit failures is guarded by the existing
// TestCommitFailure_* tests (tx_commit_failure_test.go); the late-cancellation
// outcome is pinned by TestWithTx_CancellationVisibleOnlyAfterCommit_*.
func TestWithTx_CancelledScannerWriters_AllOrNothingAndPoolsAgree(t *testing.T) {
	s := newTestStore(t)
	bg := context.Background()

	var acked atomic.Int64
	var wg sync.WaitGroup
	for w := 0; w < cancelWriters; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < cancelIterations; i++ {
				key := fmt.Sprintf("cancel.row.%d.%d", w, i)
				err := cancelledScannerTx(s, cancelMode((w+i)%int(cancelModes)), key)
				// Checked while the other writers run: this key is private.
				present := cancelKeyExists(bg, t, s.WriteDB(), key)
				if err == nil {
					acked.Add(1)
					require.True(t, present, "acknowledged write %s must persist", key)
				} else {
					require.False(t, present, "errored write %s must not be committed", key)
				}
			}
		}(w)
	}
	var readers sync.WaitGroup
	readers.Add(1)
	go func() { // cancelled read transactions against the read pool
		defer readers.Done()
		for i := 0; i < cancelWriters*cancelIterations; i++ {
			ctx, cancel := context.WithCancel(bg)
			tx, err := s.ReadDB().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
			if err != nil {
				cancel()
				continue
			}
			_, _ = tx.ExecContext(ctx, `SELECT COUNT(*) FROM meta`)
			cancel() // leaves the transaction to the pool's own cancellation cleanup
			_ = tx.Rollback()
		}
	}()
	wg.Wait()
	readers.Wait()

	// Quiescent: every writer and reader has returned.
	require.Positive(t, acked.Load(), "control mode must acknowledge writes")
	writerRows := cancelKeys(bg, t, s.WriteDB())
	readerRows := cancelKeys(bg, t, s.ReadDB())
	require.Equal(t, writerRows, readerRows, "read pool must agree with the writer once writers are quiet")
	require.Len(t, writerRows, int(acked.Load())+1, "ledger rows plus the mark")
	require.Equal(t, fmt.Sprint(acked.Load()), writerRows[cancelMarkKey], "mark counts exactly the acknowledged writes")
}

func cancelKeys(ctx context.Context, t *testing.T, db testReadQuerier) map[string]string {
	t.Helper()
	rows, err := db.QueryContext(ctx, `SELECT key, value FROM meta WHERE key LIKE 'cancel.%'`)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		require.NoError(t, rows.Scan(&k, &v))
		// Row values are 'x' for ledger rows; only the mark carries a count.
		if k != cancelMarkKey {
			v = ""
		}
		out[k] = v
	}
	require.NoError(t, rows.Err())
	return out
}

// commitVisibleCtx is a context that reports cancellation only once the
// row under key is visible on the read pool. Statements in fn use another
// context and the read pool sees only committed data, so Err() flips to
// Canceled strictly after a successful COMMIT. Done() is nil, so database/sql
// never aborts the transaction on its own: only code that consults Err()
// after the commit can observe the cancellation.
type commitVisibleCtx struct {
	context.Context
	t   *testing.T
	db  testReadQuerier
	key string
}

func (c commitVisibleCtx) Done() <-chan struct{} { return nil }

func (c commitVisibleCtx) Err() error {
	if cancelKeyExists(context.Background(), c.t, c.db, c.key) {
		return context.Canceled
	}
	return nil
}

// TestWithTx_CancellationVisibleOnlyAfterCommit_ReturnsNilAndKeepsRow pins
// Story 2 of MTIX-112.17.44: a WithTx whose cancellation becomes visible only
// after a successful COMMIT reports no error and the acknowledged row is on
// both the writer and the read pool. It kills mutants that return ctx.Err()
// after a successful Commit in Store.withWriteTx. It does not cover failed
// COMMITs or a no-op settle; the TestCommitFailure_* tests do
// (tx_commit_failure_test.go).
func TestWithTx_CancellationVisibleOnlyAfterCommit_ReturnsNilAndKeepsRow(t *testing.T) {
	tests := []struct{ name, key string }{
		{"single row", "cancel.late.one"},
		{"second transaction on the same store", "cancel.late.two"},
	}
	s := newTestStore(t)
	bg := context.Background()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := commitVisibleCtx{Context: bg, t: t, db: s.ReadDB(), key: tt.key}
			require.NoError(t, ctx.Err(), "not cancelled before the commit")

			err := s.WithTx(ctx, func(tx *sql.Tx) error {
				_, err := tx.ExecContext(bg, `INSERT INTO meta(key,value) VALUES(?, 'x')`, tt.key)
				return err
			})

			require.NoError(t, err, "a committed write must not report the late cancellation")
			require.True(t, cancelKeyExists(bg, t, s.WriteDB(), tt.key), "row on the writer")
			require.True(t, cancelKeyExists(bg, t, s.ReadDB(), tt.key), "row on the read pool")
			require.ErrorIs(t, ctx.Err(), context.Canceled, "the cancellation really became visible")
		})
	}
}
