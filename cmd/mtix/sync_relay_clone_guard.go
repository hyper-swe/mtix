// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// refuseSyncedRelayClone refuses a relay snapshot clone into a store that
// holds tasks and already syncs through a relay or hub (MTIX-112.17.16.43).
// The clone is a merge: it keeps local values with no sync event, then moves
// the ingest cursors past the events that would have corrected them, so the
// peer silently diverges. An empty store (the fresh joiner) and a store that
// does not sync import as before. It reads through the sync service and
// fails closed: evidence it cannot read refuses. It runs before any write or
// cursor advance.
//
// The guard is not atomic with the import, and no lock could make it so: the
// relay tick and the daemon take no lock that a clone could share (the push
// lock guards hub pushes only). The window is harmless for the case the guard
// exists for. A store that already holds tasks and syncs is refused here and
// stays refused, because a tick only adds sync evidence. An empty store that
// a daemon tick fills between the guard and the import is a fresh joiner
// whose tick ingests events the snapshot already stamps; the merge then finds
// them as idempotent no-ops or lists a conflict and writes nothing. The
// recovery still tells the joiner to stop any daemon in the new project
// before it clones.
func refuseSyncedRelayClone(ctx context.Context) error {
	if app.syncSvc == nil {
		return fmt.Errorf("relay clone failed: %w", errors.New("the sync service is not initialized"))
	}
	holds, err := app.syncSvc.StoreHoldsTasks(ctx)
	if err != nil {
		return fmt.Errorf("relay clone failed: %w", err)
	}
	if !holds {
		return nil
	}
	synced, err := storeSyncs(ctx)
	if err != nil {
		return fmt.Errorf("relay clone failed: %w", err)
	}
	counts, err := hubGuardCounts(ctx)
	if err != nil {
		return fmt.Errorf("relay clone failed: %w: %w", err, sqlite.ErrRelayCloneOnSyncedStore)
	}
	if !synced && !counts {
		return nil
	}
	return fmt.Errorf("relay clone failed: this store already holds tasks and syncs, and a snapshot merge keeps "+
		"local values with no sync event, then skips the events that would correct them; nothing was imported. %s: %w",
		sqlite.RelayCloneOnSyncedWays(), sqlite.ErrRelayCloneOnSyncedStore)
}
