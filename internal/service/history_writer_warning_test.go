// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/sync"
)

// TestOlderWriterWarning_VersionTable pins when the history-incomplete
// warning fires: only while history.export is on, for a file written before
// the first release that keeps task history (service.FirstHistoryVersion) or
// recording no version, read by a release build. A patch gap above that
// version, a dev build and an off switch are silent (MTIX-95.31.19.9).
func TestOlderWriterWarning_VersionTable(t *testing.T) {
	first := service.FirstHistoryVersion
	v, err := sync.ParseVersion(first)
	require.NoError(t, err)
	require.Positive(t, v.Minor, "the table builds an earlier release by lowering the minor")
	earlier := fmt.Sprintf("v%d.%d.4-beta", v.Major, v.Minor-1)
	patchAbove := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch+1)
	newer := fmt.Sprintf("%d.%d.0", v.Major, v.Minor+1)
	binary := newer
	tests := []struct {
		name, file, binary string
		historyOn, want    bool
	}{
		{"older release", earlier, binary, true, true},
		{"no version recorded", "", binary, true, true},
		{"switch off", earlier, binary, false, false},
		{"switch off, no version", "", binary, false, false},
		{"first history version", first, binary, true, false},
		{"first history pre-release", first + "-beta", binary, true, false},
		{"patch gap above", patchAbove, binary, true, false},
		{"newer", newer, binary, true, false},
		{"file is a dev build", "dev", binary, true, false},
		{"binary is a dev build", earlier, "dev", true, false},
		{"binary version unset", earlier, "", true, false},
		{"no version recorded, dev binary", "", "dev", true, false},
		{"unparseable file version", "garbage", binary, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg, ok := service.OlderWriterWarning(tt.file, tt.binary, tt.historyOn)
			assert.Equal(t, tt.want, ok)
			if tt.want {
				assert.Contains(t, msg, "history may be incomplete: board last written by mtix")
				assert.Contains(t, msg, "upgrade mtix on that machine")
				if tt.file != "" {
					assert.Contains(t, msg, tt.file)
				}
			} else {
				assert.Empty(t, msg)
			}
		})
	}
}

func olderWriterBoard(t *testing.T, f *guardFixture, writer string) []byte {
	t.Helper()
	return f.teammateBoard(t, func(d *sqlite.ExportData) {
		addTeammateNode(t, d, "PROJ-2", "PROJ-3", 3)
		d.MtixVersion = writer
	})
}

func TestAutoImport_OlderWriter_WarnsOncePerImportedFile(t *testing.T) {
	ctx := context.Background()
	f := newGuardFixture(t)
	f.svc.SetBinaryVersion("0.6.0")
	f.svc.SetHistoryExportSource(func() bool { return true })
	f.pull(t, olderWriterBoard(t, f, "0.5.4"))
	require.NoError(t, f.svc.AutoImport(ctx, f.mtixDir))
	assert.Equal(t, 1, strings.Count(f.notices.String(), "history may be incomplete: board last written by mtix 0.5.4"), f.notices.String())

	f.notices.Reset() // the same file again: not imported, no warning
	require.NoError(t, f.svc.AutoImport(ctx, f.mtixDir))
	assert.NotContains(t, f.notices.String(), "history may be incomplete")
}

func TestAutoImport_SameOrNewerWriter_IsSilent(t *testing.T) {
	for _, writer := range []string{"0.6.0", "0.6.1", "0.7.0"} {
		t.Run(writer, func(t *testing.T) {
			f := newGuardFixture(t)
			f.svc.SetBinaryVersion("0.6.0")
			f.svc.SetHistoryExportSource(func() bool { return true })
			f.pull(t, olderWriterBoard(t, f, writer))
			require.NoError(t, f.svc.AutoImport(context.Background(), f.mtixDir))
			assert.NotContains(t, f.notices.String(), "history may be incomplete")
		})
	}
}

func TestAutoImport_OlderWriter_JSONModeWritesOneWarningLine(t *testing.T) {
	f := newGuardFixture(t)
	f.svc.SetBinaryVersion("0.6.0")
	f.svc.SetHistoryExportSource(func() bool { return true })
	f.svc.SetJSONNotices(true)
	f.pull(t, olderWriterBoard(t, f, "0.5.4"))
	require.NoError(t, f.svc.AutoImport(context.Background(), f.mtixDir))
	var found []map[string]string
	for _, line := range strings.Split(f.notices.String(), "\n") {
		if strings.HasPrefix(line, "{") {
			var m map[string]string
			require.NoError(t, json.Unmarshal([]byte(line), &m), line)
			found = append(found, m)
		}
	}
	require.Len(t, found, 1)
	assert.Equal(t, "history_incomplete", found[0]["warning"])
	assert.Equal(t, "0.5.4", found[0]["writer_version"])
	assert.Contains(t, found[0]["message"], "upgrade mtix on that machine")
	for _, line := range strings.Split(f.notices.String(), "\n") {
		if strings.Contains(line, "history may be incomplete") {
			assert.True(t, strings.HasPrefix(line, "{"), "the JSON line replaces the text line: %s", line)
		}
	}
}

func TestAutoImport_OlderWriter_SilentWhileHistoryExportIsOff(t *testing.T) {
	f := newGuardFixture(t)
	f.svc.SetBinaryVersion("0.6.0") // no history source: the default, off
	f.pull(t, olderWriterBoard(t, f, "0.5.4"))
	require.NoError(t, f.svc.AutoImport(context.Background(), f.mtixDir))
	assert.NotContains(t, f.notices.String(), "history may be incomplete")
}

// withTopLevelHistory returns board with the given top-level history entries
// and a recomputed checksum.
func withTopLevelHistory(t *testing.T, board []byte, entries ...map[string]string) []byte {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(board, &m))
	m["schema_version"] = "2.1.0"
	list := make([]any, 0, len(entries))
	for _, e := range entries {
		list = append(list, e)
	}
	m["history"] = list
	raw, err := json.Marshal(m)
	require.NoError(t, err)
	var data sqlite.ExportData
	require.NoError(t, json.Unmarshal(raw, &data))
	require.NoError(t, sqlite.RecomputeExportChecksum(&data))
	out, err := json.Marshal(&data)
	require.NoError(t, err)
	return out
}

// A pulled board whose history conflicts with a local entry, or is keyed by a
// legacy id, is reported once on load, in text and as one JSON line, with
// counts and entry ids and no content.
func TestAutoImport_HistoryConflictAndLegacyKey_AreReportedOnLoad(t *testing.T) {
	for _, jsonMode := range []bool{false, true} {
		f := newGuardFixture(t)
		f.svc.SetJSONNotices(jsonMode)
		_, err := f.store.WriteDB().Exec(`INSERT INTO task_history (entry_id, task_uid, ts, actor, origin, kind, field)
			VALUES ('01U0000000000000000000Q001', 'ghost-uid', '2026-03-14T12:00:00.000000000Z', 'tester', 'local', 'change', 'status')`)
		require.NoError(t, err)
		entry := func(id, key, actor string) map[string]string {
			return map[string]string{"id": id, "task_uid": key, "ts": "2026-03-14T12:00:00.000000000Z",
				"actor": actor, "origin": "local", "kind": "change", "field": "status"}
		}
		board := withTopLevelHistory(t, f.teammateBoard(t, func(d *sqlite.ExportData) { addTeammateNode(t, d, "PROJ-2", "PROJ-3", 3) }),
			entry("01U0000000000000000000Q001", "ghost-uid", "mallory"), entry("01U0000000000000000000Q002", "PROJ-1", "x"))
		f.pull(t, board)
		require.NoError(t, f.svc.AutoImport(context.Background(), f.mtixDir))
		out := f.notices.String()
		assert.Contains(t, out, "01U0000000000000000000Q001", out)
		assert.NotContains(t, out, "mallory")
		if !jsonMode {
			assert.Contains(t, out, "legacy task id")
			continue
		}
		var line map[string]any
		for _, l := range strings.Split(out, "\n") {
			if strings.HasPrefix(l, "{") && strings.Contains(l, "history_import") {
				require.NoError(t, json.Unmarshal([]byte(l), &line))
			}
		}
		require.NotNil(t, line, out)
		assert.EqualValues(t, 1, line["history_conflicts"])
		assert.EqualValues(t, 1, line["history_skipped_legacy_key"])
	}
}
