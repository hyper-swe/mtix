// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Ancestor-board behavior is verified using an actual owned pull fixture.
package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

func TestMergeBaseBoard_GitPullFixture_ReturnsAncestorBoard(t *testing.T) {
	root, blob, oid := boardPullFixture(t)
	before := boardSnapshot(t, root)
	svc := NewSyncService(nil, nil, nil)
	data, p, err := svc.MergeBaseBoard(context.Background(), root)
	require.NoError(t, err)
	require.Equal(t, "ancestor board", data.Nodes[0].Title)
	require.Equal(t, oid, p.BaseOID)
	require.Equal(t, [2]string{"ORIG_HEAD", "@{upstream}"}, p.Candidates)
	require.Equal(t, "git-merge-base", p.Source)
	require.Equal(t, "PROJ-1", data.Nodes[0].ID)
	require.Equal(t, "2026-10-01T00:00:00Z", data.Nodes[0].CreatedAt)
	require.Equal(t, fmt.Sprintf("%x", sha256.Sum256(blob)), p.BlobSHA256)
	require.Equal(t, "41589749e35326ce49c3e30ec4aa6cea672b73c5d640d5d7ee56563e2a2aadfb", p.ContentSHA256)
	require.Equal(t, "current", p.Form)
	require.Equal(t, before, boardSnapshot(t, root))
}

func TestMergeBaseBoard_Form100_Parsed(t *testing.T) {
	expected := map[string]struct{ form, hash string }{
		"0.3.0": {"1.0.0 without uids", "c98dc3471640b3f0704319871dd0c49f559893bdd25896a14a56894bffcb588b"},
		"0.5.3": {"1.0.0", "bc4723856df9c1bf328e21f2dd77dae269a37a3cb91fc17bf1de30faa85381f6"},
		"0.5.4": {"current", "41589749e35326ce49c3e30ec4aa6cea672b73c5d640d5d7ee56563e2a2aadfb"},
	}
	for _, version := range []string{"0.3.0", "0.5.3", "0.5.4"} {
		t.Run(version, func(t *testing.T) {
			blob := historicalBoard(t, version)
			root := boardSingleRepo(t, blob)
			before := boardSnapshot(t, root)
			data, p, err := NewSyncService(nil, nil, nil).MergeBaseBoard(context.Background(), root)
			require.NoError(t, err)
			require.Equal(t, version, data.MtixVersion)
			require.Equal(t, "ancestor board", data.Nodes[0].Title)
			require.Equal(t, version != "0.3.0", data.Nodes[0].UID != "")
			if version == "0.5.3" {
				require.Equal(t, `{"fixture":"kept"}`, data.Nodes[0].Metadata)
			}
			require.Equal(t, expected[version].form, p.Form)
			require.Equal(t, expected[version].hash, p.ContentSHA256)
			require.Equal(t, fmt.Sprintf("%x", sha256.Sum256(blob)), p.BlobSHA256)
			require.Equal(t, before, boardSnapshot(t, root))
		})
	}
}

func historicalBoard(t *testing.T, version string) []byte {
	t.Helper()
	data, err := sqlite.DecodeExportData(bytes.NewReader(boardBytes(t, "ancestor board")))
	require.NoError(t, err)
	data.MtixVersion = version
	if version != "0.5.4" {
		data, err = sqlite.Schema100Form(data)
		require.NoError(t, err)
	}
	if version == "0.3.0" {
		data, err = sqlite.WithoutUIDs(data)
		require.NoError(t, err)
	}
	if version == "0.5.3" {
		data.Nodes[0].Metadata = `{"fixture":"kept"}`
		require.NoError(t, sqlite.RecomputeExportChecksum(data))
	}
	out, err := json.Marshal(data)
	require.NoError(t, err)
	return out
}

func TestMergeBaseBoard_Refusals_DoNotWrite(t *testing.T) {
	cases := []struct {
		name string
		edit func(*testing.T, string)
	}{
		{"missing-origin", func(t *testing.T, r string) { require.NoError(t, os.Remove(filepath.Join(r, ".git", "ORIG_HEAD"))) }},
		{"missing-upstream", func(t *testing.T, r string) { boardGit(t, r, "config", "--unset", "branch.main.merge") }},
		{"detached", func(t *testing.T, r string) { boardGit(t, r, "checkout", "--detach") }},
		{"missing-base-blob", func(t *testing.T, r string) {
			boardGit(t, r, "rm", ".mtix/tasks.json")
			boardGit(t, r, "commit", "-m", "fixture absent")
			boardGit(t, r, "update-ref", "ORIG_HEAD", "HEAD")
			boardGit(t, r, "update-ref", "refs/remotes/origin/main", "HEAD")
			boardWrite(t, filepath.Join(r, ".mtix", "tasks.json"), boardBytes(t, "untracked"))
		}},
		{"grafts", func(t *testing.T, r string) { boardWrite(t, filepath.Join(r, ".git", "info", "grafts"), nil) }},
		{"common-dir", func(t *testing.T, r string) {
			boardWrite(t, filepath.Join(r, ".git", "commondir"), []byte("../outside"))
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := boardSingleRepo(t, boardBytes(t, "ancestor board"))
			c.edit(t, root)
			assertBoardUnavailable(t, NewSyncService(nil, nil, nil), root)
		})
	}
}

func assertBoardUnavailable(t *testing.T, svc *SyncService, root string) {
	t.Helper()
	before := boardSnapshot(t, root)
	data, p, err := svc.MergeBaseBoard(context.Background(), root)
	require.ErrorIs(t, err, ErrNoMergeBase)
	require.Nil(t, data)
	require.Empty(t, p.BaseOID)
	require.Less(t, len(err.Error()), 200)
	require.Equal(t, before, boardSnapshot(t, root))
}

func TestMergeBaseBoard_MgitManagedRepo_AncestorOrCleanFallback(t *testing.T) {
	root := t.TempDir()
	boardWrite(t, filepath.Join(root, ".mgit", "metadata"), []byte("owned substrate"))
	boardWrite(t, filepath.Join(root, ".mtix", "tasks.json"), boardBytes(t, "uncommitted"))
	assertBoardUnavailable(t, NewSyncService(nil, nil, nil), root)
	gitRoot := boardSingleRepo(t, boardBytes(t, "ancestor board"))
	boardWrite(t, filepath.Join(gitRoot, ".mgit", "metadata"), []byte("owned substrate"))
	data, _, err := NewSyncService(nil, nil, nil).MergeBaseBoard(context.Background(), gitRoot)
	require.NoError(t, err)
	require.Equal(t, "ancestor board", data.Nodes[0].Title)
}

func TestMergeBaseBoard_ShallowClone_ErrNoMergeBase(t *testing.T) {
	source, _, _ := boardPullFixture(t)
	root := filepath.Join(t.TempDir(), "shallow")
	boardGit(t, filepath.Dir(root), "clone", "--depth=1", "--branch", "main", "file://"+source, root)
	boardWrite(t, filepath.Join(root, ".git", "ORIG_HEAD"), []byte(strings.TrimSpace(boardGit(t, source, "rev-parse", "ORIG_HEAD"))+"\n"))
	assertBoardUnavailable(t, NewSyncService(nil, nil, nil), root)
}

func TestMergeBaseBoard_AmbiguousBase_Refused(t *testing.T) {
	root := boardSingleRepo(t, boardBytes(t, "ancestor board"))
	base := strings.TrimSpace(boardGit(t, root, "rev-parse", "HEAD"))
	tree := strings.TrimSpace(boardGit(t, root, "rev-parse", "HEAD^{tree}"))
	a := strings.TrimSpace(boardGit(t, root, "commit-tree", tree, "-p", base, "-m", "branch a"))
	b := strings.TrimSpace(boardGit(t, root, "commit-tree", tree, "-p", base, "-m", "branch b"))
	left := strings.TrimSpace(boardGit(t, root, "commit-tree", tree, "-p", a, "-p", b, "-m", "merge left"))
	right := strings.TrimSpace(boardGit(t, root, "commit-tree", tree, "-p", b, "-p", a, "-m", "merge right"))
	boardGit(t, root, "update-ref", "ORIG_HEAD", left)
	boardGit(t, root, "update-ref", "refs/remotes/origin/main", right)
	require.Len(t, strings.Fields(boardGit(t, root, "merge-base", "--all", "ORIG_HEAD", "@{upstream}")), 2)
	assertBoardUnavailable(t, NewSyncService(nil, nil, nil), root)
}

// Canonical content omits export time and formatting; raw provenance retains exact bytes.
func TestMergeBaseBoard_FormattingAndExportTime_PreserveCanonicalHash(t *testing.T) {
	original := boardBytes(t, "ancestor board")
	var envelope map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(original, &envelope))
	envelope["exported_at"] = json.RawMessage(`"2026-10-02T00:00:00Z"`)
	changed, err := json.MarshalIndent(envelope, "", "  ")
	require.NoError(t, err)
	require.NotEqual(t, sha256.Sum256(original), sha256.Sum256(changed))
	for _, blob := range [][]byte{original, changed} {
		root := boardSingleRepo(t, blob)
		data, p, err := NewSyncService(nil, nil, nil).MergeBaseBoard(context.Background(), root)
		require.NoError(t, err)
		require.Equal(t, "2026-10-01T00:00:00Z", data.Nodes[0].CreatedAt)
		require.Equal(t, fmt.Sprintf("%x", sha256.Sum256(blob)), p.BlobSHA256)
		require.Equal(t, "41589749e35326ce49c3e30ec4aa6cea672b73c5d640d5d7ee56563e2a2aadfb", p.ContentSHA256)
	}
}

// Each clone has an available committed board even when its layout is unsupported.
func boardReadableClone(t *testing.T, layout string) (string, string, []byte, string) {
	t.Helper()
	blob := boardBytes(t, "ancestor board")
	source := boardSingleRepo(t, blob)
	root := filepath.Join(t.TempDir(), "clone")
	remote := "file://" + source
	var flags []string
	switch layout {
	case "shallow":
		flags = []string{"--depth=1"}
	case "borrowed":
		flags, remote = []string{"--shared"}, source
	case "dissociated":
		flags = []string{"--reference", source, "--dissociate"}
	}
	args := append([]string{"clone", "--branch", "main"}, flags...)
	boardGit(t, filepath.Dir(root), append(args, remote, root)...)
	boardGit(t, root, "update-ref", "ORIG_HEAD", "HEAD")
	oid := strings.TrimSpace(boardGit(t, source, "rev-parse", "HEAD"))
	require.Equal(t, oid, strings.TrimSpace(boardGit(t, root, "merge-base", "--all", "ORIG_HEAD", "@{upstream}")))
	require.Equal(t, string(blob), boardGit(t, root, "show", oid+":.mtix/tasks.json"))
	return root, source, blob, oid
}

func TestMergeBaseBoard_RestrictedCloneLayouts_Refused(t *testing.T) {
	for _, c := range []struct {
		name, marker string
	}{
		{"shallow", "shallow"},
		{"borrowed", "objects/info/alternates"},
	} {
		t.Run(c.name, func(t *testing.T) {
			root, source, _, _ := boardReadableClone(t, c.name)
			marker, err := os.ReadFile(filepath.Join(root, ".git", filepath.FromSlash(c.marker)))
			require.NoError(t, err)
			require.NotEmpty(t, marker)
			before := boardSnapshot(t, source)
			assertBoardUnavailable(t, NewSyncService(nil, nil, nil), root)
			require.Equal(t, before, boardSnapshot(t, source))
		})
	}
}

func TestMergeBaseBoard_SelfContainedCloneLayouts_ReturnExactProvenance(t *testing.T) {
	for _, layout := range []string{"full", "dissociated"} {
		t.Run(layout, func(t *testing.T) {
			root, source, blob, oid := boardReadableClone(t, layout)
			before, sourceBefore := boardSnapshot(t, root), boardSnapshot(t, source)
			data, p, err := NewSyncService(nil, nil, nil).MergeBaseBoard(t.Context(), root)
			require.NoError(t, err)
			require.Equal(t, "ancestor board", data.Nodes[0].Title)
			require.Equal(t, "PROJ-1", data.Nodes[0].ID)
			require.Equal(t, oid, p.BaseOID)
			require.Equal(t, [2]string{"ORIG_HEAD", "@{upstream}"}, p.Candidates)
			require.Equal(t, "git-merge-base", p.Source)
			require.Equal(t, "current", p.Form)
			require.Equal(t, fmt.Sprintf("%x", sha256.Sum256(blob)), p.BlobSHA256)
			require.Equal(t, "41589749e35326ce49c3e30ec4aa6cea672b73c5d640d5d7ee56563e2a2aadfb", p.ContentSHA256)
			require.Equal(t, before, boardSnapshot(t, root))
			require.Equal(t, sourceBefore, boardSnapshot(t, source))
		})
	}
}
