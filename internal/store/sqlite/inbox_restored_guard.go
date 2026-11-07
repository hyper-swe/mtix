// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// RestoredInboxRecovery preserves this store while obtaining a separate hub copy.
const RestoredInboxRecovery = "This store holds restored mail that has no guaranteed hub copy. Keep this store intact. To obtain the hub board, in a new empty directory run 'mtix init PREFIX' with the hub project prefix, configure the same hub credentials via MTIX_SYNC_DSN, then run 'mtix sync clone'. In an operator terminal, read the restored mail in the original store with 'mtix inbox --include-held --agent AGENT'; hub contact can hold mail for an unregistered recipient. To keep the local board and rewrite its mirror instead, run 'mtix sync --fix'."

func restoredInboxPresent(ctx context.Context, q SyncEventReader) (bool, error) {
	query, err := restoredInboxQuery(ctx, q, `SELECT 0`, `SELECT EXISTS(SELECT 1 FROM inbox_restored_events)`)
	if err != nil {
		return false, err
	}
	var present bool
	if err = q.QueryRowContext(ctx, query).Scan(&present); err != nil {
		return false, fmt.Errorf("read restored inbox presence: %w", err)
	}
	return present, nil
}

// HasRestoredInbox reports mail that must not be discarded as replicated state.
func (s *Store) HasRestoredInbox(ctx context.Context) (bool, error) {
	return restoredInboxPresent(ctx, s.readDB)
}

func inboxSafeRecoveryGuide(ctx context.Context, q SyncEventReader, fallback string) (string, error) {
	present, err := restoredInboxPresent(ctx, q)
	if err != nil {
		return "", err
	}
	if present {
		return RestoredInboxRecovery, nil
	}
	return fallback, nil
}

// InboxSafeRecoveryGuide omits destructive instructions while restored mail exists.
func (s *Store) InboxSafeRecoveryGuide(ctx context.Context, fallback string) (string, error) {
	return inboxSafeRecoveryGuide(ctx, s.readDB, fallback)
}

func refuseDiscardRestoredInbox(ctx context.Context, q SyncEventReader) error {
	present, err := restoredInboxPresent(ctx, q)
	if err != nil {
		return err
	}
	if present {
		return fmt.Errorf("discard refused: %s: %w", RestoredInboxRecovery, model.ErrInvalidInput)
	}
	return nil
}
