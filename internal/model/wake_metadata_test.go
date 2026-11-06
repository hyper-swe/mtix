// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestWakeMetadata_InternalIdentityDoesNotChangeJSONShape(t *testing.T) {
	key := model.WakeKey{AgentID: "worker", HostID: "host", UID: "1000", Epoch: 1, EventSeq: 7}
	fenced := key
	fenced.EventID = "stable-journal-event"
	for _, tt := range []struct {
		name          string
		before, after any
	}{
		{"key", key, fenced},
		{"claim result", model.WakeBudgetResult{}, model.WakeBudgetResult{Replaced: true}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			before, err := json.Marshal(tt.before)
			require.NoError(t, err)
			after, err := json.Marshal(tt.after)
			require.NoError(t, err)
			require.JSONEq(t, string(before), string(after))
		})
	}
}
