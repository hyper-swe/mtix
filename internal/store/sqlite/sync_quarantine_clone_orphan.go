// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"fmt"
)

// CloneOrphanReason is the text every reason of an event quarantined by
// `mtix sync clone` starts with (MTIX-95.37.1): the hub holds an event for a
// task this clone never created, such as the claim or child create a client
// before 0.5.4 pushed under a number the hub had renumbered. The source stays
// "pull"; this prefix records the clone origin.
const CloneOrphanReason = "clone origin: orphaned dependent event, node not found"

// CountQuarantinedCloneOrphans returns how many pulled events the clone
// quarantined as orphaned dependents (MTIX-95.37.1). They are also counted by
// CountQuarantined.
func (s *Store) CountQuarantinedCloneOrphans(ctx context.Context) (int, error) {
	var n int
	// Pulled events whose reason starts with the clone-orphan marker.
	if err := s.QueryRow(ctx,
		`SELECT COUNT(*) FROM sync_quarantine WHERE source <> 'push' AND instr(reason, ?) = 1`,
		CloneOrphanReason).Scan(&n); err != nil {
		return 0, fmt.Errorf("count quarantined clone orphans: %w", err)
	}
	return n, nil
}
