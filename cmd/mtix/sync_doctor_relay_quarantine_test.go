// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func relayQSample(rs uint64, op, state string) sqlite.RelayQuarantineRow {
	return sqlite.RelayQuarantineRow{
		RelayQuarantineRecord: sqlite.RelayQuarantineRecord{
			RelayQuarantineKey: sqlite.RelayQuarantineKey{PeerID: "fedcba9876543210", PubEpoch: 1, RS: rs},
			OpType:             op, NodeID: "TEST-2",
		},
		State: state,
	}
}

// TestRelayQuarantineCheck_FixNamesTheFirstOpenRow: a resolved row sits ahead of
// the open ones, and the Fix must name the first OPEN row's handle, never a
// settled row's (a command that would fail with already resolved).
func TestRelayQuarantineCheck_FixNamesTheFirstOpenRow(t *testing.T) {
	rows := []sqlite.RelayQuarantineRow{
		relayQSample(1, "create_node", sqlite.RelayQuarantineResolved),
		relayQSample(2, "create_node", sqlite.RelayQuarantineOpen),
		relayQSample(3, "update_field", sqlite.RelayQuarantineOpen),
	}
	handles := service.QuarantineHandles([]string{
		service.RelayQuarantineKeyString(rows[0].RelayQuarantineKey),
		service.RelayQuarantineKeyString(rows[1].RelayQuarantineKey),
		service.RelayQuarantineKeyString(rows[2].RelayQuarantineKey),
	})
	check := relayQuarantineCheck(rows)
	require.False(t, check.Pass)
	require.Contains(t, check.Detail, "2 relay record(s)")
	require.Contains(t, check.Fix, "--handle "+handles[1])
	require.NotContains(t, check.Fix, handles[0], "never the settled row")
	require.NotContains(t, check.Fix, handles[2])
}

// TestRelayQuarantineCheck_Fix_OffersBothVerbsWithTheirTradeOff pins MTIX-268's
// wording: neither verb is the default, a recreate's duplicate and the cancel
// command are stated, and a non-create row is offered accept only.
func TestRelayQuarantineCheck_Fix_OffersBothVerbsWithTheirTradeOff(t *testing.T) {
	create := relayQuarantineCheck([]sqlite.RelayQuarantineRow{relayQSample(2, "create_node", sqlite.RelayQuarantineOpen)}).Fix
	require.Contains(t, create, "either mtix sync quarantine resolve --handle")
	require.Contains(t, create, "--accept --reason")
	require.Contains(t, create, "--recreate --reason")
	require.Contains(t, create, "keeps it unapplied")
	require.Contains(t, create, "the author peer then holds both")
	require.Contains(t, create, `mtix cancel <id> --reason "duplicate of <other-id>"`)
	require.Less(t, indexOf(create, "--accept"), indexOf(create, "--recreate"), "recreate is not offered first")

	other := relayQuarantineCheck([]sqlite.RelayQuarantineRow{relayQSample(3, "update_field", sqlite.RelayQuarantineOpen)}).Fix
	require.Contains(t, other, "--accept --reason")
	require.NotContains(t, other, "--recreate")
}

func TestRelayQuarantineCheck_AllResolved_Passes(t *testing.T) {
	check := relayQuarantineCheck([]sqlite.RelayQuarantineRow{relayQSample(1, "create_node", sqlite.RelayQuarantineResolved)})
	require.True(t, check.Pass)
	require.Empty(t, check.Fix)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
