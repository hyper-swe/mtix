// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Rejected fixed-ID imports leave local counters untouched (MTIX-106).
package sqlite_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func TestImport_Refusal_PreservesSequenceCounter(t *testing.T) {
	for _, kind := range []string{"integrity", "transaction"} {
		t.Run(kind, func(t *testing.T) {
			src, dst := newTestStore(t), newTestStore(t)
			data := makeTestExport(t, src)
			ctx := context.Background()
			require.NoError(t, dst.CreateNode(ctx, seqNode("AA-1", 1)))
			_, err := dst.NextSequence(ctx, "AA:")
			require.NoError(t, err)
			if kind == "integrity" {
				data.NodeCount++
			} else {
				_, err = dst.WriteDB().ExecContext(ctx, `CREATE TRIGGER reject_import BEFORE INSERT ON nodes BEGIN SELECT RAISE(ABORT, 'import refused'); END`)
				require.NoError(t, err)
			}
			_, err = dst.Import(ctx, data, sqlite.ImportModeReplace, false)
			require.Error(t, err)
			var value int
			require.NoError(t, dst.QueryRow(ctx, `SELECT value FROM sequences WHERE key = ?`, "AA:").Scan(&value))
			assert.Equal(t, 2, value)
			_, err = dst.GetNode(ctx, "AA-1")
			require.NoError(t, err)
			if kind == "transaction" {
				_, err = dst.WriteDB().ExecContext(ctx, `DROP TRIGGER reject_import`)
				require.NoError(t, err)
			}
			next, err := dst.NextSequence(ctx, "AA:")
			require.NoError(t, err)
			assert.Equal(t, 3, next)
		})
	}
}
