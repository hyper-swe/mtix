// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"database/sql"
	"testing"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

// FR-19.1: real PG init/clone must not infer divergence or recommend deleting restored mail.
func TestRestoredInbox_PostgresInitAndClone(t *testing.T) {
	for _, scenario := range []struct {
		name             string
		populated, mixed bool
	}{
		{"empty-hub-mail-only", false, false}, {"existing-hub-mail-only", true, false},
		{"empty-hub-mixed", false, true}, {"existing-hub-mixed", true, true},
	} {
		populated := scenario.populated
		name := scenario.name
		t.Run(name, func(t *testing.T) {
			hub := openCmdHub(t)
			source, file := restoredInboxSource(t)
			prefix, hash, err := source.GetOrComputeLocalFirstEventHash(t.Context())
			require.NoError(t, err)
			if populated {
				require.NoError(t, registerProjectOnHub(t.Context(), hub, prefix, hash))
				events, err := source.ReadPendingEvents(t.Context(), 100)
				require.NoError(t, err)
				pushToHub(t, hub, events...)
			}
			initTestApp(t)
			_, err = app.store.Import(t.Context(), file, sqlite.ImportModeMerge, false)
			require.NoError(t, err)
			if scenario.mixed {
				events, err := source.ReadPendingEvents(t.Context(), 100)
				require.NoError(t, err)
				require.NoError(t, app.store.WithTx(t.Context(), func(tx *sql.Tx) error { return app.store.IdempotentApply(t.Context(), tx, events[0]) }))
			}
			var out, diagnostic bytes.Buffer
			err = runSyncInit(t.Context(), &out, &diagnostic, nil, transport.Options{InsecureTLS: true})
			require.NoError(t, err, diagnostic.String())
			if scenario.mixed {
				require.Contains(t, out.String(), "local project TEST")
			} else {
				require.Contains(t, out.String(), "no local events yet")
			}
			require.NotContains(t, out.String()+diagnostic.String(), "discard-local")
			_, hubHash, err := readHubFirstEventHash(t.Context(), hub, prefix)
			require.NoError(t, err)
			if populated || scenario.mixed {
				require.Equal(t, hash, hubHash)
			} else {
				require.Empty(t, hubHash)
			}
			if populated && !scenario.mixed {
				out.Reset()
				diagnostic.Reset()
				require.NoError(t, runSyncClone(t.Context(), &out, &diagnostic, nil, transport.Options{InsecureTLS: true}, false, 1), diagnostic.String())
				require.NotContains(t, out.String()+diagnostic.String(), "discard-local")
			}
			mail, err := app.store.InboxList(t.Context(), "worker")
			require.NoError(t, err)
			require.Len(t, mail, 1)
		})
	}
}
