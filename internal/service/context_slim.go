// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

// Slim returns a copy of the response whose chain entries drop the three
// fields that duplicate assembled_prompt: the ancestor prompt, the creator
// and the unresolved annotations. The assembled prompt already carries the
// prompt (with its attribution marker) and the annotations once (MTIX-225,
// from the MTIX-224 prompt-caching research). Each entry keeps id, depth,
// tier, title, status and description; the description stays because
// assembled_prompt never contains it (FR-12.2 still omits it for ancestors
// more than 2 levels from the target). assembled_prompt, siblings and
// blocking_deps are unchanged, so an agent loses no content. The receiver is
// not modified; callers that need the full structured chain (the --chain
// flag, MCP chain:true) simply do not call Slim.
func (r *ContextResponse) Slim() *ContextResponse {
	slim := *r
	slim.Chain = make([]ContextEntry, len(r.Chain))
	for i, e := range r.Chain {
		slim.Chain[i] = ContextEntry{
			ID:          e.ID,
			Depth:       e.Depth,
			Tier:        e.Tier,
			Title:       e.Title,
			Status:      e.Status,
			Description: e.Description,
		}
	}
	return &slim
}
