// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// TestSyncService_ListQuarantined_IncludesRelayRowsWithPayload: records relay
// ingest skipped are listed with source relay, their stream position and the
// payload as received; a payload that is not UTF-8 is shown with replacement
// characters while its digest covers the exact bytes (MTIX-268).
func TestSyncService_ListQuarantined_IncludesRelayRowsWithPayload(t *testing.T) {
	svc, st, _ := newTestSyncService(t)
	ctx := context.Background()
	for rs, payload := range map[uint64][]byte{3: []byte(`{"a":1}`), 5: []byte("bad \xff bytes")} {
		require.NoError(t, st.RecordRelayQuarantine(ctx, sqlite.RelayQuarantineRecord{
			RelayQuarantineKey: sqlite.RelayQuarantineKey{PeerID: "fedcba9876543210", PubEpoch: 1, RS: rs},
			Reason:             "why", Payload: payload, SeenAt: "2026-11-13T01:00:00Z",
		}))
	}
	got, err := svc.ListQuarantined(ctx)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, service.QuarantineSourceRelay, got[0].Source)
	require.Equal(t, uint64(3), got[0].Relay.RS)
	require.Equal(t, `{"a":1}`, got[0].Relay.Payload)
	require.Equal(t, "open", got[0].Relay.State)
	require.Contains(t, got[1].Relay.Payload, "�")
	require.NotEqual(t, got[0].Handle, got[1].Handle)
	require.Len(t, got[0].Handle, service.QuarantineHandleLen)

	// A relay row is not an acknowledgeable pulled event.
	_, err = svc.ResolveQuarantineHandle(ctx, got[0].Handle)
	require.Error(t, err)
}
