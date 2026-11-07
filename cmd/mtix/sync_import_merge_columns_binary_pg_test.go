//go:build !windows

// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// structuralEdits changes one column of the node at index i in an exported
// board: the class table of principle F, run on the real binary.
func structuralEdits() map[string]func(d *sqlite.ExportData, i int) {
	return map[string]func(d *sqlite.ExportData, i int){
		"project":     func(d *sqlite.ExportData, i int) { d.Nodes[i].Project = "OTHER" },
		"parent_id":   func(d *sqlite.ExportData, i int) { d.Nodes[i].ParentID = "TEST-1" },
		"depth":       func(d *sqlite.ExportData, i int) { d.Nodes[i].Depth = 3 },
		"seq":         func(d *sqlite.ExportData, i int) { d.Nodes[i].Seq = 7 },
		"node_type":   func(d *sqlite.ExportData, i int) { d.Nodes[i].NodeType = "micro" },
		"defer_until": func(d *sqlite.ExportData, i int) { d.Nodes[i].DeferUntil = "2027-05-05T00:00:00Z" },
		"progress":    func(d *sqlite.ExportData, i int) { d.Nodes[i].Progress = 0.5 },
	}
}

func exportedNodes(t *testing.T, f *quarantineRecoveryCLI) map[string]map[string]any {
	t.Helper()
	out := f.run(t, "export")
	require.Zero(t, out.exitCode, "%s", out.stderr)
	var raw struct {
		Nodes []map[string]any `json:"nodes"`
	}
	require.NoError(t, json.Unmarshal([]byte(out.stdout), &raw))
	byTask := map[string]map[string]any{}
	for _, n := range raw.Nodes {
		byTask[fmt.Sprint(n["id"])] = n
	}
	return byTask
}

// TestImportMerge_EveryStructuralColumn_RealBinary_AgreesOnAFreshCloneOrIsNamed:
// for each column the merge writes that no op carries, a file changes that
// column alone on one task; after merge, push and a fresh clone, the clone
// holds the importer's value or the import's Local only line names the column
// (principle F, on the real binary and a real hub).
func TestImportMerge_EveryStructuralColumn_RealBinary_AgreesOnAFreshCloneOrIsNamed(t *testing.T) {
	_ = requireCmdPG(t)
	f := newQuarantineRecoveryCLI(t, buildQuarantineRecoveryCLI(t))
	cols := structuralEdits()
	names := make([]string, 0, len(cols))
	for c := range cols {
		names = append(names, c)
	}
	for i := 0; i <= len(names); i++ { // task 1 stays unedited (the parent target)
		out := f.run(t, "create", fmt.Sprintf("Column task %d", i))
		require.Zero(t, out.exitCode, "%s", out.stderr)
	}
	out := f.run(t, "sync", "push", "--insecure-tls")
	require.Zero(t, out.exitCode, "%s%s", out.stdout, out.stderr)
	out = f.run(t, "export")
	var data sqlite.ExportData
	require.NoError(t, json.Unmarshal([]byte(out.stdout), &data))
	edited := map[string]string{} // column -> task id
	for k, c := range names {
		idx := k + 1 // TEST-2 onwards
		require.Equal(t, fmt.Sprintf("TEST-%d", idx+1), data.Nodes[idx].ID)
		cols[c](&data, idx)
		edited[c] = data.Nodes[idx].ID
	}
	require.NoError(t, sqlite.RecomputeExportChecksum(&data))
	raw, err := json.Marshal(&data)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "board.json")
	require.NoError(t, os.WriteFile(path, raw, 0o600))

	out = f.run(t, "import", path, "--mode", "merge", "--prefer", "theirs", "--force")
	require.Zero(t, out.exitCode, "%s%s", out.stdout, out.stderr)
	mine := exportedNodes(t, &f)
	for _, args := range [][]string{{"sync", "push", "--insecure-tls"}} {
		o := f.run(t, args...)
		require.Zero(t, o.exitCode, "%v: %s%s", args, o.stdout, o.stderr)
	}
	clone := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(clone, ".mtix"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(clone, ".mtix", "config.yaml"), []byte("prefix: TEST\nmax_depth: 10\n"), 0o644))
	fresh := quarantineRecoveryCLI{binary: f.binary, project: clone, env: f.env, hub: f.hub}
	o := fresh.run(t, "sync", "clone", "--insecure-tls")
	require.Zero(t, o.exitCode, "%s%s", o.stdout, o.stderr)
	theirs := exportedNodes(t, &fresh)

	for col, id := range edited {
		agree := fmt.Sprint(mine[id][col]) == fmt.Sprint(theirs[id][col])
		named := regexpTaskField(out.stdout, id, col)
		require.True(t, agree || named, "%s of %s: importer %v, clone %v, and the Local only line does not name it:\n%s",
			col, id, mine[id][col], theirs[id][col], out.stdout)
	}
}

// regexpTaskField reports whether the Local only line of an import's output
// lists field for task id (the line reads "Local only (...): ID (a, b); ID (c)").
func regexpTaskField(output, id, field string) bool {
	re := regexp.MustCompile(`(?m)^Local only .*$`)
	line := re.FindString(output)
	for _, part := range strings.Split(line, "; ") {
		if !strings.Contains(part, id+" (") {
			continue
		}
		for _, f := range strings.Split(part[strings.Index(part, "(")+1:], ", ") {
			if strings.TrimRight(f, ")") == field {
				return true
			}
		}
	}
	return false
}
