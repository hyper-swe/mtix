// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// withRestoredInboxGuidance changes only a divergent store's unsafe remedy.
func withRestoredInboxGuidance(ctx context.Context, store *sqlite.Store, cause error) error {
	if !errors.Is(cause, model.ErrSyncDivergentHistory) {
		return cause
	}
	guide, err := store.InboxSafeRecoveryGuide(ctx, cause.Error())
	if err != nil {
		return fmt.Errorf("read sync-init recovery guidance: %w", err)
	}
	if guide == cause.Error() {
		return cause
	}
	return fmt.Errorf("%s: %w", guide, model.ErrSyncDivergentHistory)
}
