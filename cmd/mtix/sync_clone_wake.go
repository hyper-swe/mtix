// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"database/sql"
	"io"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// retryCloneQuarantinedEvents treats clone-origin retries as bootstrap batches
// while retaining the ordinary pull retry policy (FR-18.20, FR-20).
func retryCloneQuarantinedEvents(ctx context.Context, st *sqlite.Store, limit int) (quarantineRetry, error) {
	in := newPullIngest(io.Discard)
	in.cloneBootstrap = true
	return retryQuarantinedEvents(ctx, in, st, limit)
}

// finishClonePullCursor retires bootstrap intent with the completed clone's
// durable cursor. A crash cannot leave a completed clone granting more seeds.
func finishClonePullCursor(ctx context.Context, st *sqlite.Store, cursor transport.PullCursor) error {
	return st.WithTx(ctx, func(tx *sql.Tx) error {
		if err := writePullCursor(ctx, tx, cursor); err != nil {
			return err
		}
		return sqlite.CompleteCloneWakeBootstrap(ctx, tx)
	})
}
