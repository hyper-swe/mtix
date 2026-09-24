// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// TestCloudPath_HubPrivilegesReport runs the read-only `mtix sync harden`
// dry run against the test hub, which on the cloud-contract gate is a real
// provider's catalog (MTIX-95.1). It asserts only that the report is well
// formed: whether a provider's roles hold access is not something this
// suite controls, so there is no PASS assertion, and a dry run changes
// nothing.
func TestCloudPath_HubPrivilegesReport(t *testing.T) {
	openCmdHub(t) // skips when MTIX_PG_TEST_DSN is unset; migrates the hub
	initTestApp(t)
	t.Setenv("MTIX_SYNC_HOOK", "")
	app.jsonOutput = true

	var stdout, stderr bytes.Buffer
	err := runSyncHarden(context.Background(), &stdout, &stderr, nil,
		transport.Options{InsecureTLS: true}, hardenFlags{})
	if err != nil {
		require.Equal(t, 2, exitCodeForError(err), "only pending changes may fail a dry run: %v", err)
	}
	var result struct {
		Applied bool                       `json:"applied"`
		Before  map[string]json.RawMessage `json:"before"`
	}
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &result), stdout.String())
	require.False(t, result.Applied)
	for _, key := range []string{"schema", "owners", "findings"} {
		require.Contains(t, result.Before, key)
	}
}
