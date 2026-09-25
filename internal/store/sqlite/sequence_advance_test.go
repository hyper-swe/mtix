// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// TestSkipTakenSequence_CounterPastLimit_FailsClearlyCounterUnchanged: a
// counter already at maxSequence or past it when the skip runs (written by
// another path after NextSequence took its number) is left as it is, and
// the skip fails with the limit error instead of writing 2147483648 or
// overflowing, for a root and a child key.
func TestSkipTakenSequence_CounterPastLimit_FailsClearlyCounterUnchanged(t *testing.T) {
	for _, key := range []string{"PROJ:", "PROJ:PROJ-1"} {
		for _, counter := range []int64{2147483647, 9223372036854775807} {
			t.Run(fmt.Sprintf("%s counter %d", key, counter), func(t *testing.T) {
				s, raw := applyTestStore(t)
				ctx := context.Background()
				for _, n := range []*model.Node{projectNode("PROJ-1", 1, "root"), projectNode("PROJ-1.1", 1, "child")} {
					require.NoError(t, s.CreateNode(ctx, n))
				}
				_, err := raw.Exec(`INSERT INTO sequences (key, value) VALUES (?, ?)`, key, counter)
				require.NoError(t, err)

				_, err = s.skipTakenSequence(ctx, key, 1)

				require.ErrorIs(t, err, model.ErrInvalidInput)
				require.ErrorContains(t, err, "2147483647")
				var kind string
				var value int64
				require.NoError(t, raw.QueryRow(`SELECT typeof(value), value FROM sequences WHERE key = ?`, key).
					Scan(&kind, &value))
				require.Equal(t, "integer", kind)
				require.Equal(t, counter, value)
			})
		}
	}
}
