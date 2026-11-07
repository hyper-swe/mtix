// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// NFR-2.8: every production SQLite open goes through openSQLite, and
// TestDSN_SingleBuilder holds that it holds the only sql.Open, so guarding
// the helper guards backup, recover, the inbox readers and attach alike.
func TestOpenSQLite_ShortDatabaseBesideWAL_RefusesAndLeavesFiles(t *testing.T) {
	for _, size := range []int{0, 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "x.db")
			require.NoError(t, os.WriteFile(path, make([]byte, size), 0o600))
			require.NoError(t, os.WriteFile(path+"-wal", []byte("committed"), 0o600))
			db, err := openSQLite(path, "mode=ro")
			if db != nil {
				require.NoError(t, db.Close())
			}
			require.ErrorIs(t, err, model.ErrCorrupted)
			wal, readErr := os.ReadFile(path + "-wal")
			require.NoError(t, readErr)
			require.Equal(t, "committed", string(wal))
		})
	}
}

// fakeFileInfo lets a test state the size the source had before salvage.
type fakeFileInfo struct {
	os.FileInfo
	size int64
}

func (f fakeFileInfo) Size() int64 { return f.size }

// NFR-2.8: the source size is re-checked after the last SQLite read, so a size
// change, a shrink, a missing before-size or a failed stat is never "clean".
func TestCheckRecoveredSourceSize_ChangeShrinkUnknown_AreIncomplete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.db")
	require.NoError(t, os.WriteFile(path, make([]byte, 100), 0o600))
	info, err := os.Stat(path)
	require.NoError(t, err)
	cases := []struct {
		name       string
		path       string
		before     os.FileInfo
		incomplete bool
	}{
		{"unchanged", path, info, false},
		{"grew", path, fakeFileInfo{info, 10}, true},
		{"shrank", path, fakeFileInfo{info, 1000}, true},
		{"before unknown", path, nil, true},
		{"stat fails", path + ".gone", info, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := &RecoverResult{}
			checkRecoveredSourceSize(c.path, c.before, res)
			require.Equal(t, c.incomplete, res.InboxIncomplete)
			require.Equal(t, c.incomplete, len(res.Notes) > 0)
		})
	}
}

// The re-stat must run after the inbox salvage read, not before it.
func TestRecoverInboxJournal_SourceChangesDuringInboxRead_IsIncomplete(t *testing.T) {
	for _, newSize := range []int{150, 50} {
		t.Run(fmt.Sprint(newSize), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "s.db")
			require.NoError(t, os.WriteFile(path, make([]byte, 100), 0o600))
			before, err := os.Stat(path)
			require.NoError(t, err)
			r := inboxRecoverer{salvage: func(_ context.Context, p string, _ *RecoverResult, _ time.Duration) (*InboxJournal, error) {
				require.NoError(t, os.WriteFile(p, make([]byte, newSize), 0o600))
				return &InboxJournal{Version: 1, Events: []inboxJournalEvent{}}, nil
			}}
			res := &RecoverResult{}
			r.recover(t.Context(), path, nil, res, time.Second, before)
			require.True(t, res.InboxIncomplete)
		})
	}
}
