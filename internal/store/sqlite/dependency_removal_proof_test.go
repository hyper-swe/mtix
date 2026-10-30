// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

const (
	removalCreated = `{"id":"act-1","type":"note","author":"local","text":"created","created_at":"2026-09-01T00:00:00Z"}`
	removalEntry   = `{"id":"act-rm","type":"system","author":"system","text":"Removed dependency: blocks P-2","created_at":"2026-09-03T00:00:00Z"}`
)

// removalBoard is a local board where P-1 blocks P-2. activity is the JSON of
// P-1's local activity entries ("" for none).
func removalBoard(t *testing.T, activity string) *sqlite.ExportData {
	t.Helper()
	act := ""
	if activity != "" {
		act = `,"activity":[` + activity + `]`
	}
	board := &sqlite.ExportData{}
	require.NoError(t, json.Unmarshal([]byte(`{"schema_version":"2.0.0","nodes":[`+
		`{"id":"P-1","title":"Source","created_at":"2026-09-01T00:00:00Z","updated_at":"2026-09-02T00:00:00Z"`+act+`},`+
		`{"id":"P-2","title":"Target","created_at":"2026-09-01T00:00:00Z","updated_at":"2026-09-02T00:00:00Z"}],`+
		`"dependencies":[{"from_id":"P-1","to_id":"P-2","dep_type":"blocks","created_at":"2026-09-01T00:00:00Z"}]}`), board))
	return board
}

// removalFile is the teammate's copy: the dependency is gone, P-1 has the given
// updated_at and activity entries.
func removalFile(t *testing.T, local *sqlite.ExportData, updatedAt, activity string) *sqlite.ExportData {
	t.Helper()
	return editExport(t, local, func(doc map[string]any) {
		doc["dependencies"] = []any{}
		n := docNode(t, doc, "P-1")
		n["updated_at"] = updatedAt
		var entries []any
		require.NoError(t, json.Unmarshal([]byte("["+activity+"]"), &entries))
		n["activity"] = entries
	})
}

func hasBlocksLoss(loss *sqlite.NodeLoss) bool {
	return loss != nil && len(loss.Dependencies) == 1 && loss.Dependencies[0] == "blocks P-2"
}

// TestResolveWithBase_RemovalEntryWithoutDescent_StillListsDependency: the base
// resolver settles fields against the base, but a base holds no dependencies,
// so a removal must still pass the real descent check (MTIX-107.44).
func TestResolveWithBase_RemovalEntryWithoutDescent_StillListsDependency(t *testing.T) {
	tests := []struct {
		name          string
		localActivity string
		fileUpdatedAt string
		fileActivity  string
		wantLoss      bool
	}{
		{"older copy of a task with a created entry", removalCreated, "2026-08-01T00:00:00Z", removalCreated + "," + removalEntry, true},
		{"task with no local activity, newer copy", "", "2026-09-05T00:00:00Z", removalEntry, true},
		{"copy lacks a local entry", removalCreated, "2026-09-05T00:00:00Z", removalEntry, true},
		{"copy descends and carries the removal entry", removalCreated, "2026-09-05T00:00:00Z", removalCreated + "," + removalEntry, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			local := removalBoard(t, tt.localActivity)
			file := removalFile(t, local, tt.fileUpdatedAt, tt.fileActivity)
			older := asSchemaV1(t, editExport(t, local, func(map[string]any) {}))
			base, err := sqlite.ImportBaseFromBoard(older, "test exchanged board")
			require.NoError(t, err)
			resolved, err := sqlite.ResolveWithBase(local, file, base)
			require.NoError(t, err)
			assert.Equal(t, tt.wantLoss, hasBlocksLoss(lossOf(resolved.Diff, "P-1")),
				"losses: %+v", resolved.Diff.Losses)
		})
	}
}

// TestDiffReplace_RemovalEntryOfTheWrongKind_StillALoss pins what makes an
// activity entry a removal proof: the system type, the system author and the
// exact text. A comment authored as system with the same text proves nothing.
func TestDiffReplace_RemovalEntryOfTheWrongKind_StillALoss(t *testing.T) {
	const text = `"text":"Removed dependency: blocks P-2","created_at":"2026-09-03T00:00:00Z"}`
	tests := []struct {
		name     string
		entry    string
		wantLoss bool
	}{
		{"system entry by system", `{"id":"act-rm","type":"system","author":"system",` + text, false},
		{"comment authored as system", `{"id":"act-rm","type":"comment","author":"system",` + text, true},
		{"system entry by another author", `{"id":"act-rm","type":"system","author":"someone",` + text, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			local := removalBoard(t, removalCreated)
			file := removalFile(t, local, "2026-09-05T00:00:00Z", removalCreated+","+tt.entry)
			diff, err := sqlite.DiffReplace(local, file)
			require.NoError(t, err)
			assert.Equal(t, tt.wantLoss, hasBlocksLoss(lossOf(diff, "P-1")), "losses: %+v", diff.Losses)
		})
	}
}
