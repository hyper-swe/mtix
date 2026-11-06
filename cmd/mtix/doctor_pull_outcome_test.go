// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHubContactRow_MalformedContactTimestamp_NoStoredValueLeaks(t *testing.T) {
	src := daemonOutcomeFixture(t)
	secret := "postgres://private:timestamp-secret@private.invalid/db"
	require.NoError(t, src.state.WriteJSON("hubcontact-"+src.key(), map[string]any{
		"at": secret, "ok": false, "route": "poll", "error_class": "auth"}))
	row := hubContactRow(src, true, src.now())
	raw, err := json.Marshal(row)
	require.NoError(t, err)
	require.False(t, row.Pass)
	require.Contains(t, row.Fix, "set XDG_CONFIG_HOME")
	require.NotContains(t, row.Detail, secret)
	require.NotContains(t, string(raw), "timestamp-secret")
}
