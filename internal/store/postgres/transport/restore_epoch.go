// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"context"
	"fmt"

	"github.com/hyper-swe/mtix/internal/sync/redact"
	"github.com/jackc/pgx/v5"
)

// The hub RESTORE-EPOCH (ADR-003 §15, Addendum A) — the un-forgeable gate for
// the restore-collision discriminator (Option B, §6.1).
//
// restore_epoch is a single monotonic counter (starts 0) advanced ONLY by an
// explicit operator action (MarkRestored, surfaced as `mtix sync mark-restored`,
// a documented restore-from-backup runbook step). No push path advances it, so
// CLIENTS CANNOT ADVANCE IT — the property that makes the discriminator
// trust-minimizing: a compromised client cannot manufacture a restore window
// during normal operation (§15).

// CurrentRestoreEpoch returns the hub's current restore_epoch (ADR-003 §15).
// Zero is the no-restore-ever baseline. It reads the sync_hub_state singleton;
// a hub migrated by an older CLI that predates this table reads as 0 (the
// detector then treats every collision as a same-epoch race — ordinary
// renumber, never Option B). Parameterized SQL; errors redact any DSN.
func (p *Pool) CurrentRestoreEpoch(ctx context.Context) (int64, error) {
	if p == nil || p.p == nil {
		return 0, fmt.Errorf("CurrentRestoreEpoch: pool not open")
	}
	return readRestoreEpoch(ctx, p.p)
}

// MarkRestored advances the hub restore_epoch by one and returns the new value
// (ADR-003 §15). This is the OPERATOR's out-of-band restore-from-backup step:
// after restoring the hub from a backup, the operator runs it so that every
// surviving create's stamp falls into an EARLIER epoch than every create
// accepted afterward — opening the restore window in which a cross-epoch
// settled-vs-settled collision is classified as Option B (§6.1) instead of an
// ordinary renumber (§6).
//
// It is deliberately the ONLY mutator of the counter: no client/push path
// calls it, so a client cannot advance the epoch (§15 threat model). The bump
// is a single UPDATE under the row's own lock; the operator action is
// supervised and single-flight by nature, so no extra locking is needed.
// Parameterized SQL; errors redact any DSN.
func (p *Pool) MarkRestored(ctx context.Context) (int64, error) {
	if p == nil || p.p == nil {
		return 0, fmt.Errorf("MarkRestored: pool not open")
	}
	var epoch int64
	err := p.p.QueryRow(ctx, `
		UPDATE sync_hub_state
		   SET restore_epoch = restore_epoch + 1
		 WHERE id = TRUE
		RETURNING restore_epoch`,
	).Scan(&epoch)
	if err != nil {
		return 0, fmt.Errorf("MarkRestored: %s", redact.DSN(err.Error()))
	}
	return epoch, nil
}

// epochReader is the read surface shared by the pool and an in-flight tx so the
// push path can read the epoch inside its own transaction (a consistent
// snapshot with the inserts it stamps) while CurrentRestoreEpoch reads via the
// pool.
type epochReader interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// readRestoreEpoch reads the singleton restore_epoch via any epochReader
// (ADR-003 §15). Returns 0 when the singleton row is absent (a hub not yet
// migrated to 013) so the detector degrades to "no restore window".
func readRestoreEpoch(ctx context.Context, q epochReader) (int64, error) {
	var epoch int64
	err := q.QueryRow(ctx,
		`SELECT restore_epoch FROM sync_hub_state WHERE id = TRUE`).Scan(&epoch)
	switch err {
	case nil:
		return epoch, nil
	case pgx.ErrNoRows:
		return 0, nil
	default:
		return 0, fmt.Errorf("read restore_epoch: %s", redact.DSN(err.Error()))
	}
}
