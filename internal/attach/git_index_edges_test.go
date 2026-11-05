// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package attach

import (
	"bytes"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGitVarint_Encodings_DecodeLikeGit(t *testing.T) {
	tests := []struct {
		name     string
		in       []byte
		want     uint64
		wantUsed int
		wantOK   bool
	}{
		{"single byte", []byte{0x05}, 5, 1, true},
		{"two bytes add one before the shift", []byte{0x80, 0x00}, 128, 2, true},
		{"two bytes with payload", []byte{0x81, 0x01}, 2*128 + 1, 2, true},
		{"empty", nil, 0, 0, false},
		{"truncated continuation", []byte{0x80}, 0, 0, false},
		{"over nine bytes", bytes.Repeat([]byte{0xff}, 12), 0, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, used, ok := gitVarint(tt.in)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantUsed, used)
		})
	}
}

func TestIndexReader_V4BadPathCompression_FailsClosed(t *testing.T) {
	// The first entry strips one byte from an empty previous path.
	raw := buildIndex(4, []idxEntry{{path: "a"}})
	raw[indexHeaderLen+entryFixedLen] = 1
	gitDir := filepath.Join(t.TempDir(), ".git")
	writeIndex(t, gitDir, raw)
	_, err := StoreTrackedInIndex(gitDir, storeRel)
	require.ErrorIs(t, err, ErrIndexUnsupported)
}

func TestIndexReader_IndexIsNotARegularFile_FailsClosed(t *testing.T) {
	gitDir := filepath.Join(t.TempDir(), ".git")
	require.NoError(t, os.MkdirAll(filepath.Join(gitDir, "index"), 0o755))
	_, err := StoreTrackedInIndex(gitDir, storeRel)
	require.ErrorIs(t, err, ErrIndexUnsupported)
}

func TestIndexReader_ExtensionOverrunAndUnterminatedPath_FailClosed(t *testing.T) {
	over := ext("TREE", []byte("x"))
	over[7] = 0xff // declared size far beyond the file
	noNul := buildIndex(2, []idxEntry{{path: "abcdefgh"}})
	noNul = append(noNul[:indexHeaderLen+entryFixedLen], append([]byte("zzzzzzzz"), make([]byte, 20)...)...)
	tests := map[string][]byte{
		"extension overruns": buildIndex(2, nil, over),
		"path never ends":    noNul,
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			gitDir := filepath.Join(t.TempDir(), ".git")
			writeIndex(t, gitDir, raw)
			_, err := StoreTrackedInIndex(gitDir, storeRel)
			require.ErrorIs(t, err, ErrIndexUnsupported)
		})
	}
}

func TestIndexReader_SplitIndexInLinkedWorktree_FindsBaseInCommonDir(t *testing.T) {
	mainRoot, wtRoot := linkedWorktree(t)
	admin := readGitdirTarget(t, wtRoot)
	base := bytes.Repeat([]byte{0x11}, 20)
	require.NoError(t, os.WriteFile(filepath.Join(mainRoot, ".git", "sharedindex."+hex.EncodeToString(base)),
		buildIndex(2, []idxEntry{{path: ".mtix/data/mtix.db"}}), 0o644))
	writeIndex(t, admin, buildIndex(2, nil, ext("link", append(bytes.Clone(base), make([]byte, 8)...))))
	got, err := StoreTrackedInIndex(admin, storeRel)
	require.NoError(t, err)
	assert.True(t, got)
}

func TestIndexReader_SplitIndexWithAllZeroBase_NoSharedLookup(t *testing.T) {
	gitDir := filepath.Join(t.TempDir(), ".git")
	writeIndex(t, gitDir, buildIndex(2, []idxEntry{{path: "a"}}, ext("link", make([]byte, 28))))
	got, err := StoreTrackedInIndex(gitDir, storeRel)
	require.NoError(t, err)
	assert.False(t, got)
}

func TestDetectCheckout_UnreadableAndRelativeGitFile_Handled(t *testing.T) {
	// A relative gitdir target is resolved against the checkout root.
	base := t.TempDir()
	mainRoot := filepath.Join(base, "main")
	wt := filepath.Join(base, "wt")
	admin := filepath.Join(mainRoot, ".git", "worktrees", "wt")
	require.NoError(t, os.MkdirAll(admin, 0o755))
	writeFile(t, filepath.Join(wt, ".git"), "gitdir: ../main/.git/worktrees/wt\n")
	writeFile(t, filepath.Join(admin, "commondir"), "../..\n")
	writeFile(t, filepath.Join(admin, "gitdir"), filepath.Join(wt, ".git")+"\n")
	got, err := DetectCheckout(wt)
	require.NoError(t, err)
	assert.Equal(t, CheckoutWorktree, got.Kind)

	// A bare common dir has no main checkout to attach to.
	bare := filepath.Join(base, "bare.git")
	bareAdmin := filepath.Join(bare, "worktrees", "w")
	w2 := filepath.Join(base, "w2")
	require.NoError(t, os.MkdirAll(bareAdmin, 0o755))
	writeFile(t, filepath.Join(w2, ".git"), "gitdir: "+bareAdmin+"\n")
	writeFile(t, filepath.Join(bareAdmin, "commondir"), "../..\n")
	writeFile(t, filepath.Join(bareAdmin, "gitdir"), filepath.Join(w2, ".git")+"\n")
	got, err = DetectCheckout(w2)
	require.NoError(t, err)
	assert.Equal(t, CheckoutNone, got.Kind)
}
