// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// FR-19.1: the real command must not print Complete after SQLite drops a WAL transaction.
func TestRecoverCommand_WALReplayLoss_PrintsPartial(t *testing.T) {
	for _, damaged := range []bool{false, true} {
		t.Run(map[bool]string{false: "intact", true: "bad-frame"}[damaged], func(t *testing.T) {
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
				if damaged && name == "data/mtix.db-wal" {
					require.Greater(t, len(data), 32+24+100)
					data[32+24+100] ^= 1
				}
				require.NoError(t, os.WriteFile(filepath.Join(target, name), data, 0600))
			}
			t.Chdir(root)
			var recovered string
			output, err := cliBehaviorOutput(t, func() error { var e error; recovered, e = runRecover(); return e })
			require.NoError(t, err)
			data, err := os.ReadFile(recovered)
			require.NoError(t, err)
			var file sqlite.ExportData
			require.NoError(t, json.Unmarshal(data, &file))
			require.NoError(t, sqlite.ValidateExport(&file))
			if !damaged {
				require.Contains(t, output, "Salvage complete")
				require.Len(t, file.InboxJournal.Events, 4)
				return
			}
			require.Len(t, file.InboxJournal.Events, 3)
			require.Contains(t, output, "Partial salvage")
			require.NotContains(t, output, "Salvage complete")
			require.Contains(t, output, "WAL")
			require.Contains(t, output, "possibly missing")
			for _, event := range file.InboxJournal.Events {
				require.False(t, event.Acked)
				require.Empty(t, event.AckedAt)
			}
		})
	}
}
