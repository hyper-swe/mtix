// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import "github.com/hyper-swe/mtix/internal/model"

// DecisionDelete (ADR-008 §3.5, MTIX-219 decisions feature) contains the stored facts from one journal delete. Path is nil
// for legacy payloads; NodeID is the envelope path, which can be renamed.
type DecisionDelete struct {
	UID, NodeID string
	Path        *string
	Cascade     bool
}

// DecisionTicketDeleted implements ADR-008 §3.5 (MTIX-219 decisions feature; no
// FR number is assigned in REQUIREMENTS.md yet) for an A1-validated snapshot.
// A UID match anywhere takes precedence over every path-only match.
func DecisionTicketDeleted(ticket model.DecisionTicket, deletes []DecisionDelete) (deleted, byPath bool) {
	for i, path := range ticket.Paths {
		uid := ticket.UID
		if i > 0 {
			uid = ticket.Ancestors[i-1]
		}
		for _, d := range deletes {
			if i > 0 && !d.Cascade {
				continue
			}
			if uid != "" && uid == d.UID {
				return true, false
			}
			if ticket.PathMatch[i] && d.matchesPath(uid, path) {
				byPath = true
			}
		}
	}
	return byPath, byPath
}

func (d DecisionDelete) matchesPath(uid, path string) bool {
	storedPath := d.NodeID
	if d.Path != nil {
		storedPath = *d.Path
	}
	return storedPath == path && (d.UID == "" || uid == "" || model.IsBackfillUID(d.UID) || model.IsBackfillUID(uid))
}
