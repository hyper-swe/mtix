// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// MTIX-112.17.16.16: an import writes no sync event, so tasks an auto-import
// only adds stay local on a store that syncs. The notice says so.

const localOnlyNotice = "wrote no sync event and stay local only unless their author pushed them"

// TestAutoImport_AddedOnlyOnSyncedStore_NoticeSaysLocalOnly: a teammate's new
// task imported on a synced store prints the local-only notice.
func TestAutoImport_AddedOnlyOnSyncedStore_NoticeSaysLocalOnly(t *testing.T) {
	f := newGuardFixture(t)
	markFixtureSynced(t, f)
	f.pull(t, f.teammateBoard(t, func(d *sqlite.ExportData) { addTeammateNode(t, d, "PROJ-2", "PROJ-3", 3) }))

	require.NoError(t, f.svc.AutoImport(context.Background(), f.mtixDir))
	out := f.notices.String()
	assert.Contains(t, out, "nodes 1 added, 0 updated, 0 removed")
	assert.Contains(t, out, localOnlyNotice)
	assert.Contains(t, out, "PROJ-3", "it names the added task")
}

// TestAutoImport_AddedOnlyOnSoloStore_NoLocalOnlyNotice: a store that does not
// sync prints only the count line.
func TestAutoImport_AddedOnlyOnSoloStore_NoLocalOnlyNotice(t *testing.T) {
	f := newGuardFixture(t)
	f.pull(t, f.teammateBoard(t, func(d *sqlite.ExportData) { addTeammateNode(t, d, "PROJ-2", "PROJ-3", 3) }))

	require.NoError(t, f.svc.AutoImport(context.Background(), f.mtixDir))
	assert.Contains(t, f.notices.String(), "nodes 1 added")
	assert.NotContains(t, f.notices.String(), "local only")
}
