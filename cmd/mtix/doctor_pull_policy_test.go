// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

func TestHubContactRow_ManualOrNoHub_HistoricalFailureIsNotPermanentFail(t *testing.T) {
	for _, hub := range []bool{false, true} {
		t.Run(map[bool]string{false: "no hub", true: "manual"}[hub], func(t *testing.T) {
			src := daemonOutcomeFixture(t)
			require.NoError(t, src.state.RecordHubContact(src.key(), oplocal.HubContact{At: src.now(), Route: "poll", ErrorClass: "auth"}))
			if hub {
				_, err := app.configSvc.Set("sync.hub_contact", "manual")
				require.NoError(t, err)
			}
			row := hubContactRow(src, hub, src.now())
			require.True(t, row.Pass, "a disabled automatic contact is not a permanent failing check")
			require.Contains(t, row.Detail, "last pull failed")
			require.NotNil(t, row.LastContact, "historical evidence must remain visible")
			if !hub {
				require.NotContains(t, row.Fix, "mtix sync pull")
			}
		})
	}
}

func TestRunDaemon_LocalTLSPosture_PrintsLocalRecovery(t *testing.T) {
	src := daemonOutcomeFixture(t)
	dsn, calls := daemonErrorHub(t, "42501")
	t.Setenv(transport.EnvDSN, dsn)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var stdout, stderr bytes.Buffer
	require.NoError(t, runDaemon(ctx, &stdout, &stderr, nil, transport.Options{}, 5,
		daemonPassControl{observed: func(bool) { cancel() }}))
	require.Zero(t, calls.Load(), "TLS policy refuses before a socket is opened")
	row := hubContactRow(src, true, src.now())
	require.Contains(t, row.Detail, "tls")
	require.Contains(t, row.Fix, "sslmode=verify-full")
	require.Contains(t, row.Fix, "sslrootcert")
	require.NotContains(t, row.Fix, "ask the hub operator")
}
