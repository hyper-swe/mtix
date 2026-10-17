// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import "context"

// An older accepted allow may already have overwritten a deny. Without its
// committed preimage, absence must never fall through to implicit eligibility.
func quarantineLegacyPermissionGrant(grants []AgentGrant, batch PermissionBatch, host, agent string) []AgentGrant {
	index := permissionGrantIndex(grants, batch.BoardID, agent)
	if index >= 0 && (grants[index].Denied || grants[index].Quarantined) {
		return grants
	}
	if index < 0 {
		grants = append(grants, AgentGrant{HostID: host, BoardID: batch.BoardID, Hub: batch.Hub, AgentID: agent, ConfirmedAt: batch.Time, Mode: batch.Mode, Source: batch.Source})
		index = len(grants) - 1
	}
	grants[index].Quarantined = true
	grants[index].QuarantineBatchID = effectivePermissionCaller(batch)
	return grants
}

// QuarantinedPermissionGrants reads active legacy recovery holds without writes.
func (s *State) QuarantinedPermissionGrants(ctx context.Context) ([]AgentGrant, error) {
	snapshot, err := s.permissionSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	holds := []AgentGrant{}
	for _, grant := range snapshot.grants {
		if grant.Quarantined {
			holds = append(holds, grant)
		}
	}
	return holds, nil
}
