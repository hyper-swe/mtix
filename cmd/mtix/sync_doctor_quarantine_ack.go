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
func quarantineAcknowledgementFix(ctx context.Context, st *sqlite.Store) (string, error) {
	var after *sqlite.QuarantineKey
	for {
		page, err := st.QuarantinePage(ctx, after, 500)
		if err != nil {
			return "", fmt.Errorf("scan acknowledgement fixes: list quarantines: %w", err)
		}
		if len(page) == 0 {
			return "", nil
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
				return "Inspect and report the retained event, then: mtix sync quarantine acknowledge <event-id> --reason <text>", nil
			}
		}
		last := page[len(page)-1]
		after = &sqlite.QuarantineKey{Lamport: last.Lamport, EventID: last.EventID}
	}
}
