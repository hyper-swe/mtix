// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"fmt"
)

// SetHubEvidenceSource installs the reader of the local hub or relay
// evidence (a configured relay directory, hub endpoint or stored DSN, or an
// exported DSN), the same evidence the replace guard of the command line
// counts (MTIX-107.31.7, MTIX-107.31.8). The automatic import records the
// durable part as the sticky hub-connected mark before it imports, so the
// replace guard counts a relay whose first publish did not finish, and treats
// the store as synced while an exported DSN is set. A source that returns an
// error stops the automatic import. nil, the default, means no evidence.
func (s *SyncService) SetHubEvidenceSource(read func() (HubEvidence, error)) { s.hubEvidence = read }

// noteHubEvidence records the durable local hub or relay evidence as the
// sticky hub-connected mark (HubMarkService.HubConnected), so the store-level
// guard sees a store whose relay publisher wrote its records but not its
// cursor (the push cursor is written only after the append). It reports
// whether the evidence is only transient: an exported or refused DSN makes the
// automatic import treat the store as synced for this import, and writes no
// mark. With no source installed it does nothing. It fails closed: when the
// evidence cannot be read, or the mark cannot be read or written, the
// automatic import stops, importing nothing (the durable evidence read before
// a failure is still recorded) (MTIX-107.31.7, MTIX-107.31.8).
func (s *SyncService) noteHubEvidence(ctx context.Context) (transient bool, err error) {
	if s.hubEvidence == nil {
		return false, nil
	}
	ev, readErr := s.hubEvidence()
	// The durable part read so far is recorded even when the rest cannot be
	// read, as before this change; the import then stops with the cause.
	_, markErr := NewHubMarkService(s.store).HubConnected(ctx, ev)
	if readErr != nil {
		err := fmt.Errorf("auto-import: read the hub evidence: %w", readErr)
		if markErr != nil {
			err = errors.Join(err, fmt.Errorf("auto-import: record the hub evidence: %w", markErr))
		}
		return false, err
	}
	if markErr != nil {
		return false, fmt.Errorf("auto-import: record the hub evidence: %w", markErr)
	}
	return ev.EnvDSNExported || ev.DSNRefused, nil
}
