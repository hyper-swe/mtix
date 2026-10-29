// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// quarantineAcknowledgementFix only names acknowledgement when an actual
// unacknowledged raw event is eligible. Stored reason text is not authority.
func quarantineAcknowledgementFix(ctx context.Context, st *sqlite.Store, _ bool) (string, error) {
	var after *sqlite.QuarantineKey
	var firstID string
	var allIDs []string
	eligible := 0
	for {
		page, err := st.QuarantinePage(ctx, after, 500)
		if err != nil {
			return "", fmt.Errorf("scan acknowledgement fixes: list quarantines: %w", err)
		}
		if len(page) == 0 {
			return quarantineAcknowledgementCommand(firstID, eligible, allIDs), nil
		}
		for _, q := range page {
			allIDs = append(allIDs, q.EventID)
			if !service.QuarantineAcknowledgementEligible(q) {
				continue
			}
			ack, err := st.QuarantineAcknowledgementFor(ctx, q)
			if err != nil {
				return "", fmt.Errorf("scan acknowledgement fixes: read receipt: %w", err)
			}
			if ack == nil {
				if eligible == 0 {
					firstID = q.EventID
				}
				eligible++
			}
		}
		last := page[len(page)-1]
		after = &sqlite.QuarantineKey{Lamport: last.Lamport, EventID: last.EventID}
	}
}

// quarantineAcknowledgementCommand formats the hint for either output mode. It
// always names the event by its handle, so the command is typeable for every id
// (NUL, invalid UTF-8, shell metacharacters) and resolves to that event. It
// carries no --reason value, so copying it unchanged is refused until the
// operator supplies an explanation. The handle is the shortest prefix of at
// least 12 hex characters that no other retained event shares
// (MTIX-187.2.6.3, MTIX-187.7.6).
func quarantineAcknowledgementCommand(eventID string, eligible int, allIDs []string) string {
	if eligible == 0 {
		return ""
	}
	return fmt.Sprintf("Inspect and report retained events (%d eligible unacknowledged; first shown): mtix sync quarantine list; add --reason '<your explanation>' after the handle, then: mtix sync quarantine acknowledge --handle %s",
		eligible, service.QuarantineHandleAmong(allIDs, eventID))
}
