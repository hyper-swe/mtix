// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"go/ast"
	"go/token"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// identityWriteRE matches the SQL that gives a task a new id or uid, inserts
// a task, or deletes tasks: the writes after which a task's history key could
// no longer match the task.
var identityInsertDeleteRE = regexp.MustCompile(`(?is)(?:INSERT|REPLACE)(?:\s+OR\s+\w+)?\s+INTO\s+(?:\w+\.)?nodes\b|DELETE\s+FROM\s+(?:\w+\.)?nodes\b`)
var identityUpdateRE = regexp.MustCompile(`(?is)UPDATE(?:\s+OR\s+\w+)?\s+(?:\w+\.)?nodes(?:\s+(?:AS\s+)?\w+)?\s+SET\s+`)
var identityColumnRE = regexp.MustCompile(`(?is)(^|[\s,(])(id|uid)\s*=|\(\s*(?:\w+\s*,\s*)*(?:id|uid)\b[^)]*\)\s*=`)
var identityNoiseRE = regexp.MustCompile("(?s)/\\*.*?\\*/|[\"`\\[\\]]")

// setList returns the SET list that starts at the beginning of text: up to the
// first WHERE outside parentheses, so a subquery's WHERE does not end it.
func setList(text string) string {
	depth := 0
	for i := 0; i < len(text); i++ {
		switch text[i] {
		case '(':
			depth++
		case ')':
			depth--
		}
		if depth == 0 && isSpace(text[i]) && len(text)-i >= 7 && strings.EqualFold(text[i+1:i+6], "WHERE") && isSpace(text[i+6]) {
			return text[:i]
		}
	}
	return text
}

// writesIdentity reports whether SQL text inserts, replaces or deletes nodes, or
// assigns nodes.id or nodes.uid, however the statement is quoted or aliased.
func writesIdentity(sqlText string) bool {
	text := identityNoiseRE.ReplaceAllString(sqlText, "")
	if identityInsertDeleteRE.MatchString(text) {
		return true
	}
	for _, loc := range identityUpdateRE.FindAllStringIndex(text, -1) {
		if identityColumnRE.MatchString(setList(text[loc[1]:])) {
			return true
		}
	}
	return false
}

// scanIdentityWriters lists the functions whose SQL writes nodes.id or
// nodes.uid, inserts into nodes, or deletes from nodes.
func scanIdentityWriters(t *testing.T, dir string) []updateSite {
	t.Helper()
	files := parseDir(t, dir)
	strs, decls := packageStrings(files)
	var out []updateSite
	check := func(file, fn string, root ast.Node) {
		ast.Inspect(root, func(n ast.Node) bool {
			e, ok := n.(ast.Expr)
			if !ok {
				return true
			}
			switch e.(type) {
			case *ast.BinaryExpr, *ast.BasicLit, *ast.Ident:
			default:
				return true
			}
			v, folded := foldString(e, strs)
			if !folded {
				return true
			}
			if writesIdentity(v) {
				out = append(out, updateSite{file: file, fn: fn})
			}
			return false
		})
	}
	for name, f := range files {
		for _, d := range f.Decls {
			if fn, ok := d.(*ast.FuncDecl); ok && fn.Body != nil {
				check(name, funcName(fn), fn.Body)
			}
		}
	}
	for ident, d := range decls {
		if _, isLit := d.expr.(*ast.FuncLit); isLit || d.kind == token.VAR.String() {
			check(d.file, "var "+ident, d.expr)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].file+out[i].fn < out[j].file+out[j].fn })
	return out
}

// identityWriters is the sweep of every write that changes which key a task's
// history belongs under (MTIX-95.31.19.3). Each entry says how its history
// stays with the task, or why none is needed. An unlisted writer fails.
func identityWriters() map[string]string {
	const noMove = "an import never moves history: a task with a new, changed or minted uid starts fresh and the old history stays under its old key"
	return map[string]string{
		"node_uid.go:Store.backfillUIDsFromCreateEvents": "the store fills uids; rekeyFilledHistory moves the filled nodes' history in the same transaction",
		"node_uid.go:setBackfillUID":                     "the store mints a uid for one row; rekeyHistoryExact(id, uid)",
		"import_title_identity.go:stampNewUIDs":          "import stamp: " + noMove,
		"import_merge_keep.go:mergeUnchangedContent":     "import adoption: " + noMove,
		"import.go:updateExportNode":                     "import takeover: " + noMove,
		"import.go:insertExportNode":                     "import insert or mint: " + noMove,
		"import.go:clearAllTables":                       "replace deletes then reinserts every task: only a task whose uid is present on both sides is recorded; " + noMove,
		"node_renumber.go:rewriteSubtreeIDs":             "the store's own renumber (RenumberSubtree, hub rejection): renumberRecorded rekeys and records the move; an import's local moves do not rekey",
		"sync_reconcile_rename.go:renameNodeRow":         "the store's own rename (RenameTo, ImportAs): rekeyHistoryExact(old, new) and the move is recorded",
		"node_create.go:insertNode":                      "a new task: it has no history yet",
		"sync_apply_ops_more.go:insertAppliedCreate":     "a new task from a replayed create: it has no history yet",
		"node_uid_unique.go:duplicateUIDMessage":         "operator recovery text, never executed",
		"sync_reconcile.go:DiscardLocal":                 "deletes every task and keeps the history on purpose: the re-pull replays into it, deduped by the event index",
	}
}

func TestTaskHistoryIdentity_EveryWriterOfIdOrUID_IsAccountedFor(t *testing.T) {
	listed := identityWriters()
	found := map[string]bool{}
	for _, site := range scanIdentityWriters(t, ".") {
		key := site.file + ":" + site.fn
		found[key] = true
		_, ok := listed[key]
		assert.True(t, ok, "%s writes nodes.id/uid or inserts/deletes tasks but is not in the identity sweep: move its history or say why none is needed", key)
	}
	for key := range listed {
		assert.True(t, found[key], "the identity sweep lists %s, which no longer writes a task identity", key)
	}
}

// TestIdentityWriteRE_CatchesEveryWriteForm: the sweep must not be escapable by
// another spelling of the same write.
func TestIdentityWriteRE_CatchesEveryWriteForm(t *testing.T) {
	writes := []string{
		"UPDATE nodes SET uid = ? WHERE id = ?",
		"UPDATE OR IGNORE nodes SET uid = ?",
		"update main.nodes set id = ? where id = ?",
		"REPLACE INTO nodes (id, uid) VALUES (?, ?)",
		"INSERT OR REPLACE INTO nodes (id) VALUES (?)",
		"INSERT INTO nodes SELECT * FROM staging",
		"INSERT INTO main.nodes(id) VALUES (?)",
		"DELETE FROM nodes",
		`UPDATE "nodes" SET "uid" = ? WHERE id = ?`,
		"UPDATE `nodes` SET `uid` = ?",
		"UPDATE [nodes] SET [id] = ?",
		`INSERT INTO "nodes" (id) VALUES (?)`,
		`DELETE FROM "nodes"`,
		"UPDATE nodes AS n SET uid = ? WHERE n.id = ?",
		"UPDATE nodes n SET id = ?",
		"UPDATE nodes SET (uid, title) = (?, ?) WHERE id = ?",
		"UPDATE nodes SET title = (SELECT t FROM x WHERE x.k = 1), uid = ? WHERE id = ?",
		"UPDATE nodes SET uid/*x*/= ?",
	}
	for _, w := range writes {
		assert.True(t, writesIdentity(w), "not caught: %s", w)
	}
	for _, other := range []string{"UPDATE nodes SET title = ? WHERE id = ?", "UPDATE nodes SET title = (SELECT uid FROM x WHERE y = 1) WHERE id = ?", "SELECT id FROM nodes", "INSERT INTO nodes_fts(rowid) VALUES (1)", "UPDATE nodes SET "} {
		assert.False(t, writesIdentity(other), "a false alarm: %s", other)
	}
}

func isSpace(c byte) bool { return c == ' ' || c == '\n' || c == '\t' || c == '\r' }
