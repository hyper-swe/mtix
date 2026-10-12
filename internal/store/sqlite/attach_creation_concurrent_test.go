// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"database/sql"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
)

// Concurrent opens must share creation identity without enrolling existing schemas.
func TestStoreInstanceID_ConcurrentOpen_PreservesSingleIdentity(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "legacy"}[legacy], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "store.db")
			prepareConcurrentCreation(t, path, legacy)
			start := make(chan struct{})
			ids := make(chan string, 4)
			errs := make(chan error, 8)
			var wg sync.WaitGroup
			for range 4 {
				wg.Go(func() {
					<-start
					s, err := sqlite.New(path, slog.Default())
					if err != nil {
						errs <- err
						return
					}
					id, err := s.StoreInstanceID(t.Context())
					if err != nil {
						errs <- err
					}
					ids <- id
					errs <- s.Close()
				})
			}
			close(start)
			wg.Wait()
			close(ids)
			close(errs)
			for err := range errs {
				require.NoError(t, err)
			}
			var first string
			observed := 0
			for id := range ids {
				observed++
				if legacy {
					assert.Empty(t, id)
				} else {
					require.NotEmpty(t, id)
					if first == "" {
						first = id
					}
					assert.Equal(t, first, id)
				}
			}
			require.Equal(t, 4, observed)
		})
	}
}

// Prepare journal state without assigning a creation identity or opening the Store.
func prepareConcurrentCreation(t *testing.T, path string, legacy bool) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()
	// No WAL pre-step (MTIX-147): enabling WAL here hid SQLITE_BUSY on the
	// journal_mode switch. The simultaneous opens must perform the switch
	// themselves; TestNew_ConcurrentFirstOpenNoWALPrestep_AllSucceedWithOneIdentity
	// covers the brand-new-file case.
	expectedTables := 0
	if legacy {
		_, err := db.Exec("CREATE TABLE meta(key TEXT PRIMARY KEY,value TEXT NOT NULL)")
		require.NoError(t, err)
		expectedTables = 1
		var identities int
		require.NoError(t, db.QueryRow("SELECT count(*) FROM meta WHERE key='store_instance_id'").Scan(&identities))
		require.Zero(t, identities)
	}
	var tables int
	require.NoError(t, db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table'").Scan(&tables))
	require.Equal(t, expectedTables, tables)
}
