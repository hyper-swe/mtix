// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"fmt"
)

// SetHubEvidenceSource installs the reader of the local hub or relay
// evidence (a configured relay directory, hub endpoint or stored DSN), the
// same evidence the sticky hub-connected mark accepts (MTIX-107.31.7). The
// automatic import records it as the mark before it imports, so the replace
// guard counts a relay whose first publish did not finish. nil, the default,
// means no evidence.
func (s *SyncService) SetHubEvidenceSource(read func() HubEvidence) { s.hubEvidence = read }

// noteHubEvidence records the local hub or relay evidence as the sticky
// hub-connected mark (HubMarkService.HubConnected), as the replace guards of
// the CLI count it, so the store-level guard sees a store whose relay
// publisher wrote its records but not its cursor (the push cursor is written
// only after the append, so a crash or a failed advance leaves the store
// reading as never synced). With no source installed it does nothing. It
// fails closed: when the evidence or the mark cannot be read or written the
// automatic import stops, importing nothing (MTIX-107.31.7).
func (s *SyncService) noteHubEvidence(ctx context.Context) error {
	if s.hubEvidence == nil {
		return nil
	}
	if _, err := NewHubMarkService(s.store).HubConnected(ctx, s.hubEvidence()); err != nil {
		return fmt.Errorf("auto-import: record the hub evidence: %w", err)
	}
	return nil
}
