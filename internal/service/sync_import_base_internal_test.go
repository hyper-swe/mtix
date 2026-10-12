// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

// MTIX-95.31.15.1: the base made from the git merge-base board and the base
// made from the last-exchanged record must agree, task by task and field by
// field, or the same exchange would resolve differently by source.

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func TestImportBase_FieldKeys_MatchTheExchangeRecordFields(t *testing.T) {
	assert.Equal(t, exchangeFields(), sqlite.ExchangeFieldKeys())
}

func TestImportBase_RecordAndBoard_DigestTheSameFields(t *testing.T) {
	for _, schema := range []string{"2.0.0", "1.0.0"} {
		t.Run(schema, func(t *testing.T) {
			data, err := sqlite.DecodeExportData(bytes.NewReader(boardBytes(t, "ancestor board")))
			require.NoError(t, err)
			data.SchemaVersion = schema
			require.NoError(t, sqlite.RecomputeExportChecksum(data))
			rec, err := buildExchangeRecord(data, "git-import", receivedDigest([]byte("raw")), "2026-10-13T00:00:00Z")
			require.NoError(t, err)
			fromBoard, err := sqlite.ImportBaseFromBoard(data, "board")
			require.NoError(t, err)
			tasks := make([]sqlite.BaseTask, 0, len(rec.Tasks))
			for _, task := range rec.Tasks {
				tasks = append(tasks, sqlite.BaseTask{Kind: task.Kind, Identity: task.Identity, ID: task.ID, Fields: task.Fields})
			}
			fromRecord := sqlite.NewImportBase("record", schema == "1.0.0", tasks)
			assert.Equal(t, fromBoard.Older, fromRecord.Older)
			for _, task := range rec.Tasks {
				assert.Equal(t, fromBoard.Fields(task.Kind, task.Identity), fromRecord.Fields(task.Kind, task.Identity),
					"%s digests by source", task.ID)
				assert.NotEmpty(t, fromBoard.Fields(task.Kind, task.Identity))
			}
			// A local copy of the same task resolves to no change against either base.
			res1, err1 := sqlite.ResolveWithBase(data, data, fromBoard)
			res2, err2 := sqlite.ResolveWithBase(data, data, fromRecord)
			if schema == "1.0.0" {
				require.NoError(t, err1)
				require.NoError(t, err2)
				assert.False(t, res1.Diff.Lossy())
				assert.False(t, res2.Diff.Lossy())
				assert.Equal(t, res1.Kept, res2.Kept)
			} else {
				require.Error(t, err1)
				require.Error(t, err2)
			}
		})
	}
}

func TestRecordBase_NoRecord_ReportsWhy(t *testing.T) {
	svc := NewSyncService(nil, nil, nil)
	base, found, why := svc.recordBase(context.Background(), t.TempDir())
	assert.Nil(t, base)
	assert.False(t, found)
	assert.Contains(t, why, "missing")
}
