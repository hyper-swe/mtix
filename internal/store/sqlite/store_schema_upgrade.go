// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"fmt"
)

// addUIDColumnPreV3 adds nodes.uid to a pre-v3 database (MTIX-30.1).
// No-op on fresh DBs (schemaSQL already has the column) and on v3+. A
// "duplicate column" error from a re-run after an interrupted migration
// is tolerated.
func (s *Store) addUIDColumnPreV3(ctx context.Context, existingVersion int) error {
	if existingVersion == 0 || existingVersion >= 3 {
		return nil
	}
	if _, err := s.writeDB.ExecContext(ctx, migrateV2ToV3SQL); err != nil &&
		!containsString(err.Error(), "duplicate column name") {
		return fmt.Errorf("migrate v2 -> v3 (add nodes.uid): %w", err)
	}
	return nil
}

// addSyncEventUIDColumnPreV4 adds sync_events.uid to a pre-v4 database
// (MTIX-30.6 / ADR-003 §3, §7 Phase 3). No-op on fresh DBs (schemaSQL
// already has the column) and on v4+. A "duplicate column" error from a
// re-run after an interrupted migration is tolerated, mirroring
// addUIDColumnPreV3. Legacy rows keep uid NULL; apply falls back to
// node_id for them (dual-carry), so no row backfill is needed here.
func (s *Store) addSyncEventUIDColumnPreV4(ctx context.Context, existingVersion int) error {
	// Only v2/v3 DBs carry a pre-v4 sync_events table that survives into
	// this init and needs the ALTER. A v1 DB has its legacy sync_events
	// DROPped by the v1->v2 step above and then re-created (with uid) by
	// schemaSQL, so it needs no ALTER; a fresh (0) or already-v4 DB needs
	// none either.
	if existingVersion < 2 || existingVersion >= 4 {
		return nil
	}
	if _, err := s.writeDB.ExecContext(ctx, migrateV3ToV4SQL); err != nil &&
		!containsString(err.Error(), "duplicate column name") {
		return fmt.Errorf("migrate v3 -> v4 (add sync_events.uid): %w", err)
	}
	s.logger.Info("schema_migrated",
		"event", "schema_migrated", "from_version", existingVersion, "to_version", 4,
		"added", "sync_events.uid")
	return nil
}
