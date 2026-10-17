// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPendingPermissionBatches_TamperedCommittedPreimage_RefusesWithoutReconstruction(t *testing.T) {
	for _, kind := range []string{"missing", "digest", "duplicate", "board", "agent", "host", "deny-pending", "hold-identity", "hold-conflicts-deny"} {
		t.Run(kind, func(t *testing.T) {
			s, _ := contractAcceptedPending(t, "after:grants.json")
			s.alertStage = nil
			p := generationPending(t, s)
			host, err := s.HostID()
			require.NoError(t, err)
			grant := AgentGrant{HostID: host, BoardID: p.Batch.BoardID, AgentID: p.Batch.Changes[0].AgentID, ConfirmedAt: p.Batch.Time, Mode: p.Batch.Mode, Source: p.Batch.Source}
			before := []AgentGrant{grant}
			switch kind {
			case "duplicate":
				before = append(before, grant)
			case "board":
				before[0].BoardID = "22222222-2222-4222-8222-222222222222"
			case "agent":
				before[0].AgentID = "other"
			case "host":
				before[0].HostID = strings.Repeat("0", 32)
			case "deny-pending":
				before[0].Denied = true
				before[0].PendingRegistration = true
			case "hold-identity":
				before[0].QuarantineBatchID = replayIntent().ID
			case "hold-conflicts-deny":
				before[0].Quarantined = true
				before[0].QuarantineBatchID = replayIntent().ID
				before[0].Denied = true
			}
			p.BeforeGrants = &before
			p.BeforeHash, err = permissionRecoveryHash(before)
			require.NoError(t, err)
			if kind == "missing" {
				p.BeforeGrants = nil
			}
			if kind == "digest" {
				p.BeforeHash = strings.Repeat("0", 64)
			}
			require.NoError(t, s.WriteJSON("permissions-pending", p))
			requirePermissionReadersRefuseUnchanged(t, s)
			_, err = s.PendingPermissionBatches(t.Context())
			require.Error(t, err)
		})
	}
}
