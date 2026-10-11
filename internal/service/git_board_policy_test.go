// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Pure validation and source references pin the optional reader's narrow boundary.
package service

import (
	"bytes"
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

func TestMergeBaseBoard_InvalidData_IsBoundedAndPrivate(t *testing.T) {
	marker := strings.Repeat("PRIVATE_BOARD_VALUE", 30)
	valid := boardBytes(t, "ancestor board")
	cases := [][]byte{nil, []byte("null"), []byte("[]"), []byte("{broken"), append(bytes.Clone(valid), []byte(" {}")...)}
	for _, edit := range []func(*sqlite.ExportData){
		func(d *sqlite.ExportData) { d.SchemaVersion = "99." + marker },
		func(d *sqlite.ExportData) { d.NodeCount = 99 },
		func(d *sqlite.ExportData) { d.Checksum = marker },
		func(d *sqlite.ExportData) {
			d.Nodes[0].UpdatedAt = marker
			require.NoError(t, sqlite.RecomputeExportChecksum(d))
		},
	} {
		d, err := sqlite.DecodeExportData(bytes.NewReader(valid))
		require.NoError(t, err)
		edit(d)
		blob, err := json.Marshal(d)
		require.NoError(t, err)
		cases = append(cases, blob)
	}
	cases = append(cases, []byte(`{"node_count":"`+marker+`"}`))
	for i, raw := range cases {
		t.Run(string(rune('A'+i)), func(t *testing.T) {
			data, p, err := decodeGitBoard(raw)
			require.ErrorIs(t, err, ErrNoMergeBase)
			require.Nil(t, data)
			require.Empty(t, p)
			require.NotContains(t, err.Error(), marker)
			require.Less(t, len(err.Error()), 200)
			root := boardSingleRepo(t, raw)
			before := boardSnapshot(t, root)
			got, _, readErr := NewSyncService(nil, nil, nil).MergeBaseBoard(context.Background(), root)
			require.ErrorIs(t, readErr, ErrNoMergeBase)
			require.Nil(t, got)
			require.NotContains(t, readErr.Error(), marker)
			require.Less(t, len(readErr.Error()), 200)
			require.Equal(t, before, boardSnapshot(t, root))
		})
	}
}

func TestMergeBaseBoard_CompatibleSchemaRules_ArePreserved(t *testing.T) {
	for _, schema := range []string{"", "unparseable", "1.0.0", "2.0.0"} {
		t.Run(schema, func(t *testing.T) {
			data, err := sqlite.DecodeExportData(bytes.NewReader(boardBytes(t, "ancestor board")))
			require.NoError(t, err)
			data.SchemaVersion = schema
			blob, err := json.Marshal(data)
			require.NoError(t, err)
			got, _, err := decodeGitBoard(blob)
			require.NoError(t, err)
			require.Equal(t, schema, got.SchemaVersion)
		})
	}
}

func TestMergeBaseBoard_OnlyImportPathCalls(t *testing.T) {
	root := filepath.Join("..", "..")
	require.NoError(t, filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		require.Empty(t, boardMethodReferences(path, f), "unexpected ancestor reader reference in %s", path)
		return nil
	}))
}

func boardMethodReferences(path string, f *ast.File) []string {
	var problems []string
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok { // Package initializers are checked too.
			ast.Inspect(decl, func(n ast.Node) bool {
				if boardReference(n) {
					problems = append(problems, "package reference")
				}
				return true
			})
			continue
		}
		allowed := filepath.ToSlash(path) == "../../internal/service/sync_autoimport_guard.go" && fn.Name.Name == "refuseLossyImport"
		if allowed {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if boardReference(n) {
				problems = append(problems, fn.Name.Name)
			}
			return true
		})
	}
	return problems
}

func boardReference(n ast.Node) bool {
	selected, ok := n.(*ast.SelectorExpr)
	return ok && selected.Sel.Name == "MergeBaseBoard"
}

func TestMergeBaseBoard_ReferenceGuard_IncludesMethodValues(t *testing.T) {
	for _, source := range []string{"var f = svc.MergeBaseBoard", "func bad(){ f := svc.MergeBaseBoard; _ = f }", "func bad(){ f := (*SyncService).MergeBaseBoard; _ = f }", "func bad(){ svc.MergeBaseBoard(ctx,root) }"} {
		f, err := parser.ParseFile(token.NewFileSet(), "fixture.go", "package fixture\n"+source, 0)
		require.NoError(t, err)
		require.NotEmpty(t, boardMethodReferences("fixture.go", f))
	}
	f, err := parser.ParseFile(token.NewFileSet(), "fixture.go", "package fixture\nfunc refuseLossyImport(){ svc.MergeBaseBoard(ctx,root) }", 0)
	require.NoError(t, err)
	require.Empty(t, boardMethodReferences("../../internal/service/sync_autoimport_guard.go", f))
}

func TestMergeBaseBoard_InvalidCapsAndCancellation(t *testing.T) {
	root := boardSingleRepo(t, boardBytes(t, "ancestor board"))
	for _, capBytes := range []int64{0, -1, 1} {
		svc := NewSyncService(nil, nil, nil)
		svc.MaxImportSize = capBytes
		assertBoardUnavailable(t, svc, root)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := NewSyncService(nil, nil, nil).MergeBaseBoard(ctx, root)
	require.ErrorIs(t, err, ErrNoMergeBase)
	require.ErrorIs(t, err, context.Canceled)
}

func TestMergeBaseBoard_MetadataLayouts_RefuseSafely(t *testing.T) {
	root := t.TempDir()
	_, err := boardRepository(filepath.Join(root, "absent"))
	require.ErrorIs(t, err, ErrNoMergeBase)
	git := filepath.Join(root, ".git")
	boardWrite(t, git, []byte("gitdir: ../shared"))
	_, err = boardRepository(root)
	require.ErrorIs(t, err, ErrNoMergeBase)
	require.NoError(t, os.Remove(git))
	require.NoError(t, os.Symlink(t.TempDir(), git))
	_, err = boardRepository(root)
	require.ErrorIs(t, err, ErrNoMergeBase)
	require.NoError(t, os.Remove(git))
	boardWrite(t, filepath.Join(git, "info"), []byte("unsupported metadata"))
	_, err = boardRepository(root)
	require.ErrorIs(t, err, ErrNoMergeBase)
	require.NoError(t, os.Remove(filepath.Join(git, "info")))
	require.NoError(t, os.Symlink(t.TempDir(), filepath.Join(git, "info")))
	_, err = boardRepository(root)
	require.ErrorIs(t, err, ErrNoMergeBase)
}
