// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// TestScrubSyncText_SyncPasswordEnv_RemovedFromOutput verifies a password
// supplied only through MTIX_SYNC_PASSWORD is removed by the central
// scrubber, though no DSN carries it (MTIX-87).
func TestScrubSyncText_SyncPasswordEnv_RemovedFromOutput(t *testing.T) {
	for name, tt := range map[string]struct{ pw, text string }{
		"plain":       {"Sekr3tPW-9xQ", "connect failed: Sekr3tPW-9xQ rejected"},
		"special":     {`p/w?#% @:'\Zq7`, `connect failed: p/w?#% @:'\Zq7 rejected`},
		"quote chars": {`a'b"c\d-Zq9`, `connect failed: a'b"c\d-Zq9 rejected`},
		// A short password is removed only where it stands as a password
		// (see redact.Known), so it is printed as a setting here.
		"short": {"x9Zq", "connect failed: host=db.example.com password=x9Zq rejected"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(transport.EnvDSN, "")
			t.Setenv(transport.EnvPassword, tt.pw)
			require.NotContains(t, scrubSyncText(tt.text), tt.pw)
		})
	}
	t.Setenv(transport.EnvPassword, "")
	require.Equal(t, "plain text", scrubSyncText("plain text"), "no password set: output is unchanged")
}
