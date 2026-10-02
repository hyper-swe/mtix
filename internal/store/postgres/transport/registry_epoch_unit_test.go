// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// TestDecideAgainstRegistered_HeldStamp_EarlierOnlyWithinHubRange: an
// incoming create of another node meets a held create; it is a restore
// collision only when the held create's stamp lies from 0 up to, but not
// including, the hub's current epoch. A stamp at the current epoch, above
// it or below 0 is not earlier: the incoming create renumbers. The same
// uid is the same node: a no-op (MTIX-95.1.7).
func TestDecideAgainstRegistered_HeldStamp_EarlierOnlyWithinHubRange(t *testing.T) {
	incoming := &model.SyncEvent{EventID: "e-in", ProjectPrefix: "MTIX", NodeID: "MTIX-1"}
	tests := []struct {
		name          string
		heldUID       string
		heldEpoch     int64
		currentEpoch  int64
		wantCollision bool
		wantNoop      bool
	}{
		{"earlier epoch", "u-held", 0, 1, true, false},
		{"earlier epoch, not the first", "u-held", 2, 3, true, false},
		{"the current epoch", "u-held", 1, 1, false, false},
		{"above the current epoch", "u-held", 2, 1, false, false},
		{"below 0", "u-held", -1, 0, false, false},
		{"below 0 after a restore", "u-held", -1, 1, false, false},
		{"the same node", "u-in", 0, 1, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			held := registeredCreate{eventID: "e-held", uid: tt.heldUID, epoch: tt.heldEpoch}
			got := decideAgainstRegistered(incoming, held, "u-in", tt.currentEpoch)
			require.Equal(t, tt.wantNoop, got.noop)
			if tt.wantCollision {
				require.Nil(t, got.renumber)
				require.Equal(t, &RestoreCollision{EventID: "e-in", ProjectPrefix: "MTIX", DisplayPath: "MTIX-1",
					HeldEventID: "e-held", HeldEpoch: tt.heldEpoch, DetectedEpoch: tt.currentEpoch}, got.restoreCollision)
				return
			}
			require.Nil(t, got.restoreCollision)
			if !tt.wantNoop {
				require.Equal(t, &RenumberRequired{EventID: "e-in", ProjectPrefix: "MTIX", DisplayPath: "MTIX-1",
					RegisteredEventID: "e-held"}, got.renumber)
			}
		})
	}
}
