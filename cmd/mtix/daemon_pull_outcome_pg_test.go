// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

func TestRunDaemon_PGPull_FailureSuccessFailureReplacesOutcome(t *testing.T) {
	_ = openCmdHub(t)
	goodDSN := os.Getenv(transport.EnvDSN)
	src := daemonOutcomeFixture(t)
	useClientVersion(t, "0.6.0")
	setPeerMeta(t, "meta.sync.project_prefix", "TEST")
	badDSN, _ := daemonErrorHub(t, "28P01")
	for _, tc := range []struct {
		dsn string
		ok  bool
	}{{badDSN, false}, {goodDSN, true}, {badDSN, false}} {
		t.Setenv(transport.EnvDSN, tc.dsn)
		_, stderr := runOutcomeDaemonOnce(t)
		rec, present, err := src.state.ReadHubContact(src.key())
		require.NoError(t, err)
		require.True(t, present)
		require.Equal(t, tc.ok, rec.OK, stderr)
		row := hubContactRow(src, true, src.now())
		raw, err := json.Marshal(row)
		require.NoError(t, err)
		require.NotContains(t, string(raw), "daemon-outcome-secret")
		if tc.ok {
			require.Contains(t, row.Detail, "last hub contact succeeded")
			require.NotContains(t, string(raw), `"error_class"`)
		} else {
			require.Contains(t, string(raw), `"error_class":"auth"`)
		}
	}
}
