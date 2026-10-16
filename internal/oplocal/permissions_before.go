// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package oplocal

import "encoding/json"

func permissionRecoveryHash(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", failure("permission recovery hash", err)
	}
	return alertHash(data), nil
}

func permissionAllowAgents(b PermissionBatch) map[string]bool {
	agents := map[string]bool{}
	for _, change := range b.Changes {
		if change.Action == "allow" && change.Outcome == "" {
			agents[change.AgentID] = true
		}
	}
	return agents
}

func capturePermissionBefore(grants []AgentGrant, b PermissionBatch) ([]AgentGrant, string, error) {
	allowed := permissionAllowAgents(b)
	before := []AgentGrant{}
	for _, grant := range grants {
		if grant.BoardID == b.BoardID && allowed[grant.AgentID] {
			before = append(before, grant)
		}
	}
	hash, err := permissionRecoveryHash(before)
	return before, hash, err
}

func validatePermissionBefore(p permissionPending, host string) error {
	if p.BeforeGrants == nil {
		if p.BeforeHash != "" {
			return invalid("permission committed preimage is missing")
		}
		return nil
	}
	if p.Batch == nil {
		return invalid("permission committed preimage has no batch")
	}
	hash, err := permissionRecoveryHash(*p.BeforeGrants)
	if err != nil {
		return err
	}
	if hash != p.BeforeHash {
		return invalid("permission committed preimage hash conflicts")
	}
	allowed, seen := permissionAllowAgents(*p.Batch), map[string]bool{}
	for _, grant := range *p.BeforeGrants {
		if grant.BoardID != p.Batch.BoardID || !allowed[grant.AgentID] || seen[grant.AgentID] {
			return invalid("permission committed preimage scope conflicts")
		}
		if err := validateAgentGrant(grant, host); err != nil {
			return err
		}
		seen[grant.AgentID] = true
	}
	return nil
}
