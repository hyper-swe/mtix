// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// NFR-2.8: every production SQLite open goes through openSQLite, and
// TestSQLiteDSN_NonTestSourcesUseOneBuilder holds that it holds the only sql.Open, so guarding
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

// NFR-2.8: only zero and one byte are "empty" to SQLite; a 2-byte file is a
// (corrupt) database that SQLite will not reset, so the guard leaves it to the
// normal open path and does not refuse it.
func TestGuardEmptyDatabaseWAL_TwoByteDatabaseBesideWAL_IsNotRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.db")
	require.NoError(t, os.WriteFile(path, make([]byte, 2), 0o600))
	require.NoError(t, os.WriteFile(path+"-wal", []byte("committed"), 0o600))
	require.NoError(t, guardEmptyDatabaseWAL(path))
}

// NFR-2.8: a WAL that cannot be examined is not "no WAL here". A link that
// points at itself makes the stat fail with something other than not-exist.
func TestGuardEmptyDatabaseWAL_WALStatError_Refuses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks require Windows privileges")
	}
	path := filepath.Join(t.TempDir(), "x.db")
	require.NoError(t, os.WriteFile(path, make([]byte, 100), 0o600))
	require.NoError(t, os.Symlink(filepath.Base(path)+"-wal", path+"-wal"))
	require.Error(t, guardEmptyDatabaseWAL(path))
}

// TestGuardWALWithStat_DatabaseStatError_RefusesAndLeavesWAL pins NFR-2.8 for
// MTIX-112.50.6: a database that cannot be examined is not a healthy
// database. The stat of the resolved database alone fails with something
// other than not-exist while its nonempty -wal stats fine; the guard that
// every open runs first (openSQLite calls guardEmptyDatabaseWAL) refuses,
// and the WAL is left byte-for-byte intact.
func TestGuardWALWithStat_DatabaseStatError_RefusesAndLeavesWAL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.db")
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	committed := []byte("committed\x00wal\xffbytes")
	require.NoError(t, os.WriteFile(path+"-wal", committed, 0o600))
	resolved, err := resolveDatabasePath(path)
	require.NoError(t, err)

	injected := errors.New("injected database stat failure")
	var failed int
	statDatabase := func(name string) (os.FileInfo, error) {
		if name == resolved {
			failed++
			return nil, injected
		}
		return os.Stat(name)
	}

	err = guardWALWithStat(path, storeRefusalAdvice, statDatabase)

	require.ErrorIs(t, err, injected)
	require.Positive(t, failed)
	wal, readErr := os.ReadFile(path + "-wal")
	require.NoError(t, readErr)
	require.Equal(t, committed, wal)
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
