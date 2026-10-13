// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// historyCensus lists every non-test store file with "UPDATE nodes SET"
// statements and how many it holds. wired files record task_history through
// withHistoryTx / createNodeTx (MTIX-95.31.19.2); pending files are the
// remaining write paths scheduled for MTIX-95.31.19.3 (A2). An unlisted file,
// or a changed count, fails so a new write path cannot skip the recorder.
var historyCensus = map[string]struct {
	count   int
	pending bool
}{
	"node_update.go": {1, false}, "claim.go": {3, false}, "transition.go": {2, false},
	"defer.go": {2, false}, "cancel.go": {2, false}, "node_delete.go": {3, false},
	"context.go": {2, true}, "dependency.go": {2, true}, "import.go": {1, true},
	"import_merge_keep.go": {1, true}, "import_merge_streams.go": {1, true},
	"import_title_identity.go": {1, true}, "import_workflow_conflict.go": {1, true},
	"node_renumber.go": {1, true}, "node_uid_unique.go": {1, true}, "node_uid.go": {1, true},
	"progress.go": {1, true}, "store.go": {1, true}, "sync_apply.go": {6, true},
	"sync_reconcile.go": {3, true}, "sync_update_classification.go": {1, true},
	"sync_workflow_write.go": {1, true},
}

func TestTaskHistoryCensus_UpdateNodesStatements_AreAllListed(t *testing.T) {
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	found := map[string]int{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		require.NoError(t, err)
		if n := strings.Count(string(b), "UPDATE nodes SET"); n > 0 {
			found[f] = n
		}
	}
	for f, n := range found {
		entry, ok := historyCensus[f]
		if assert.True(t, ok, "%s holds %d UPDATE nodes SET statements but is not in the history census: wire it to withHistoryTx or list it pending", f, n) {
			assert.Equal(t, entry.count, n, "%s: statement count changed; review the new write path", f)
		}
	}
	for f := range historyCensus {
		assert.Contains(t, found, f, "census lists %s, which no longer holds a statement", f)
	}
}
