// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// updateSite is one "UPDATE nodes ... SET" statement and its enclosing function.
type updateSite struct{ file, fn string }

// updateNodesRE matches the statement however the SQL is wrapped or cased.
var updateNodesRE = regexp.MustCompile(`(?is)UPDATE\s+nodes\s+SET`)

// parseDir parses the non-test Go files of dir.
func parseDir(t *testing.T, dir string) map[string]*ast.File {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	require.NoError(t, err)
	fset := token.NewFileSet()
	out := map[string]*ast.File{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		require.NoError(t, err)
		parsed, err := parser.ParseFile(fset, f, src, 0)
		require.NoError(t, err)
		out[filepath.Base(f)] = parsed
	}
	return out
}

// funcName names a declaration as Func or Type.Method.
func funcName(d *ast.FuncDecl) string {
	if d.Recv == nil || len(d.Recv.List) == 0 {
		return d.Name.Name
	}
	typ := d.Recv.List[0].Type
	if star, ok := typ.(*ast.StarExpr); ok {
		typ = star.X
	}
	if id, ok := typ.(*ast.Ident); ok {
		return id.Name + "." + d.Name.Name
	}
	return d.Name.Name
}

// scanUpdateStatements lists every UPDATE nodes statement (whitespace and case
// insensitive, in any string literal) with its enclosing function.
func scanUpdateStatements(t *testing.T, dir string) []updateSite {
	t.Helper()
	var out []updateSite
	for name, file := range parseDir(t, dir) {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				text, err := strconv.Unquote(lit.Value)
				if err != nil {
					return true
				}
				for range updateNodesRE.FindAllString(text, -1) {
					out = append(out, updateSite{file: name, fn: funcName(fn)})
				}
				return true
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].file+out[i].fn < out[j].file+out[j].fn
	})
	return out
}

// historyCensus maps "file:Func" to the number of UPDATE nodes statements in
// that function and whether it is still pending MTIX-95.31.19.3 (A2). A
// non-pending function runs only inside withHistoryTx or createNodeTx
// (MTIX-95.31.19.2); an unlisted function, or a changed count, fails.
var historyCensus = map[string]struct {
	count   int
	pending bool
}{
	"node_update.go:execUpdateQuery": {1, false}, "claim.go:Store.claimNodeTx": {1, false},
	"claim.go:Store.UnclaimNode": {1, false}, "claim.go:Store.ForceReclaimNode": {1, false},
	"transition.go:applyTransitionUpdate": {1, false}, "transition.go:appendActivityEntry": {1, false},
	"defer.go:Store.executeDeferTx": {1, false}, "defer.go:redeferTx": {1, false},
	"cancel.go:applyCancelUpdate": {1, false}, "cancel.go:cancelDescendants": {1, false},
	"node_delete.go:Store.DeleteNode": {1, false}, "node_delete.go:cascadeDelete": {1, false},
	"node_delete.go:restoreNodes": {1, false},
	// Pending A2: every remaining write path.
	"context.go:Store.SetAnnotations": {1, true}, "context.go:Store.mutateAnnotations": {1, true},
	"progress.go:recomputeNodeProgress":                 {1, true},
	"dependency.go:autoBlockNode":                       {1, true},
	"dependency.go:restoreUnblockedNode":                {1, true},
	"import_merge_keep.go:mergeUnchangedContent":        {1, true},
	"import_merge_streams.go:writeNodeStreams":          {1, true},
	"import_title_identity.go:stampNewUIDs":             {1, true},
	"import_workflow_conflict.go:writeWorkflowGroup":    {1, true},
	"import.go:updateExportNode":                        {1, true},
	"node_renumber.go:rewriteSubtreeIDs":                {2, true},
	"node_uid_unique.go:duplicateUIDMessage":            {1, true},
	"node_uid.go:setBackfillUID":                        {1, true},
	"node_uid.go:Store.backfillUIDsFromCreateEvents":    {1, true},
	"store.go:Store.UpdateProgress":                     {1, true},
	"sync_apply.go:recomputeContentHashRow":             {1, true},
	"sync_apply.go:syncApplier.applyComment":            {1, true},
	"sync_apply.go:syncApplier.applyDelete":             {1, true},
	"sync_apply.go:syncApplier.applySetAcceptance":      {1, true},
	"sync_apply.go:syncApplier.applySetPrompt":          {1, true},
	"sync_apply.go:syncApplier.applyUpdateField":        {1, true},
	"sync_reconcile.go:assignParentToFormerRoots":       {1, true},
	"sync_reconcile.go:renameNodeRow":                   {2, true},
	"sync_update_classification.go:applyIssueTypeField": {1, true},
	"sync_workflow_write.go:writeWorkflowColumns":       {1, true},
}

func siteKey(s updateSite) string { return s.file + ":" + s.fn }

func TestTaskHistoryCensus_Scanner_FindsMultiLineAndLowercaseStatements(t *testing.T) {
	dir := t.TempDir()
	src := "package x\n\nfunc Multi() string {\n\treturn `UPDATE nodes\n\t   SET status = 'done'`\n}\n\nfunc Lower() string { return \"update  nodes set title = ''\" }\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "fixture.go"), []byte(src), 0o600))
	got := scanUpdateStatements(t, dir)
	assert.ElementsMatch(t, []updateSite{{"fixture.go", "Multi"}, {"fixture.go", "Lower"}}, got)
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
