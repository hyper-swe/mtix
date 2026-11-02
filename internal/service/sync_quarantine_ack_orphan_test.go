// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// An orphaned dependent the clone quarantined is acknowledgeable by its
// marker reason on a valid retained envelope (MTIX-95.37.1); nothing else is.
func TestQuarantineAcknowledgementEligible_CloneOrphan_ByMarkerReasonOnly(t *testing.T) {
	valid := `{"project_prefix":"TEST","author_id":"peer","author_machine_hash":"0123456789abcdef","vector_clock":{},"lamport_clock":1,"event_id":"` + quarantineAckEventID + `","node_id":"TEST-9.1","op_type":"claim","payload":{"agent_id":"agent-a"}}`
	tests := []struct {
		name, source, reason, raw string
		want                      bool
	}{
		{"clone orphan pull", "pull", sqlite.CloneOrphanReason + ": node TEST-9.1: not found", valid, true},
		{"clone orphan sweep row", "sweep", sqlite.CloneOrphanReason, valid, true},
		{"valid event with another reason", "pull", "apply failed", valid, false},
		{"marker not at start", "pull", "x " + sqlite.CloneOrphanReason, valid, false},
		{"push hold with marker", "push", sqlite.CloneOrphanReason, valid, false},
		{"malformed raw with marker", "pull", sqlite.CloneOrphanReason, "not-json", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := service.QuarantineAcknowledgementEligible(sqlite.QuarantinedEvent{
				EventID: quarantineAckEventID, Source: tt.source, RawEvent: tt.raw, Reason: tt.reason})
			require.Equal(t, tt.want, got)
		})
	}
}
