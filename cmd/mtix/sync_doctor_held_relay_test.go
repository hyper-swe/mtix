// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"testing"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

// A dependent whose blocking creation is relay-held is told the hold is
// permanent, not that it resolves with the creation.
func TestHeldPushGuidance_DependentOfRelayHeldCreateIsPermanent(t *testing.T) {
	relay := map[string]bool{"EVT-1": true}
	dep := sqlite.HeldPushEvent{EventID: "EVT-2", NodeID: "MTIX-1.1", OpType: "create_node",
		Reason: holdDependsPrefix + "EVT-1 (MTIX-1)"}
	got := heldPushGuidance(dep, relay)
	for _, want := range []string{"permanent", "never renumbered", "escalate", "only adds held events"} {
		require.Contains(t, got, want)
	}
	require.NotContains(t, got, "resolves with")
	other := sqlite.HeldPushEvent{EventID: "EVT-3", NodeID: "MTIX-2.1", OpType: "create_node",
		Reason: holdDependsPrefix + "EVT-9 (MTIX-2)"}
	require.Contains(t, heldPushGuidance(other, relay), "resolves with the held creation")
}

// The relay hold's printed text and the --json detail are the same string and
// name no command this build does not ship to fix it.
func TestRelayHeldGuidance_RenderedAndJSON(t *testing.T) {
	h := sqlite.HeldPushEvent{EventID: "EVT-1", NodeID: "MTIX-1", OpType: "create_node",
		Reason: holdRefusedPrefix + "the hub holds MTIX-1 for another task and this creation is " + holdRelayCarried}
	text := heldPushGuidance(h, nil)
	r := appendCheck(DoctorReport{OverallPass: true}, heldPushCheckName, false, text)
	raw, err := json.Marshal(r)
	require.NoError(t, err)
	var back DoctorReport
	require.NoError(t, json.Unmarshal(raw, &back))
	require.Equal(t, text, back.Checks[0].Detail)
	for _, want := range []string{"permanent", "never renumbered", "relay-only peers",
		"only adds held events", "escalate"} {
		require.Contains(t, text, want)
	}
	for _, banned := range []string{"mtix create", "mtix cancel", "until it is resolved"} {
		require.NotContains(t, text, banned)
	}
}
