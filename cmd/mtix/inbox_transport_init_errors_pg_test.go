// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package main

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func TestRestoredInbox_PostgresInitPreservesOrdinaryErrors(t *testing.T) {
	for _, tc := range []struct {
		name, prefix string
		restored     bool
	}{
		{"ordinary-divergence", "TEST", false}, {"ordinary-invalid-prefix", "", false}, {"restored-invalid-prefix", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hub := openCmdHub(t)
			require.NoError(t, registerProjectOnHub(t.Context(), hub, "TEST", strings.Repeat("f", 64)))
			initTestApp(t)
			if tc.restored {
				_, file := restoredInboxSource(t)
				_, err := app.store.Import(t.Context(), file, sqlite.ImportModeMerge, false)
				require.NoError(t, err)
			}
			hp, hh, err := readHubFirstEventHash(t.Context(), hub, tc.prefix)
			require.NoError(t, err)
			cause := sqlite.DetectDivergentHistory(tc.prefix, "local", hp, hh)
			require.Error(t, cause)
			want := wrapSyncErr(io.Discard, "divergence", cause)
			var out, diagnostic bytes.Buffer
			err = claimProjectOnHub(t.Context(), &out, &diagnostic, hub, localFirstEvent{tc.prefix, "local"})
			require.EqualError(t, err, want.Error(), "keep original classification and do not duplicate divergence sentinel text")
		})
	}
}
