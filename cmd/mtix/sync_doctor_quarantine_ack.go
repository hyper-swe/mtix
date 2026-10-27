// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// quarantineAcknowledgementFix only names acknowledgement when an actual
// unacknowledged raw event is eligible. Stored reason text is not authority.
func quarantineAcknowledgementFix(ctx context.Context, st *sqlite.Store) (string, error) {
	var after *sqlite.QuarantineKey
	var firstID string
	eligible := 0
	for {
		page, err := st.QuarantinePage(ctx, after, 500)
		if err != nil {
			return "", fmt.Errorf("scan acknowledgement fixes: list quarantines: %w", err)
		}
		if len(page) == 0 {
			return quarantineAcknowledgementCommand(firstID, eligible), nil
		}
		for _, q := range page {
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

// quarantineAcknowledgementCommand quotes the event id as one POSIX shell
// argument after --, including ids that begin with a flag prefix (MTIX-187.2.6).
func quarantineAcknowledgementCommand(eventID string, eligible int) string {
	if eligible == 0 {
		return ""
	}
	quotedID := "'" + strings.ReplaceAll(eventID, "'", "'\"'\"'") + "'"
	return fmt.Sprintf("Inspect and report retained events (%d eligible unacknowledged; first shown), then: mtix sync quarantine acknowledge --reason 'reviewed invalid node id; accepted as unapplied' -- %s", eligible, quotedID)
}
