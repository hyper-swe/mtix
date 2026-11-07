// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// NFR-2.8: command reporting must also preserve the original empty database and WAL.
func TestRecoverCommand_EmptyDatabase_PreservesSource(t *testing.T) {
	for _, mode := range []string{"no-mirror", "mirror", "private-export"} {
		t.Run(mode, func(t *testing.T) {
			initTestApp(t)
			events := envelopeCLIEvents(t, "base one", "base two", "base three")
			require.NoError(t, app.store.InboxAck(t.Context(), "worker", events[0].Seq))
			require.NoError(t, app.syncSvc.AutoExport(t.Context(), app.mtixDir))
			for _, q := range []string{`PRAGMA wal_autocheckpoint=0`, `PRAGMA wal_checkpoint(TRUNCATE)`} {
				_, err := app.store.WriteDB().Exec(q)
				require.NoError(t, err)
			}
			_, err := app.store.WriteDB().Exec(`INSERT INTO sync_events(event_id,project_prefix,node_id,op_type,payload,wall_clock_ts,lamport_clock,vector_clock,author_id,author_machine_hash,created_at)
    VALUES('wal-cli-only','TEST','TEST-1','comment','{"to":"worker","body":"WAL only mail"}',0,1,'{}','sender','','2026-11-05T00:00:00Z')`)
			require.NoError(t, err)
			source := app.mtixDir
			root := t.TempDir()
			target := filepath.Join(root, ".mtix")
			require.NoError(t, os.MkdirAll(filepath.Join(target, "data"), 0700))
			for _, name := range []string{"config.yaml", "tasks.json", "data/mtix.db", "data/mtix.db-wal", "data/mtix.db-shm"} {
				data, e := os.ReadFile(filepath.Join(source, name))
				require.NoError(t, e)
				if name == "data/mtix.db" {
					data = nil
				}
				if name == "tasks.json" && mode == "no-mirror" {
					continue
				}
				require.NoError(t, os.WriteFile(filepath.Join(target, name), data, 0600))
			}
			path := filepath.Join(target, "data/mtix.db")
			wal, err := os.ReadFile(path + "-wal")
			require.NoError(t, err)
			require.NotEmpty(t, wal)
			fromExport := ""
			if mode == "private-export" {
				file, err := app.store.ExportWithInbox(t.Context(), "", "")
				require.NoError(t, err)
				data, err := json.Marshal(file)
				require.NoError(t, err)
				fromExport = filepath.Join(root, "private.json")
				require.NoError(t, os.WriteFile(fromExport, data, 0600))
			}
			t.Chdir(root)
			var recovered string
			output, recoverErr := cliBehaviorOutput(t, func() error { var e error; recovered, e = runRecoverFrom(fromExport); return e })
			for suffix, before := range map[string][]byte{"": {}, "-wal": wal} {
				after, err := os.ReadFile(path + suffix)
				require.NoError(t, err, "recover command removed source %s", suffix)
				require.Equal(t, sha256.Sum256(before), sha256.Sum256(after))
			}
			if mode == "no-mirror" {
				require.ErrorContains(t, recoverErr, "zero-byte")
				require.Contains(t, recoverErr.Error(), "skipped")
				return
			}
			require.NoError(t, recoverErr)
			data, err := os.ReadFile(recovered)
			require.NoError(t, err)
			var file sqlite.ExportData
			require.NoError(t, json.Unmarshal(data, &file))
			require.NoError(t, sqlite.ValidateExport(&file))
			require.Contains(t, output, "Partial salvage")
			require.NotContains(t, output, "Salvage complete")
			require.Contains(t, output, "zero-byte")
			require.Contains(t, output, "skipped")
			require.NotEmpty(t, file.Nodes)
			if mode == "private-export" {
				require.Len(t, file.InboxJournal.Events, 4)
				for _, event := range file.InboxJournal.Events {
					require.False(t, event.Acked)
					require.Empty(t, event.AckedAt)
				}
			} else {
				require.Nil(t, file.InboxJournal)
			}
		})
	}
}
