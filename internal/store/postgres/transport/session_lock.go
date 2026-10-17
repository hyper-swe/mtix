// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hyper-swe/mtix/internal/sync/redact"
	"github.com/jackc/pgx/v5/pgxpool"
)

// sessionUnlockTimeout bounds the release of a session advisory lock. The
// release runs on a context of its own, so a cancelled caller still sends
// the unlock (MTIX-107.85).
const sessionUnlockTimeout = 5 * time.Second

// errLockNotHeld reports that pg_advisory_unlock returned false: the
// session did not hold the lock it was asked to release.
var errLockNotHeld = errors.New("the session did not hold the lock")

// releaseSessionLock releases the session advisory lock on
// AdvisoryLockKey that conn holds. It does not use ctx's cancellation or
// deadline, only its values, so a lock taken by a run whose context was
// cancelled is still released. When the unlock fails or returns false,
// it closes conn so the pool destroys it on Release instead of reusing a
// session that may still hold the lock; closing the session releases
// every session lock it holds. The returned error reports the failure.
func releaseSessionLock(ctx context.Context, conn *pgxpool.Conn) error {
	uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sessionUnlockTimeout)
	defer cancel()

	var released bool
	err := conn.QueryRow(uctx,
		`SELECT pg_advisory_unlock(hashtext($1))`, AdvisoryLockKey,
	).Scan(&released)
	if err == nil && released {
		return nil
	}
	if err == nil {
		err = errLockNotHeld
	}
	if closeErr := conn.Conn().Close(uctx); closeErr != nil {
		err = errors.Join(err, closeErr)
	}
	return fmt.Errorf("release session lock (connection closed): %s", redact.DSN(err.Error()))
}
