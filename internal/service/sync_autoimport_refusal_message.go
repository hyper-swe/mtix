// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// syncedFreshProject names where a file can still be merged when the project
// syncs with a hub (MTIX-112.17.16.16).
const syncedFreshProject = "a new project made with mtix init that has no hub configured (a copy of the project directory " +
	"carries the sync mark and the hub config, so it still refuses)"

// syncedMergeWays stands in a refusal's list of ways out for the merge import
// on a store that syncs with a hub (MTIX-112.17.16.16).
const syncedMergeWays = "take the hub's board with mtix sync pull and then end this refusal with mtix sync --fix (a pull alone " +
	"leaves it pending), because a merge import is refused on a project that " +
	"syncs (it writes no sync journal, so a later hub edit would silently overwrite the merged values); to load the " +
	"file anyway, merge it into " + syncedFreshProject

// syncedMergeNote stands in the refusal for the merge option on a store that
// syncs with a hub.
const syncedMergeNote = "  mtix sync pull, then mtix sync --fix         take the hub's board, then end this refusal: a pull alone leaves " +
	"it pending. A merge import of the file is " +
	"refused on a project that syncs with a hub: it writes no sync journal, so a later hub edit would silently " +
	"overwrite the merged values. To load the file anyway, merge it into " + syncedFreshProject + "\n"

// syncedReplaceNote stands in the refusal for the replace option on a store
// that syncs with a hub: a replace import is refused there (MTIX-107.31), so
// it is not offered, and the way to take the hub's board is named instead.
const syncedReplaceNote = "  A replace import of the file is refused on a project that syncs with a hub: the sync log keeps the " +
	"events of the tasks it would delete. To rebuild this store from the hub instead, run " +
	sqlite.SyncLogRecovery + "\n"

// syncedReplaceWays stands in a refusal's list of ways out for the replace
// import on a store that syncs with a hub (MTIX-107.31).
const syncedReplaceWays = "rebuild this store from the hub, because a replace import is refused on a project that syncs: " +
	sqlite.SyncLogRecovery
