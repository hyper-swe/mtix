// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package attach

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

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
	assert.Equal(t, CheckoutInvalid, got.Kind)
}

func TestScanStoreInIndex_TrackedSymlinkOverStorePath_ReportsAncestor(t *testing.T) {
	tests := []struct {
		name    string
		entries []idxEntry
		want    string
	}{
		{"symlink at .mtix", []idxEntry{{path: ".mtix", mode: 0o120000}}, TrackedAncestor},
		{"symlink at .mtix/data", []idxEntry{{path: ".mtix/data", mode: 0o120000}}, TrackedAncestor},
		{"gitlink at .mtix", []idxEntry{{path: ".mtix", mode: 0o160000}}, TrackedAncestor},
		{"store file", []idxEntry{{path: ".mtix/data/mtix.db"}}, TrackedStore},
		{"only tasks.json", []idxEntry{{path: ".mtix/tasks.json"}}, TrackedNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gitDir := filepath.Join(t.TempDir(), ".git")
			writeIndex(t, gitDir, buildIndex(2, tt.entries))
			got, err := ScanStoreInIndex(gitDir, storeRel)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestScanStoreInIndex_SparseDirInsideStoreDir_FailsClosedWithCause(t *testing.T) {
	gitDir := filepath.Join(t.TempDir(), ".git")
	writeIndex(t, gitDir, buildIndex(3, []idxEntry{{path: ".mtix/data/sub/", mode: 0o040000}}, ext("sdir", nil)))
	_, err := ScanStoreInIndex(gitDir, storeRel)
	var ie *IndexError
	require.ErrorAs(t, err, &ie)
	assert.Equal(t, CauseSparse, ie.Cause)
	require.ErrorIs(t, err, ErrIndexUnsupported)
}

func TestScanStoreInIndex_Causes_AreTyped(t *testing.T) {
	tests := []struct {
		name  string
		raw   []byte
		cause string
	}{
		{"unknown extension", buildIndex(2, nil, ext("XYZW", nil)), CauseExtension},
		{"unknown version", buildIndex(9, nil), CauseVersion},
		{"malformed", []byte("not an index at all, just text........"), CauseMalformed},
		{"split base missing", buildIndex(2, nil, ext("link", append(bytes.Repeat([]byte{7}, 20), make([]byte, 8)...))), CauseSplitBase},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gitDir := filepath.Join(t.TempDir(), ".git")
			writeIndex(t, gitDir, tt.raw)
			_, err := ScanStoreInIndex(gitDir, storeRel)
			var ie *IndexError
			require.ErrorAs(t, err, &ie)
			assert.Equal(t, tt.cause, ie.Cause)
		})
	}
}

// A crafted v4 index: one huge name, then many entries that each strip one byte
// and add one. Rebuilding every name copied the whole previous path, which ran
// for minutes inside the size cap; the path cap fails it closed at once.
func TestIndexReader_V4HugePathQuadratic_FailsClosedWithinDeadline(t *testing.T) {
	var b bytes.Buffer
	b.WriteString("DIRC")
	_ = binary.Write(&b, binary.BigEndian, uint32(4))
	const n = 200000
	_ = binary.Write(&b, binary.BigEndian, uint32(n+1))
	writeEntry := func(strip byte, name []byte) {
		b.Write(make([]byte, 62))
		b.WriteByte(strip)
		b.Write(name)
		b.WriteByte(0)
	}
	writeEntry(0, bytes.Repeat([]byte("a"), 8<<20))
	for i := 0; i < n; i++ {
		writeEntry(1, []byte("b"))
	}
	b.Write(make([]byte, 20))
	gitDir := filepath.Join(t.TempDir(), ".git")
	writeIndex(t, gitDir, b.Bytes())

	done := make(chan error, 1)
	go func() { _, err := ScanStoreInIndex(gitDir, storeRel); done <- err }()
	select {
	case err := <-done:
		require.ErrorIs(t, err, ErrIndexUnsupported)
	case <-time.After(10 * time.Second):
		t.Fatal("the crafted v4 index kept the reader busy for over 10 seconds")
	}
}
