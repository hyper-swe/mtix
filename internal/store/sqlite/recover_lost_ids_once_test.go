package sqlite

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRecover_TwoMisreadEntriesForOneLostID_ListsItOnceAndSalvageFileImports
// covers MTIX-107.80: two index entries for RC-2 that point at other rows,
// with RC-2 absent from the mirror, list RC-2 once in LostIDs, and the
// salvage file, written to disk and read back as the CLI does, still
// imports with a replace import.
func TestRecover_TwoMisreadEntriesForOneLostID_ListsItOnceAndSalvageFileImports(t *testing.T) {
	tc := &damagedIndexCase{
		rc4: true,
		editMirror: func(t *testing.T, m *ExportData) {
			kept := m.Nodes[:0]
			for _, n := range m.Nodes {
				if n.ID != "RC-2" {
					kept = append(kept, n)
				}
			}
			m.Nodes = kept
		},
		patches: map[string]indexPatch{
			"RC-2": {pointAt: "RC-1"}, "RC-4": {renameTo: "RC-2", pointAt: "RC-3"},
		},
	}
	dbPath, mirrorPath := damagedIndexFixture(t, tc)

	res, err := Recover(context.Background(), dbPath, mirrorPath, "test-version", slog.Default())
	require.NoError(t, err)
	assert.Equal(t, []string{"RC-2"}, res.LostIDs, "RC-2 is listed as lost once")

	raw, err := json.Marshal(res.Export)
	require.NoError(t, err)
	var back ExportData
	require.NoError(t, json.Unmarshal(raw, &back))

	fresh, err := New(t.TempDir()+"/fresh.db", slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = fresh.Close() })
	_, err = fresh.Import(context.Background(), &back, ImportModeReplace, false)
	require.NoError(t, err, "the salvage file imports with a replace import")
	for _, id := range []string{"RC-1", "RC-3", "RC-4"} {
		_, err := fresh.GetNode(context.Background(), id)
		assert.NoError(t, err, "%s is imported", id)
	}
}

// TestFinishRecover_LostIDRepeatedNotAdjacent_ListedOnceAfterSort covers
// MTIX-107.80: a lost id read twice with another lost id between the two
// reads is only adjacent after the sort, so compacting before sorting keeps
// it twice.
func TestFinishRecover_LostIDRepeatedNotAdjacent_ListedOnceAfterSort(t *testing.T) {
	res := &RecoverResult{LostIDs: []string{"RC-9", "RC-2", "RC-9", "RC-2", "RC-7"}}
	got, err := finishRecover(res, map[string]exportNode{}, &dbSalvage{}, "test-version")
	require.NoError(t, err)
	assert.Equal(t, []string{"RC-2", "RC-7", "RC-9"}, got.LostIDs)
}
