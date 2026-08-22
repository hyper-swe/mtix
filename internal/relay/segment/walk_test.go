// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package segment_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hyper-swe/mtix/internal/relay/segment"
	"github.com/stretchr/testify/require"
)

// writeSeg drops a placeholder segment file. The walker never parses
// content, so bytes are irrelevant here.
func writeSeg(t *testing.T, dir string, no uint64) string {
	t.Helper()
	p := filepath.Join(dir, segment.FileName(no))
	require.NoError(t, os.WriteFile(p, []byte("placeholder"), 0o600))
	return p
}

// TestListSegments_OrdersByNumberNotByDirectoryOrder is FR-21 §5.2:
// naming carries ordering and no manifest is load-bearing, so the
// walker must sort numerically. A directory listing arriving in any
// order — which FR-21 §11.4 explicitly simulates — must not reorder a
// peer's stream.
func TestListSegments_OrdersByNumberNotByDirectoryOrder(t *testing.T) {
	dir := t.TempDir()
	for _, no := range []uint64{3, 1, 12, 2, 100000000} {
		writeSeg(t, dir, no)
	}
	segs, foreign, err := segment.ListSegments(dir)
	require.NoError(t, err)
	require.Empty(t, foreign)

	var got []uint64
	for _, s := range segs {
		got = append(got, s.No)
	}
	require.Equal(t, []uint64{1, 2, 3, 12, 100000000}, got)
	require.Equal(t, filepath.Join(dir, "seg-00000001.mrseg"), segs[0].Path)
	require.Equal(t, int64(len("placeholder")), segs[0].Size)
}

// TestListSegments_EmptyAndMissingDirectories separates "nothing
// published yet" from "the medium is gone".
func TestListSegments_EmptyAndMissingDirectories(t *testing.T) {
	t.Run("empty directory", func(t *testing.T) {
		segs, foreign, err := segment.ListSegments(t.TempDir())
		require.NoError(t, err)
		require.Empty(t, segs)
		require.Empty(t, foreign)
	})
	t.Run("missing directory", func(t *testing.T) {
		_, _, err := segment.ListSegments(filepath.Join(t.TempDir(), "absent"))
		require.Error(t, err)
		require.NotErrorIs(t, err, segment.ErrSymlink)
	})
	t.Run("a regular file where a directory belongs", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "notadir")
		require.NoError(t, os.WriteFile(p, []byte("x"), 0o600))
		_, _, err := segment.ListSegments(p)
		require.ErrorIs(t, err, segment.ErrForeignEntry)
	})
	t.Run("an unreadable directory reports the medium error", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("permission bits do not constrain root")
		}
		dir := t.TempDir()
		require.NoError(t, os.Chmod(dir, 0o000))
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		_, _, err := segment.ListSegments(dir)
		require.Error(t, err)
		require.NotErrorIs(t, err, segment.ErrSymlink)
		require.NotErrorIs(t, err, segment.ErrSegmentCorrupt)
	})
}

// TestListSegments_ForeignEntriesAreIgnoredAndFlagged is the §5.2 rule:
// anything that is not a segment name is reported for doctor and never
// parsed — and never deleted, because the relay medium belongs to the
// operator, not to mtix.
func TestListSegments_ForeignEntriesAreIgnoredAndFlagged(t *testing.T) {
	dir := t.TempDir()
	writeSeg(t, dir, 1)
	for _, name := range []string{
		"acks.json",
		"seg-1.mrseg",
		"seg-00000002.mrseg.tmp",
		".DS_Store",
		"seg-0000000x.mrseg",
		"README",
	} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600))
	}
	require.NoError(t, os.Mkdir(filepath.Join(dir, "subdir"), 0o700))

	segs, foreign, err := segment.ListSegments(dir)
	require.NoError(t, err)
	require.Len(t, segs, 1)
	require.Equal(t, uint64(1), segs[0].No)
	require.ElementsMatch(t, []string{
		"acks.json", "seg-1.mrseg", "seg-00000002.mrseg.tmp",
		".DS_Store", "seg-0000000x.mrseg", "README", "subdir",
	}, foreign)
}

// TestListSegments_RefusesSymlinks is FR-21 §5.1 (the CWE-59 lesson
// applied preemptively): a symlink anywhere under the relay directory
// is a hard refusal, not a followed path. The medium is writable by
// software other than mtix, so a planted link is a realistic input
// rather than a hypothetical one.
func TestListSegments_RefusesSymlinks(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, dir string)
	}{
		{"symlink named as a segment", func(t *testing.T, dir string) {
			target := filepath.Join(t.TempDir(), "elsewhere.mrseg")
			require.NoError(t, os.WriteFile(target, []byte("x"), 0o600))
			require.NoError(t, os.Symlink(target, filepath.Join(dir, segment.FileName(2))))
		}},
		{"dangling symlink named as a segment", func(t *testing.T, dir string) {
			require.NoError(t, os.Symlink(filepath.Join(dir, "absent"), filepath.Join(dir, segment.FileName(2))))
		}},
		{"symlink with a foreign name", func(t *testing.T, dir string) {
			require.NoError(t, os.Symlink("/etc/passwd", filepath.Join(dir, "notes.txt")))
		}},
		{"symlinked subdirectory", func(t *testing.T, dir string) {
			require.NoError(t, os.Symlink(t.TempDir(), filepath.Join(dir, "nested")))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeSeg(t, dir, 1)
			tt.setup(t, dir)
			_, _, err := segment.ListSegments(dir)
			require.ErrorIs(t, err, segment.ErrSymlink)
			require.Equal(t, "RELAY_SYMLINK", segment.CodeOf(err))
		})
	}
}

// TestListSegments_RefusesASymlinkedDirectoryItself closes the case
// where the peer directory handed in is itself a link.
func TestListSegments_RefusesASymlinkedDirectoryItself(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	require.NoError(t, os.Mkdir(target, 0o700))
	link := filepath.Join(root, "link")
	require.NoError(t, os.Symlink(target, link))

	_, _, err := segment.ListSegments(link)
	require.ErrorIs(t, err, segment.ErrSymlink)
}

// TestListPeers_GrammarAndOrdering enforces the §5.2 peer id grammar at
// the directory boundary and returns a stable order.
func TestListPeers_GrammarAndOrdering(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{
		"fedcba9876543210",
		"0123456789abcdef",
		"0123456789abcdef-courier",
	} {
		require.NoError(t, os.Mkdir(filepath.Join(dir, name), 0o700))
	}
	// Off-grammar directory names. Case variants of a valid id are
	// rejected by ValidatePeerID (see the naming tests) but cannot be
	// created alongside it here: a case-insensitive medium collides
	// them, and the relay is expected to live on exactly such media.
	for _, name := range []string{"bootstrap", "NOTAPEER", "0123456789abcde"} {
		require.NoError(t, os.Mkdir(filepath.Join(dir, name), 0o700))
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "relay.json"), []byte("{}"), 0o600))

	peers, foreign, err := segment.ListPeers(dir)
	require.NoError(t, err)
	require.Equal(t, []string{"0123456789abcdef", "0123456789abcdef-courier", "fedcba9876543210"},
		[]string{peers[0].PeerID, peers[1].PeerID, peers[2].PeerID})
	require.Equal(t, filepath.Join(dir, "0123456789abcdef"), peers[0].Path)
	require.ElementsMatch(t,
		[]string{"bootstrap", "NOTAPEER", "0123456789abcde", "relay.json"},
		foreign)
}

// TestListPeers_RefusesSymlinks applies the §5.1 refusal one level up:
// a peer directory that is a link would let a foreign writer redirect a
// whole peer's stream.
func TestListPeers_RefusesSymlinks(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, "0123456789abcdef"), 0o700))
	require.NoError(t, os.Symlink(t.TempDir(), filepath.Join(dir, "fedcba9876543210")))

	_, _, err := segment.ListPeers(dir)
	require.ErrorIs(t, err, segment.ErrSymlink)
}

// TestListPeers_MissingDirectory reports the medium being absent
// distinctly from a refusal.
func TestListPeers_MissingDirectory(t *testing.T) {
	_, _, err := segment.ListPeers(filepath.Join(t.TempDir(), "absent"))
	require.Error(t, err)
	require.NotErrorIs(t, err, segment.ErrSymlink)
}
