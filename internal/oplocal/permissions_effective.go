// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package oplocal

import "encoding/json"

// Unfinished allows retain only their prior committed grant. Legacy pending
// records without a preimage fail closed for their affected allows.
func restorePendingPermissionAllows(grants []AgentGrant, p permissionPending, host string) []AgentGrant {
	result := append([]AgentGrant{}, grants...)
	if p.Batch == nil {
		return result
	}
	var before []AgentGrant
	if p.BeforeGrants != nil {
		before = *p.BeforeGrants
	}
	for _, change := range p.Batch.Changes {
		if change.Action == "allow" && change.Outcome == "" {
			if p.BeforeGrants == nil {
				result = quarantineLegacyPermissionGrant(result, *p.Batch, host, change.AgentID)
				continue
			}
			result = restorePermissionGrant(result, before, p.Batch.BoardID, change.AgentID)
		}
	}
	return result
}

func permissionGrantIndex(grants []AgentGrant, board, agent string) int {
	for i, grant := range grants {
		if grant.BoardID == board && grant.AgentID == agent {
			return i
		}
	}
	return -1
}

func restorePermissionGrant(grants, before []AgentGrant, board, agent string) []AgentGrant {
	index := permissionGrantIndex(grants, board, agent)
	prior := permissionGrantIndex(before, board, agent)
	if prior >= 0 {
		if index >= 0 {
			grants[index] = before[prior]
			return grants
		}
		return append(grants, before[prior])
	}
	if index >= 0 {
		return append(grants[:index], grants[index+1:]...)
	}
	return grants
}

func permissionPendingGrantProjection(data map[string]json.RawMessage, grants []AgentGrant, p permissionPending, host string) (map[string]json.RawMessage, error) {
	grants = restorePendingPermissionAllows(grants, p, host)
	batch := *p.Batch
	batch.Changes = []PermissionChange{}
	for _, change := range p.Batch.Changes {
		if change.Action == "deny" {
			batch.Changes = append(batch.Changes, change)
		}
	}
	return permissionGrantProjection(data, grants, batch, host)
}

func permissionSnapshotGrantProjection(data map[string]json.RawMessage, grants []AgentGrant, pending permissionPending, host string, receipt *permissionReplay) (map[string]json.RawMessage, error) {
	if receipt != nil && receipt.Completed && !receipt.Cancelled {
		return permissionGrantProjection(data, grants, *pending.Batch, host)
	}
	return permissionPendingGrantProjection(data, grants, pending, host)
}
