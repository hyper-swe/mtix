package sqlite_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// cascade cancel where one descendant blocks another inside the subtree.
func TestTaskHistory_CascadeCancel_DependentInsideTheSubtree_RecordsItsCancel(t *testing.T) {
	f := newHistoryFixture(t)
	f.seed(t) // PROJ-1, PROJ-1.1, PROJ-1.2
	ctx := context.Background()
	require.NoError(t, f.s.AddDependency(ctx, &model.Dependency{FromID: ccChild, ToID: ccChild2, DepType: model.DepTypeBlocks, CreatedBy: "alice"}))
	n, err := f.s.GetNode(ctx, ccChild2)
	require.NoError(t, err)
	require.Equal(t, model.StatusBlocked, n.Status)
	f.clearHistory(t)
	require.NoError(t, f.s.CancelNode(ctx, ccRoot, "gone", "bob", true))
	n, err = f.s.GetNode(ctx, ccChild2)
	require.NoError(t, err)
	require.Equal(t, model.StatusCancelled, n.Status)
	row := f.onlyRow(t, ccChild2, "status")
	assert.Equal(t, `"blocked"`, row.old.String)
	assert.Equal(t, `"cancelled"`, row.new.String)
	assert.Equal(t, "local", row.origin)
}
