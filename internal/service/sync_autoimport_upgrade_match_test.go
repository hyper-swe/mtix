// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests for MTIX-95.31.6 (FR-15.2i, FR-7.8): the automatic import's refusal
// lists a task the pulled board holds under another uid, the same task only
// because a uid was assigned when a clone upgraded from before uids were
// shared, whose two titles differ, as "treated as the same task (uid
// assigned at upgrade)", so the user can refuse the merge that would keep
// only the board's task. Written red-first against the MTIX-95.31.4 code,
// which imported such a pair without a word.
package service_test

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// upgradeMatchedLine is the refusal's line for PROJ-2 when the board holds
// it under another uid assigned at upgrade, with another title.
const upgradeMatchedLine = `  PROJ-2: treated as the same task (uid assigned at upgrade) ` +
	`(local "Task PROJ-2", file "Teammate's PROJ-2")`

// upgradeMatchedNote is the start of what the refusal's merge option says
// about such a task, PROJ-2.
const upgradeMatchedNote = "a task treated as the same task (uid assigned at upgrade), here PROJ-2, " +
	"keeps its id and takes the file's uid"

// upgradedAt returns when the clones in these tests upgraded: a month after
// the tasks were created, so every uid they assigned then counts as one
// assigned at upgrade.
func upgradedAt() time.Time {
	return time.Date(2026, 9, 24, 8, 0, 0, 0, time.UTC).Add(30 * 24 * time.Hour)
}

// setUpgradedUID gives node id of f's store a uid assigned at upgrade and
// re-exports, as the clone's last command did.
func setUpgradedUID(t *testing.T, f *guardFixture, id string) {
	t.Helper()
	ctx := context.Background()
	_, err := f.store.WriteDB().ExecContext(ctx, `UPDATE nodes SET uid = ? WHERE id = ?`, uidMintedAt(t, upgradedAt()), id)
	require.NoError(t, err)
	require.NoError(t, f.svc.AutoExport(ctx, f.mtixDir))
}

// TestAutoImport_SameTaskAtUpgradeWithOtherTitle_RefusalListsIt verifies
// the refusal lists PROJ-2, held under uids both assigned at upgrade, when
// the pulled board gives it another title (the two may be different tasks
// created in the same second before 0.4), on its own or beside other
// losses, and changes nothing; with one title the board imports and the
// file's uid is adopted.
func TestAutoImport_SameTaskAtUpgradeWithOtherTitle_RefusalListsIt(t *testing.T) {
	tests := []struct {
		name      string
		title     string
		dropNotes bool // the board also lacks PROJ-1's two annotations
		want      []string
	}{
		{"only the uid and the title differ", "Teammate's PROJ-2", false,
			[]string{upgradeMatchedLine + "\n", upgradeMatchedNote}},
		{"the board also lacks local annotations", "Teammate's PROJ-2", true,
			[]string{upgradeMatchedLine + "\n", "  PROJ-1: 2 annotations", upgradeMatchedNote}},
		{"one title", "Task PROJ-2", false, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			f := newGuardFixture(t)
			setUpgradedUID(t, f, "PROJ-2")
			fileUID := uidMintedAt(t, upgradedAt().Add(time.Hour))
			f.pull(t, f.teammateBoard(t, func(d *sqlite.ExportData) {
				n := &d.Nodes[nodeIndex(t, d, "PROJ-2")]
				n.UID, n.Title = fileUID, tt.title
				if tt.dropNotes {
					d.Nodes[nodeIndex(t, d, "PROJ-1")].Annotations = nil
				}
			}))
			before := f.storeSnapshot(t)

			err := f.svc.AutoImport(ctx, f.mtixDir)
			if tt.want == nil {
				require.NoError(t, err)
				assert.NotContains(t, f.notices.String(), refusalHeader)
				assert.Equal(t, fileUID, uidAt(t, f, "PROJ-2"), "the file's uid is adopted")
				return
			}
			require.ErrorIs(t, err, service.ErrAutoImportRefused)
			assert.Equal(t, before, f.storeSnapshot(t), "a refusal changes nothing")
			msg := f.notices.String()
			for _, w := range tt.want {
				assert.Contains(t, msg, w)
			}
		})
	}
}

// TestAutoImport_SameTaskAtUpgradeBeyondTheTenListed_NamedInMergeOption
// verifies the merge option names a task treated as the same task at
// upgrade with another title even when the loss list counts it without
// listing it (more than ten nodes lose data).
func TestAutoImport_SameTaskAtUpgradeBeyondTheTenListed_NamedInMergeOption(t *testing.T) {
	ctx := context.Background()
	f := newGuardFixture(t)
	created := time.Date(2026, 9, 24, 8, 0, 0, 0, time.UTC)
	for seq := 3; seq <= 13; seq++ {
		id := "PROJ-" + strconv.Itoa(seq)
		require.NoError(t, f.store.CreateNode(ctx, &model.Node{
			ID: id, Project: "PROJ", Depth: 0, Seq: seq, Title: "Task " + id,
			Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
			NodeType: model.NodeTypeEpic, ContentHash: "h-" + id, CreatedAt: created, UpdatedAt: created,
		}))
	}
	setUpgradedUID(t, f, "PROJ-9")
	f.pull(t, f.teammateBoard(t, func(d *sqlite.ExportData) {
		kept := d.Nodes[:0:0]
		for _, id := range []string{"PROJ-1", "PROJ-2", "PROJ-9"} {
			kept = append(kept, d.Nodes[nodeIndex(t, d, id)])
		}
		kept[2].UID, kept[2].Title = uidMintedAt(t, upgradedAt().Add(time.Hour)), "Teammate's PROJ-9"
		d.Nodes, d.NodeCount = kept, len(kept)
	}))

	require.ErrorIs(t, f.svc.AutoImport(ctx, f.mtixDir), service.ErrAutoImportRefused)
	msg := f.notices.String()
	assert.Contains(t, msg, "  and 1 more nodes\n", "PROJ-9 sorts after the ten listed")
	assert.NotContains(t, msg, "  PROJ-9: ")
	assert.Contains(t, msg, "a task treated as the same task (uid assigned at upgrade), here PROJ-9, keeps its id")
}
