// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/testutil"
)

// UID rendering must protect human output without changing JSON encoding,
// including encoding/json's existing replacement of invalid UTF-8.
func TestInvalidNodeIDDetail_HostileUIDs_TextSafeJSONUnchanged(t *testing.T) {
	saveAndResetApp(t)
	for _, fx := range testutil.HostileIDs() {
		t.Run(fx.Name, func(t *testing.T) {
			rows := []sqlite.InvalidNodeID{{ID: "TEST-0", UID: fx.Value}}
			app.jsonOutput = false
			requirePrintableASCII(t, invalidNodeIDDetail(rows), fx.Value)
			require.Equal(t, fx.Value, rows[0].UID)
			app.jsonOutput = true
			got, err := json.Marshal(invalidNodeIDDetail(rows))
			require.NoError(t, err)
			want, err := json.Marshal(fmt.Sprintf("1 local tasks have an id outside the node id grammar: %q (uid %s)", "TEST-0", fx.Value))
			require.NoError(t, err)
			require.Equal(t, string(want), string(got))
		})
	}
}

func TestPrintUIDRepairReports_HostileUIDs_TextSafeJSONUnchanged(t *testing.T) {
	for _, fx := range testutil.HostileIDs() {
		t.Run(fx.Name, func(t *testing.T) {
			for _, mismatch := range []transport.UIDMismatch{
				{NodeID: "TEST-1", HubUID: fx.Value, LocalUID: "local"},
				{NodeID: "TEST-1", HubUID: "hub", LocalUID: fx.Value},
			} {
				reports := []transport.UIDRepairReport{{Project: "TEST", Mismatched: []transport.UIDMismatch{mismatch}}}
				before, err := json.MarshalIndent(reports, "", "  ")
				require.NoError(t, err)
				var stdout, stderr bytes.Buffer
				printUIDRepairReports(&stdout, &stderr, reports)
				requirePrintableASCII(t, stderr.String(), fx.Value)
				after, err := json.MarshalIndent(reports, "", "  ")
				require.NoError(t, err)
				require.Equal(t, string(before), string(after))
				require.Equal(t, mismatch, reports[0].Mismatched[0])
			}
		})
	}
}
