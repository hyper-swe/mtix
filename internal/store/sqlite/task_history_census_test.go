// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"go/ast"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Census states: wired (every path records through withHistoryTx or
// createNodeTx), pending (A2 will wire it), excluded (writes only columns that
// are not history fields: activity, derived progress).
const (
	censusWired    = "wired"
	censusPending  = "pending"
	censusExcluded = "excluded"
)

// historyCensus maps "file:Func" to the number of UPDATE nodes statements in
// that function and whether it is still pending MTIX-95.31.19.3 (A2). A
// non-pending function runs only inside withHistoryTx or createNodeTx
// (MTIX-95.31.19.2); an unlisted function, or a changed count, fails.
var historyCensus = map[string]struct {
	count int
	state string
}{
	"node_update.go:execUpdateQuery": {1, censusWired}, "claim.go:Store.claimNodeTx": {1, censusWired},
	"claim.go:Store.UnclaimNode": {1, censusWired}, "claim.go:Store.ForceReclaimNode": {1, censusWired},
	"transition.go:applyTransitionUpdate": {1, censusWired}, "transition.go:appendActivityEntry": {1, censusExcluded},
	"defer.go:Store.executeDeferTx": {1, censusWired}, "defer.go:redeferTx": {1, censusWired},
	"cancel.go:applyCancelUpdate": {1, censusWired}, "cancel.go:cancelDescendants": {1, censusWired},
	"node_delete.go:Store.DeleteNode": {1, censusWired}, "node_delete.go:cascadeDelete": {1, censusWired},
	"node_delete.go:restoreNodes": {1, censusWired},
	// Pending A2: every remaining write path.
	"context.go:Store.SetAnnotations": {1, censusPending}, "context.go:Store.mutateAnnotations": {1, censusPending},
	"progress.go:recomputeNodeProgress":                 {1, censusPending},
	"dependency.go:autoBlockNode":                       {1, censusPending},
	"dependency.go:restoreUnblockedNode":                {1, censusPending},
	"import_merge_keep.go:mergeUnchangedContent":        {1, censusPending},
	"import_merge_streams.go:writeNodeStreams":          {1, censusPending},
	"import_title_identity.go:stampNewUIDs":             {1, censusPending},
	"import_workflow_conflict.go:writeWorkflowGroup":    {1, censusPending},
	"import.go:updateExportNode":                        {1, censusPending},
	"node_renumber.go:rewriteSubtreeIDs":                {2, censusPending},
	"node_uid_unique.go:duplicateUIDMessage":            {1, censusPending},
	"node_uid.go:setBackfillUID":                        {1, censusPending},
	"node_uid.go:Store.backfillUIDsFromCreateEvents":    {1, censusPending},
	"store.go:Store.UpdateProgress":                     {1, censusPending},
	"sync_apply.go:recomputeContentHashRow":             {1, censusPending},
	"sync_apply.go:syncApplier.applyComment":            {1, censusPending},
	"sync_apply.go:syncApplier.applyDelete":             {1, censusPending},
	"sync_apply.go:syncApplier.applySetAcceptance":      {1, censusPending},
	"sync_apply.go:syncApplier.applySetPrompt":          {1, censusPending},
	"sync_apply.go:syncApplier.applyUpdateField":        {1, censusPending},
	"sync_reconcile.go:assignParentToFormerRoots":       {1, censusPending},
	"sync_reconcile.go:renameNodeRow":                   {2, censusPending},
	"sync_update_classification.go:applyIssueTypeField": {1, censusPending},
	"sync_workflow_write.go:writeWorkflowColumns":       {1, censusPending},
}

func siteKey(s updateSite) string { return s.file + ":" + s.fn }

func TestTaskHistoryCensus_Scanner_FindsEveryStatementShape(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []updateSite
	}{
		{"multi-line raw string", "package x\n\nfunc Multi() string {\n\treturn `UPDATE nodes\n\t   SET status = 'done'`\n}\n", []updateSite{{"f.go", "Multi"}}},
		{"lowercase and extra spaces", "package x\n\nfunc Lower() string { return \"update  nodes set title = ''\" }\n", []updateSite{{"f.go", "Lower"}}},
		{"package const used by a method", "package x\n\ntype S struct{}\n\nconst q = \"UPDATE nodes SET status = ?\"\n\nfunc (s *S) Use() string { return q }\n", []updateSite{{"f.go", "S.Use"}}},
		{"unreferenced package const", "package x\n\nconst orphan = \"UPDATE nodes SET status = ?\"\n", []updateSite{{"f.go", "const orphan"}}},
		{"concatenated literal", "package x\n\nfunc Cat() string { return \"UPDATE nodes \" + \"SET status = ?\" }\n", []updateSite{{"f.go", "Cat"}}},
		{"concatenated consts", "package x\n\nconst a = \"UPDATE nodes \"\nconst b = a + \"SET x = 1\"\n\nfunc Use() string { return b }\n", []updateSite{{"f.go", "Use"}}},
		{"package func literal var", "package x\n\nvar run = func() string { return \"UPDATE nodes SET status = ?\" }\n", []updateSite{{"f.go", "var run"}}},
		{"func literal inside a method", "package x\n\ntype S struct{}\n\nfunc (s *S) M() func() string { return func() string { return \"UPDATE nodes SET a = 1\" } }\n", []updateSite{{"f.go", "S.M"}}},
		{"no statement", "package x\n\nfunc None() string { return \"SELECT 1 FROM nodes\" }\n", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, "f.go"), []byte(tt.src), 0o600))
			assert.ElementsMatch(t, tt.want, scanUpdateStatements(t, dir))
		})
	}
}

func TestTaskHistoryCensus_UpdateNodesStatements_AreAllListed(t *testing.T) {
	counts := map[string]int{}
	for _, site := range scanUpdateStatements(t, ".") {
		counts[siteKey(site)]++
	}
	for key, n := range counts {
		entry, ok := historyCensus[key]
		if assert.True(t, ok, "%s holds %d UPDATE nodes statement(s) but is not in the history census: record it through withHistoryTx or list it pending A2", key, n) {
			assert.Equal(t, entry.count, n, "%s: statement count changed; review the new write path", key)
		}
	}
	for key := range historyCensus {
		assert.Contains(t, counts, key, "census lists %s, which no longer holds a statement", key)
	}
}

// historyRecordedCore are the transaction bodies whose UPDATE nodes statements
// are marked wired in historyCensus. A caller must open the transaction with
// withHistoryTx, or reach one of these through a function that already did.
func historyRecordedCore() []string {
	return []string{"executeTransitionTx", "executeDeferTx", "executeCancelTx", "claimNodeTx", "executeUpdateTx", "createNodeTx"}
}

// historyEntryAllowlist names the functions that call a recorded core without
// opening the transaction themselves, each with the reason it still records.
func historyEntryAllowlist() map[string]string {
	return map[string]string{
		"Store.executeDeferTx":        "runs inside DeferNode's withHistoryTx",
		"Store.createNodeWithHistory": "createNodeTx records the initial entries itself",
		"Store.createNodeAllocatedTx": "createNodeTx records the initial entries itself",
		"Store.createNodeTx":          "records the initial entries itself",
		"Store.CreateNodeAllocated":   "createNodeTx records the initial entries itself (via createNodeAllocatedTx)",
		"Store.CreateNodesAllocated":  "createNodeTx records the initial entries itself (via createNodeAllocatedTx)",
		"Store.claimNodeTx":           "runs inside ClaimNode's withHistoryTx or createNodeTx",
		"Store.executeUpdateTx":       "runs inside UpdateNode's withHistoryTx",
		"Store.executeTransitionTx":   "runs inside a withHistoryTx caller",
		"Store.executeCancelTx":       "runs inside CancelNode's withHistoryTx",
		"Store.insertAndClaimNode":    "called by createNodeTx, which records",
	}
}

// TestTaskHistoryCensus_EntryPoints_OpenTheirTransactionWithHistory fails when
// a function calls a recorded core outside withHistoryTx and outside the
// allowlist, so a new entry point cannot reuse a wired statement unrecorded.
func TestTaskHistoryCensus_EntryPoints_OpenTheirTransactionWithHistory(t *testing.T) {
	core := map[string]bool{}
	for _, n := range historyRecordedCore() {
		core[n] = true
	}
	allow := historyEntryAllowlist()
	for file, parsed := range parseDir(t, ".") {
		for _, decl := range parsed.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			calls := calledNames(fn.Body)
			var hit string
			for n := range calls {
				if core[n] {
					hit = n
				}
			}
			if hit == "" || calls["withHistoryTx"] {
				continue
			}
			_, listed := allow[funcName(fn)]
			assert.True(t, listed, "%s: %s calls %s without withHistoryTx: record it or list it with a reason", file, funcName(fn), hit)
		}
	}
}

// calledNames returns the identifiers called in body (f() and x.f()).
func calledNames(body *ast.BlockStmt) map[string]bool {
	out := map[string]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch f := call.Fun.(type) {
		case *ast.Ident:
			out[f.Name] = true
		case *ast.SelectorExpr:
			out[f.Sel.Name] = true
		}
		return true
	})
	return out
}
