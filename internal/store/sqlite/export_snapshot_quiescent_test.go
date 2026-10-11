// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func snapshotQuiescentData(t *testing.T, populated bool) *ExportData {
	t.Helper()
	store := snapshotProcessStore(t, t.TempDir())
	if populated {
		snapshotSeed(t, store)
		_, err := store.WriteDB().Exec(`UPDATE nodes SET uid='bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb' WHERE id='SNAP-1'`)
		require.NoError(t, err)
		require.NoError(t, snapshotWriteState(store))
	}
	data, err := store.Export(context.Background(), "SNAP", "reference")
	require.NoError(t, err)
	require.NoError(t, ValidateExport(data))
	return data
}
func TestExportSnapshot_QuiescentBytesMatchReference(t *testing.T) {
	for _, name := range []string{"empty", "populated"} {
		t.Run(name, func(t *testing.T) {
			data := snapshotQuiescentData(t, name == "populated")
			actual, err := json.MarshalIndent(data, "", "  ")
			require.NoError(t, err)
			reference, err := os.ReadFile(filepath.Join("testdata", "snapshot-reference-"+name+".json"))
			require.NoError(t, err)
			require.Equal(t, string(reference), string(actual), "quiescent native export bytes must match approved old reference")
			older, err := Schema100Form(data)
			require.NoError(t, err)
			actual, err = json.MarshalIndent(older, "", "  ")
			require.NoError(t, err)
			reference, err = os.ReadFile(filepath.Join("testdata", "snapshot-reference-"+name+"-legacy.json"))
			require.NoError(t, err)
			require.Equal(t, string(reference), string(actual), "legacy native snapshot bytes must stay unchanged")
		})
	}
}
