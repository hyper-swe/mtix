// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGuardTrip_AlertNamesConfigCommand_QuotesOpaqueRecipient(t *testing.T) {
	for _, tc := range []struct {
		name    string
		fixture func(*testing.T) *wakeBudgetFixture
		count   int
		command string
	}{
		{"budget", func(t *testing.T) *wakeBudgetFixture { return newWakeBudgetFixture(t, "1") }, 2, "mtix config set wake.wakes_per_hour 2"},
		{"backoff", newWakeBackoffFixture, 6, "mtix config set wake.backoff_after_unacked 6"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := tc.fixture(t)
			b := f.boardFor(t, tc.count, "O'Brien; echo injected")
			require.NoError(t, b.dispatcher(t, false, nil).Pass(t.Context(), b.keys))
			rows, err := f.alerts.Recent(t.Context(), 0)
			require.NoError(t, err)
			require.Len(t, rows, 1)
			require.Equal(t, tc.command+` --agent 'O'\''Brien; echo injected'`, rows[0].Fix)
		})
	}
}
