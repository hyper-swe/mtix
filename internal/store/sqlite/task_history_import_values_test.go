package sqlite_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// an import that changes code_refs, weight and metadata.
func TestTaskHistory_Import_FreeTextRefsAndMetadata_StoreNoText(t *testing.T) {
	ctx := context.Background()
	src := newHistoryFixture(t)
	seedCascadeNodes(t, src.s, ccNode(ccRoot, model.StatusOpen))
	_, err := src.db.Exec(`UPDATE nodes SET code_refs = '[{"file":"a.go","line":3,"snippet":"API_KEY=sekrit-123"}]', weight = 2.5, estimate_min = 30, metadata = '{"k":"private"}' WHERE id = ?`, ccRoot)
	require.NoError(t, err)
	data, err := src.s.Export(ctx, "PROJ", "0.6.0")
	require.NoError(t, err)
	dst := newHistoryFixture(t)
	seedCascadeNodes(t, dst.s, ccNode(ccRoot, model.StatusOpen))
	var uid string
	require.NoError(t, src.db.QueryRow(`SELECT uid FROM nodes WHERE id = ?`, ccRoot).Scan(&uid))
	_, err = dst.db.Exec(`UPDATE nodes SET uid = ? WHERE id = ?`, uid, ccRoot)
	require.NoError(t, err)
	dst.clearHistory(t)
	_, err = dst.s.Import(ctx, data, sqlite.ImportModeReplace, false)
	require.NoError(t, err)
	var dump string
	require.NoError(t, dst.db.QueryRow(`SELECT COALESCE(group_concat(COALESCE(old_value,'')||COALESCE(new_value,''), '|'),'') FROM task_history`).Scan(&dump))
	assert.NotContains(t, dump, "sekrit", "a code snippet is free text")
	assert.NotContains(t, dump, "private", "metadata is value-free")
	fields := dst.fields(t, ccRoot)
	assert.Contains(t, fields, "code_refs", "the change is still noticed")
	assert.Contains(t, fields, "weight")
	assert.Contains(t, fields, "estimate_min")
	for _, r := range dst.rows(t, ccRoot) {
		if r.field == "code_refs" || r.field == "metadata" {
			assert.False(t, r.old.Valid || r.new.Valid, "%s carries no value", r.field)
		}
	}
}
