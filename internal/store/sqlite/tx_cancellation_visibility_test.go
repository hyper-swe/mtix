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

func cancelKeyExists(ctx context.Context, t *testing.T, db *sql.DB, key string) bool {
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

// TestWithTx_CancelledScannerWriters_AllOrNothingAndPoolsAgree pins why the
// MTIX-112.2.2.1 review saw the writer and read pool differ: a cancelled
// WithTx is all-or-nothing against the error it returns, an acknowledged write
// is never lost while other writers run, and the pools differ only while
// writers are still running. After the writers are quiet, the writer and the
// read pool agree, including after cancelled read transactions.
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

func cancelKeys(ctx context.Context, t *testing.T, db *sql.DB) map[string]string {
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
