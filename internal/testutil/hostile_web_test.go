// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package testutil

// The browser snapshot must match child A's fixtures after Go JSON encoding.
// Invalid UTF-8 is replaced by U+FFFD by encoding/json at the web boundary.
import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHostileWebFixtures_MatchSharedCatalog(t *testing.T) {
	data, err := os.ReadFile("testdata/hostile-web.json")
	require.NoError(t, err)
	expected, err := json.Marshal(map[string]any{"ids": HostileIDs(), "text": HostileText()})
	require.NoError(t, err)
	require.JSONEq(t, string(expected), string(data), "regenerate browser snapshot from HostileIDs and HostileText")
}
