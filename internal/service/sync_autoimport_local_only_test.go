// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// MTIX-112.17.16.16: an automatic import journals nothing, so the tasks it
// changes that the hub already knows are named as local only, on the notice
// writer and in the store's record that mtix doctor reads.

// TestAutoImport_SyncedStoreHubKnownTaskChanged_NoticeNamesItLocalOnly: the
// teammate's retitle of a task the hub knows imports with the usual line,
// plus a line naming the task as local only and saying a pull may replace it.
func TestAutoImport_SyncedStoreHubKnownTaskChanged_NoticeNamesItLocalOnly(t *testing.T) {
	ctx := context.Background()
	f := newGuardFixture(t)
	_, err := f.store.WriteDB().Exec(`UPDATE sync_events SET sync_status = 'pushed'`)
	require.NoError(t, err)
	f.pull(t, f.teammateBoard(t, func(d *sqlite.ExportData) {
		i := nodeIndex(t, d, "PROJ-2")
		d.Nodes[i].Title, d.Nodes[i].ContentHash = "Retitled upstream", "h-upstream"
	}))

	require.NoError(t, f.svc.AutoImport(ctx, f.mtixDir))

	assert.Contains(t, f.notices.String(), "1 task the hub already knows changed only locally (PROJ-2)")
	assert.Contains(t, f.notices.String(), "may be replaced on the next pull")
	live, overflow, err := f.store.LocalOnlyImports(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"PROJ-2"}, localOnlyIDs(live))
	assert.Zero(t, overflow)
}

// TestAutoImport_UnsyncedStore_NoLocalOnlyLine: a store whose tasks the hub
// does not know gets only the usual line.
func TestAutoImport_UnsyncedStore_NoLocalOnlyLine(t *testing.T) {
	ctx := context.Background()
	f := newGuardFixture(t)
	f.pull(t, f.teammateBoard(t, func(d *sqlite.ExportData) {
		i := nodeIndex(t, d, "PROJ-2")
		d.Nodes[i].Title, d.Nodes[i].ContentHash = "Retitled upstream", "h-upstream"
	}))

	require.NoError(t, f.svc.AutoImport(ctx, f.mtixDir))

	assert.NotContains(t, f.notices.String(), "changed only locally")
	live, _, err := f.store.LocalOnlyImports(ctx)
	require.NoError(t, err)
	assert.Empty(t, live)
}

// TestAutoImport_ManyHubKnownTasks_NoticeNamesTenAndCountsTheRest: the line
// names at most ten ids and counts the rest.
func TestAutoImport_ManyHubKnownTasks_NoticeNamesTenAndCountsTheRest(t *testing.T) {
	ctx := context.Background()
	f := newGuardFixture(t)
	created := time.Date(2026, 9, 24, 8, 0, 0, 0, time.UTC)
	for i := 3; i <= 14; i++ {
		id := fmt.Sprintf("PROJ-%d", i)
		require.NoError(t, f.store.CreateNode(ctx, &model.Node{
			ID: id, Project: "PROJ", Depth: 0, Seq: i, Title: "Task " + id,
			Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
			NodeType: model.NodeTypeEpic, ContentHash: "h-" + id, CreatedAt: created, UpdatedAt: created,
		}))
	}
	require.NoError(t, f.svc.AutoExport(ctx, f.mtixDir))
	_, err := f.store.WriteDB().Exec(`UPDATE sync_events SET sync_status = 'pushed'`)
	require.NoError(t, err)
	f.pull(t, f.teammateBoard(t, func(d *sqlite.ExportData) {
		for i := range d.Nodes {
			d.Nodes[i].Title, d.Nodes[i].ContentHash = "Retitled "+d.Nodes[i].ID, "h2-"+d.Nodes[i].ID
		}
	}))

	require.NoError(t, f.svc.AutoImport(ctx, f.mtixDir))

	assert.Contains(t, f.notices.String(), "14 tasks the hub already knows changed only locally")
	assert.Contains(t, f.notices.String(), "and 4 more)")
	assert.NotContains(t, f.notices.String(), "PROJ-9", "only the first ten ids are named")
}

func localOnlyIDs(in []sqlite.LocalOnlyImport) []string {
	out := make([]string, 0, len(in))
	for _, x := range in {
		out = append(out, x.ID)
	}
	return out
}
