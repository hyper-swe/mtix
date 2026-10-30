// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

// Slim returns a copy of the response whose chain entries carry only the
// structural fields (id, depth, tier, title, status). The ancestor prompts,
// descriptions, creators and unresolved annotations are dropped from the
// structured chain because assembled_prompt already carries them once
// (MTIX-225, from the MTIX-224 prompt-caching research). assembled_prompt,
// siblings and blocking_deps are unchanged, so an agent loses no content.
// The receiver is not modified; callers that need the full structured chain
// (the --chain flag, MCP chain:true) simply do not call Slim.
func (r *ContextResponse) Slim() *ContextResponse {
	slim := *r
	slim.Chain = make([]ContextEntry, len(r.Chain))
	for i, e := range r.Chain {
		slim.Chain[i] = ContextEntry{
			ID:     e.ID,
			Depth:  e.Depth,
			Tier:   e.Tier,
			Title:  e.Title,
			Status: e.Status,
		}
	}
	return &slim
}
