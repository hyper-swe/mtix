// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// MTIX-112.2 round 2: a replace that drops the task holding the newest events
// frees their seqs, and the next events take them again (sync_events has no
// AUTOINCREMENT). Acks, deliveries and cursors keyed to the freed seqs must go
// with the events, or new mail arrives already acknowledged.

func TestInboxRetention_ReplaceDroppingNewestEvents_NewMailIsUnread(t *testing.T) {
	for _, variant := range []string{"selective ack", "ack through", "hook delivery"} {
		t.Run(variant, func(t *testing.T) {
			ctx := context.Background()
			s, raw := retentionStore(t)
			addressTo(t, s, "opus", "PROJ-1")
			addressTo(t, s, "opus", "PROJ-2")
			listed, err := s.InboxList(ctx, "opus")
			require.NoError(t, err)
			require.Len(t, listed, 2)
			newest := listed[1].Seq
			switch variant {
			case "selective ack":
				require.NoError(t, s.InboxAck(ctx, "opus", newest))
			case "ack through":
				require.NoError(t, s.InboxAckThrough(ctx, "opus", newest))
			default:
				_, err = raw.Exec(`INSERT INTO inbox_deliveries (agent_id, event_seq, hook_name, delivered_at) VALUES ('hooky', ?, 'h', '2030-01-01T00:00:00Z')`, newest)
				require.NoError(t, err)
			}

			data, err := s.Export(ctx, "", "")
			require.NoError(t, err)
			data = withoutExportNode(data, "PROJ-2")
			require.NoError(t, sqlite.RecomputeExportChecksum(data))
			_, err = s.Import(ctx, data, sqlite.ImportModeReplace, false)
			require.NoError(t, err)

			addressTo(t, s, "opus", "PROJ-3")
			got, err := s.InboxList(ctx, "opus")
			require.NoError(t, err)
			var fresh []sqlite.InboxEvent
			for _, e := range got {
				if e.NodeID == "PROJ-3" {
					fresh = append(fresh, e)
				}
			}
			require.Len(t, fresh, 1, "mail addressed after the replace is unread, not pre-acknowledged")
			assert.LessOrEqual(t, fresh[0].Seq, newest, "guard the premise: the new mail reuses a freed seq")

			hist, err := s.InboxHistory(ctx, "opus")
			require.NoError(t, err)
			last := hist[len(hist)-1]
			assert.Equal(t, "PROJ-3", last.NodeID)
			assert.Equal(t, sqlite.InboxUnacked, last.State)
			assert.Nil(t, last.AckedAt)

			hooky, err := s.InboxList(ctx, "hooky")
			require.NoError(t, err)
			assert.Empty(t, hooky, "a delivery row of a removed event must not deliver the unrelated event that reuses its seq")
		})
	}
}

func TestInboxRetention_ReplaceDroppingNewestEvents_EarlierAcksSurvive(t *testing.T) {
	ctx := context.Background()
	s, _ := retentionStore(t)
	addressTo(t, s, "opus", "PROJ-1")
	addressTo(t, s, "opus", "PROJ-2")
	listed, err := s.InboxList(ctx, "opus")
	require.NoError(t, err)
	require.NoError(t, s.InboxAck(ctx, "opus", listed[0].Seq))

	data, err := s.Export(ctx, "", "")
	require.NoError(t, err)
	data = withoutExportNode(data, "PROJ-2")
	require.NoError(t, sqlite.RecomputeExportChecksum(data))
	_, err = s.Import(ctx, data, sqlite.ImportModeReplace, false)
	require.NoError(t, err)

	got, err := s.InboxList(ctx, "opus")
	require.NoError(t, err)
	assert.Empty(t, got, "the ack of a surviving event still holds")
}

func TestInboxRetention_HookDeliveries_CountedAndListed(t *testing.T) {
	ctx := context.Background()
	s, raw := retentionStore(t)
	mustCreateNode(t, s, "PROJ-1", "")
	addressTo(t, s, "opus", "PROJ-2")
	var createSeq, commentSeq int64
	require.NoError(t, raw.QueryRow(`SELECT rowid FROM sync_events WHERE op_type = 'create_node' AND node_id = 'PROJ-1'`).Scan(&createSeq))
	require.NoError(t, raw.QueryRow(`SELECT rowid FROM sync_events WHERE op_type = 'comment'`).Scan(&commentSeq))
	// One hook-only delivery, and one delivery of an event that is also addressed.
	for _, seq := range []int64{createSeq, commentSeq} {
		_, err := raw.Exec(`INSERT INTO inbox_deliveries (agent_id, event_seq, hook_name, delivered_at) VALUES ('opus', ?, 'h', '2030-01-01T00:00:00Z')`, seq)
		require.NoError(t, err)
	}
	assert.Equal(t, int64(2), unackedTotal(t, s), "the delivery-only event and the addressed one count once each")

	hist, err := s.InboxHistory(ctx, "opus")
	require.NoError(t, err)
	require.Len(t, hist, 2)
	assert.Equal(t, createSeq, hist[0].Seq)
	assert.Equal(t, commentSeq, hist[1].Seq)

	require.NoError(t, s.InboxAck(ctx, "opus", createSeq))
	assert.Equal(t, int64(1), unackedTotal(t, s))
	hist, err = s.InboxHistory(ctx, "opus")
	require.NoError(t, err)
	assert.Equal(t, sqlite.InboxAcked, hist[0].State)
}

func TestCountUnackedInboxFile_MissingFile_ErrorsAndCreatesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.db")
	_, err := sqlite.CountUnackedInboxFile(context.Background(), path)
	require.Error(t, err)
	_, statErr := os.Stat(path)
	assert.True(t, os.IsNotExist(statErr), "the count must never create the database it was asked to read")
	empty := filepath.Join(t.TempDir(), "empty.db")
	require.NoError(t, os.WriteFile(empty, nil, 0o600))
	_, err = sqlite.CountUnackedInboxFile(context.Background(), empty)
	require.Error(t, err)
}

func TestCountUnackedInboxFile_ReadOnlyFileInReadOnlyDir_CountsWithoutWriting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "damaged.db")
	s, err := sqlite.New(path, nil)
	require.NoError(t, err)
	addressTo(t, s, "opus", "PROJ-1")
	require.NoError(t, s.Close())
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	// Neither the file nor its directory is writable: only a real read-only
	// open can read it.
	require.NoError(t, os.Chmod(path, 0o444))
	require.NoError(t, os.Chmod(dir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	n, err := sqlite.CountUnackedInboxFile(context.Background(), path)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, sha256.Sum256(before), sha256.Sum256(after), "the database bytes are unchanged")
}

// A crashed database leaves a -wal beside it. A read-write open would merge
// it into the main file and delete it on close; the count must leave both
// files exactly as they were.
func TestCountUnackedInboxFile_LeftoverWAL_FilesUntouched(t *testing.T) {
	src := t.TempDir()
	srcPath := filepath.Join(src, "mtix.db")
	s, err := sqlite.New(srcPath, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	addressTo(t, s, "opus", "PROJ-1")
	walBytes, err := os.ReadFile(srcPath + "-wal")
	require.NoError(t, err, "premise: the open store holds unmerged frames in its -wal")
	require.NotEmpty(t, walBytes)

	dir := t.TempDir()
	path := filepath.Join(dir, "damaged.db")
	mainBytes, err := os.ReadFile(srcPath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, mainBytes, 0o600))
	require.NoError(t, os.WriteFile(path+"-wal", walBytes, 0o600))

	_, _ = sqlite.CountUnackedInboxFile(context.Background(), path)

	afterMain, err := os.ReadFile(path)
	require.NoError(t, err)
	afterWAL, err := os.ReadFile(path + "-wal")
	require.NoError(t, err, "the count must not merge away the -wal")
	assert.Equal(t, sha256.Sum256(mainBytes), sha256.Sum256(afterMain), "the main file is unchanged")
	assert.Equal(t, sha256.Sum256(walBytes), sha256.Sum256(afterWAL), "the -wal is unchanged")
}
