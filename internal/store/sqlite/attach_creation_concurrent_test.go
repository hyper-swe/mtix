// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
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
			if legacy {
				_, err := newTestDB(t, path).Exec("CREATE TABLE meta(key TEXT PRIMARY KEY,value TEXT NOT NULL)")
				require.NoError(t, err)
			}
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
			for id := range ids {
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
		})
	}
}
