// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestWakeBudget_054ReaderMutatesWithoutChangingAccounting(t *testing.T) {
	binary := compat054Binary(t)
	f := newWakeCompatFixture(t)
	claim, err := f.store.ClaimWakeBudget(t.Context(), f.key, time.Minute, func(model.WakeBudgetProposal) (model.WakeBudgetDecision, error) {
		return model.WakeBudgetDecision{Allow: true}, nil
	})
	require.NoError(t, err)
	require.True(t, claim.Won)
	before, err := f.store.WakeBudgetCounters(t.Context())
	require.NoError(t, err)
	require.Len(t, before, 1)
	require.Equal(t, int64(1), before[0].Total)
	ledger, err := f.store.WakeLedgerStats(t.Context())
	require.NoError(t, err)
	var version string
	require.NoError(t, f.store.ReadDB().QueryRowContext(t.Context(), "SELECT value FROM meta WHERE key=?", "schema_version").Scan(&version))
	require.Equal(t, "4", version)
	var nodesBefore, nodesAfter int64
	require.NoError(t, f.store.ReadDB().QueryRowContext(t.Context(), "SELECT count(*) FROM nodes").Scan(&nodesBefore))
	output := runWakeCompatOldMutation(t, binary, f.project)
	t.Logf("real 0.5.4 mutation: %s", output)
	require.NoError(t, f.store.ReadDB().QueryRowContext(t.Context(), "SELECT count(*) FROM nodes").Scan(&nodesAfter))
	require.Equal(t, nodesBefore+1, nodesAfter)
	after, err := f.store.WakeBudgetCounters(t.Context())
	require.NoError(t, err)
	require.Equal(t, before, after)
	ledgerAfter, err := f.store.WakeLedgerStats(t.Context())
	require.NoError(t, err)
	require.Equal(t, ledger, ledgerAfter)
}
