// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCommitFailure_ConcurrentWriter_NoAcknowledgedWriteLost runs failed
// commits against a concurrent direct writer. A write the writer was told
// succeeded must persist: recovery must never roll back another caller's work
// (the stuck connection must not reach the pool before it is cleaned, MTIX-198).
func TestCommitFailure_ConcurrentWriter_NoAcknowledgedWriteLost(t *testing.T) {
	const failures = 60
	ctx := context.Background()
	s, _, _ := bufferedStore(t)
	armFault(t, s, "")

	var acked atomic.Int64
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			_, err := s.writeDB.ExecContext(ctx,
				`INSERT INTO meta (key, value) VALUES (?, 'x')`, fmt.Sprintf("conc.%d", i))
			if err == nil {
				acked.Add(1)
			}
		}
	}()
	for i := 0; i < failures; i++ {
		err := s.WithTx(ctx, func(tx *sql.Tx) error {
			_, execErr := tx.ExecContext(ctx, `INSERT INTO fault_child(p) VALUES (999)`)
			return execErr
		})
		require.ErrorContains(t, err, "FOREIGN KEY")
	}
	close(stop)
	wg.Wait()

	var persisted int64
	require.NoError(t, s.writeDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM meta WHERE key LIKE 'conc.%'`).Scan(&persisted))
	require.Equal(t, acked.Load(), persisted, "every acknowledged write must persist")
	require.Positive(t, acked.Load())
}
