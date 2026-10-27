// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// quarantineAcknowledgementFix only names acknowledgement when an actual
// unacknowledged raw event is eligible. Stored reason text is not authority.
func quarantineAcknowledgementFix(ctx context.Context, st *sqlite.Store, jsonOutput bool) (string, error) {
	var after *sqlite.QuarantineKey
	var firstID string
	eligible := 0
	for {
		page, err := st.QuarantinePage(ctx, after, 500)
		if err != nil {
			return "", fmt.Errorf("scan acknowledgement fixes: list quarantines: %w", err)
		}
		if len(page) == 0 {
			return quarantineAcknowledgementCommand(firstID, eligible, jsonOutput), nil
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

// quarantineAcknowledgementCommand formats a hint for the selected output mode,
// requiring an operator reason while preserving id presentation (MTIX-187.2.6.3).
func quarantineAcknowledgementCommand(eventID string, eligible int, jsonOutput bool) string {
	if eligible == 0 {
		return ""
	}
	if !jsonOutput && recoveryHintNeedsInspection(eventID) {
		return fmt.Sprintf("Inspect retained events (%d eligible unacknowledged; first id %s): mtix sync quarantine list --json; this id cannot be included in a text recovery command.", eligible, strconv.QuoteToASCII(eventID))
	}
	quotedID := "'" + strings.ReplaceAll(eventID, "'", "'\"'\"'") + "'"
	return fmt.Sprintf("Inspect and report retained events (%d eligible unacknowledged; first shown); add --reason '<your explanation>' before --, then: mtix sync quarantine acknowledge -- %s", eligible, quotedID)
}

// recoveryHintNeedsInspection classifies values needing a visible text label.
func recoveryHintNeedsInspection(id string) bool {
	if !utf8.ValidString(id) {
		return true
	}
	for _, r := range id {
		if !unicode.IsPrint(r) {
			return true
		}
	}
	return false
}
