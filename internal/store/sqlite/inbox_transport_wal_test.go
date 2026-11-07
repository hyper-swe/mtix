// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"database/sql"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// FR-19.1: preserve a real crash-style snapshot with three committed WAL-only mail.
func crashMailWAL(t *testing.T) (string, []byte, int, int) {
	t.Helper()
	path, _, _ := damageMailFixture(t, "")
	setDamageCoverage(t, path, 300, 0)
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()
	db.SetMaxOpenConns(1)
	for _, q := range []string{`PRAGMA journal_mode=WAL`, `PRAGMA wal_autocheckpoint=0`, `PRAGMA wal_checkpoint(TRUNCATE)`} {
		_, err = db.Exec(q)
		require.NoError(t, err)
	}
	var secondFrame int
	for i := 1; i <= 3; i++ {
		_, err = db.Exec(`INSERT INTO sync_events(rowid,event_id,project_prefix,node_id,op_type,payload,wall_clock_ts,lamport_clock,vector_clock,author_id,author_machine_hash,created_at)
   VALUES(?,?,'PROJ','PROJ-1','comment','{"to":"recipient","body":"committed only in WAL"}',0,1,'{}','sender','','2026-11-05T00:00:00Z')`, 300+i, fmt.Sprintf("wal-mail-%d", i))
		require.NoError(t, err)
		if i == 1 {
			data, e := os.ReadFile(path + "-wal")
			require.NoError(t, e)
			page := int(binary.BigEndian.Uint32(data[8:12]))
			secondFrame = (len(data) - 32) / (24 + page)
		}
	}
	data, err := os.ReadFile(path + "-wal")
	require.NoError(t, err)
	page := int(binary.BigEndian.Uint32(data[8:12]))
	require.Greater(t, len(data), 32+(secondFrame+1)*(24+page))
	snap := filepath.Join(t.TempDir(), "crash.db")
	for _, suffix := range []string{"", "-wal", "-shm"} {
		raw, e := os.ReadFile(path + suffix)
		require.NoError(t, e)
		require.NoError(t, os.WriteFile(snap+suffix, raw, 0600))
	}
	return snap, data, page, secondFrame
}

// FR-19.1 / MTIX-112.48: SQLite can silently ignore committed mail after a bad WAL frame.
func TestRecover_WALReplayLoss_IsIncomplete(t *testing.T) {
	for _, mode := range []string{"first-checksum", "second-transaction-checksum", "second-checksum-word", "second-salt", "salt", "torn-tail", "header-checksum", "clean", "symlink-first-checksum", "symlink-clean"} {
		t.Run(mode, func(t *testing.T) {
			path, data, page, second := crashMailWAL(t)
			data, expected := damageCrashMailWAL(mode, data, page, second)
			require.NoError(t, os.WriteFile(path+"-wal", data, 0600))
			if strings.HasPrefix(mode, "symlink-") {
				if runtime.GOOS == "windows" {
					t.Skip("symlink creation requires separate Windows privileges")
				}
				alias := filepath.Join(t.TempDir(), "alias.db")
				require.NoError(t, os.Symlink(path, alias))
				path = alias
			}
			result := recoverDamageMail(t, path)
			require.Len(t, result.Export.InboxJournal.Events, expected)
			if mode == "clean" || mode == "symlink-clean" {
				require.False(t, result.InboxIncomplete)
				acked := 0
				for _, event := range result.Export.InboxJournal.Events {
					if event.Acked {
						acked++
					}
				}
				require.Equal(t, 300, acked)
				return
			}
			require.True(t, result.InboxIncomplete, "WAL replay loss must never claim complete salvage")
			notes := strings.Join(result.Notes, " ")
			require.Contains(t, notes, "WAL")
			require.Contains(t, notes, "committed records")
			require.Contains(t, notes, "tickets and mail")
			require.Contains(t, notes, "Even a stale tail makes recovery partial and may redeliver already-read mail")
			require.Contains(t, notes, "possibly missing")
			require.Contains(t, notes, "unknown")
			for _, event := range result.Export.InboxJournal.Events {
				require.False(t, event.Acked)
				require.Empty(t, event.AckedAt)
			}
			dest, _ := retentionStore(t)
			_, err := dest.Import(t.Context(), result.Export, sqlite.ImportModeMerge, false)
			require.NoError(t, err)
			mail, err := dest.InboxList(t.Context(), "recipient")
			require.NoError(t, err)
			require.Len(t, mail, expected, "every readable committed event is restored unread")
		})
	}
}

// damageCrashMailWAL selects independently observable WAL replay failures.
func damageCrashMailWAL(mode string, data []byte, page, second int) ([]byte, int) {
	expected := 303
	switch mode {
	case "first-checksum", "symlink-first-checksum":
		data[32+24+100] ^= 1
		expected = 300
	case "second-transaction-checksum":
		data[32+second*(24+page)+24+100] ^= 1
		expected = 301
	case "second-checksum-word":
		data[32+second*(24+page)+20] ^= 1
		expected = 301
	case "second-salt":
		data[32+second*(24+page)+12] ^= 1
		expected = 301
	case "salt":
		data[32+8] ^= 1
		expected = 300
	case "torn-tail":
		data = append(data, byte(1))
	case "header-checksum":
		data[24] ^= 1
		expected = 300
	}
	return data, expected
}
