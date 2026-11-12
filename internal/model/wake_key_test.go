// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests the single wake key constructor every delivery route uses (MTIX-112.40.13).
package model_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestNewWakeKey_FromRecipientAndEvent_CarriesEveryKeyField(t *testing.T) {
	r := model.WakeRecipient{AgentID: "agent", HostID: "host", UID: "uid"}
	got := model.NewWakeKey(r, 7, 3, "event-id")
	require.Equal(t, model.WakeKey{AgentID: "agent", HostID: "host", UID: "uid", EventSeq: 7, Epoch: 3, EventID: "event-id"}, got)
	require.Equal(t, r, got.Recipient())
}

func TestNewWakeKey_SameInputs_AreEqualKeys(t *testing.T) {
	r := model.WakeRecipient{AgentID: "a", HostID: "h", UID: "u"}
	require.Equal(t, model.NewWakeKey(r, 1, 1, "e"), model.NewWakeKey(r, 1, 1, "e"))
	require.NotEqual(t, model.NewWakeKey(r, 1, 1, "e"), model.NewWakeKey(r, 1, 2, "e"), "epoch is part of the key")
}
