// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package sqlite_test

import (
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func damageMailFixture(t *testing.T, coverage string) (string, int, int) {
	t.Helper()
	source, raw := retentionStore(t)
	addressTo(t, source, "recipient", "PROJ-1")
	_, err := raw.Exec(`DELETE FROM sync_events`)
	require.NoError(t, err)
	for seq := 1; seq <= 300; seq++ {
		payload := fmt.Sprintf(`{"to":"recipient","body":%q}`, strings.Repeat("m", 100))
		_, err = raw.Exec(`INSERT INTO sync_events(rowid,event_id,project_prefix,node_id,op_type,payload,wall_clock_ts,lamport_clock,vector_clock,author_id,author_machine_hash,created_at) VALUES(?,?,'PROJ','PROJ-1','comment',?,0,1,'{}','sender','','2026-11-05T00:00:00Z')`, seq, fmt.Sprintf("damage-%03d", seq), payload)
		require.NoError(t, err)
	}
	switch coverage {
	case "cursor":
		_, err = raw.Exec(`INSERT INTO agent_inbox_cursor(agent_id,cursor) VALUES('recipient',100)`)
	case "selective":
		_, err = raw.Exec(`INSERT INTO agent_inbox_ack(agent_id,event_seq,acked_at) VALUES('recipient',44,'2026-11-05T00:00:00Z')`)
	}
	require.NoError(t, err)
	return transportBackupPage(t, source, "sync_events")
}

func recoverDamageMail(t *testing.T, path string) *sqlite.RecoverResult {
	t.Helper()
	result, err := sqlite.Recover(t.Context(), path, filepath.Join(t.TempDir(), "absent.json"), "test", sqlite.RecoverOptions{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	require.NoError(t, err)
	require.NotNil(t, result.Export.InboxJournal)
	require.NoError(t, sqlite.ValidateExport(result.Export))
	return result
}

func TestRecover_FlippedChildPointer_ReportsIntegrityDamage(t *testing.T) {
	path, root, size := damageMailFixture(t, "")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	h := (root - 1) * size
	require.Equal(t, byte(5), data[h])
	count := int(binary.BigEndian.Uint16(data[h+3:]))
	patched := false
	for i := 1; i < count; i++ {
		prev := h + int(binary.BigEndian.Uint16(data[h+12+2*(i-1):]))
		at := h + int(binary.BigEndian.Uint16(data[h+12+2*i:]))
		before, after := binary.BigEndian.Uint32(data[at:]), binary.BigEndian.Uint32(data[prev:])
		bits := before ^ after
		if bits == 0 || bits&(bits-1) != 0 {
			continue
		}
		binary.BigEndian.PutUint32(data[at:], after)
		patched = true
		break
	}
	require.True(t, patched, "fixture needs adjacent children differing by one bit")
	require.NoError(t, os.WriteFile(path, data, 0600))
	result := recoverDamageMail(t, path)
	require.Less(t, len(result.Export.InboxJournal.Events), 300, "fixture must hide a leaf")
	require.True(t, result.InboxIncomplete, "agreeing scan and walk cannot prove complete salvage")
	require.Contains(t, strings.Join(result.Notes, " "), "integrity_check")
}

func TestRecover_FlippedRowid_CannotBorrowAcknowledgment(t *testing.T) {
	for _, coverage := range []string{"cursor", "selective"} {
		t.Run(coverage, func(t *testing.T) {
			path, root, size := damageMailFixture(t, coverage)
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			patched := false
			for _, cell := range rowCells(t, data, root, size) {
				if cell.seq != 172 {
					continue
				}
				require.Equal(t, 2, cell.length)
				data[cell.at] ^= 1
				seq, _ := rowVarint(data[cell.at:])
				require.Equal(t, int64(44), seq)
				patched = true
			}
			require.True(t, patched)
			require.NoError(t, os.WriteFile(path, data, 0600))
			result := recoverDamageMail(t, path)
			dst, _ := retentionStore(t)
			_, err = dst.Import(t.Context(), result.Export, sqlite.ImportModeMerge, false)
			require.NoError(t, err)
			mail, err := dst.InboxList(t.Context(), "recipient")
			require.NoError(t, err)
			var ids []string
			for _, event := range mail {
				ids = append(ids, event.EventID)
			}
			require.Contains(t, ids, "damage-172", "unverified coverage must fail toward unread mail")
			require.True(t, result.InboxIncomplete)
			require.Contains(t, strings.Join(result.Notes, " "), "acknowledgment coverage discarded")
		})
	}
}
