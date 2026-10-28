// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Push transaction bounds (MTIX-107.29). Both stay well below the 15-minute
// late-event sweep overlap. The idle bound kills a suspended client quickly;
// the transaction bound (Postgres 17+ only) also covers an active push and is
// larger so a 100-event batch over a slow link, at a few round trips per
// event plus registry lock waits, finishes inside it.
const (
	pushIdleTimeout        = "1min"
	pushTransactionTimeout = "10min"
)

// setPushTimeouts bounds suspended and over-long push transactions below the
// sweep's 15-minute overlap. pg_settings omits transaction_timeout on older
// hubs; joining on it keeps an unsupported setting from aborting the
// transaction. Rule 1a: names and values are bound arrays passed to
// set_config, local to this transaction only.
func setPushTimeouts(ctx context.Context, tx pgx.Tx) error {
	// Set each bound setting that this server exposes, transaction-locally.
	_, err := tx.Exec(ctx, `SELECT set_config(b.name, b.value, true)
FROM unnest($1::text[], $2::text[]) AS b(name, value)
JOIN pg_catalog.pg_settings s ON s.name = b.name`,
		[]string{"idle_in_transaction_session_timeout", "transaction_timeout"},
		[]string{pushIdleTimeout, pushTransactionTimeout})
	if err != nil {
		return fmt.Errorf("set push transaction timeouts: %w", err)
	}
	return nil
}
