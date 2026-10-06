// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package ingest_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/relay/ingest"
)

// Relay's shipped ingest path must retain the real store's injected clock.
func TestIngestAll_StoreClock_StampsAppliedRows(t *testing.T) {
	r := newPeerRig(t)
	fixed := time.Date(2001, 2, 3, 4, 5, 6, 123456789, time.UTC)
	r.store.SetClock(func() time.Time { return fixed })
	e := event(t, model.OpCreateNode, "PROJ-1", 1, &model.CreateNodePayload{Title: "remote"})
	r.publish(t, fleetKey, marshalEvents(t, e)...)
	in, err := ingest.New(r.config())
	require.NoError(t, err)
	stats, err := in.IngestAll(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, stats.Applied)
	var applied, updated string
	require.NoError(t, r.store.QueryRow(context.Background(),
		`SELECT applied_at FROM applied_events WHERE event_id = ?`, e.EventID).Scan(&applied))
	require.Equal(t, fixed.Format(time.RFC3339Nano), applied)
	require.NoError(t, r.store.QueryRow(context.Background(),
		`SELECT updated_at FROM nodes WHERE id = ?`, e.NodeID).Scan(&updated))
	require.Equal(t, fixed.Format(time.RFC3339), updated)
}

func (f *fakeStore) IdempotentApply(ctx context.Context, tx *sql.Tx, e *model.SyncEvent) error {
	return f.inner.IdempotentApply(ctx, tx, e)
}
