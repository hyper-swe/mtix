// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package attach

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func scanCause(t *testing.T, raw []byte) string {
	t.Helper()
	gitDir := filepath.Join(t.TempDir(), ".git")
	writeIndex(t, gitDir, raw)
	_, err := ScanStoreInIndex(gitDir, storeRel)
	var ie *IndexError
	require.ErrorAs(t, err, &ie)
	return ie.Cause
}

func TestIndexReader_VersionBoundaries_OnlyTwoToFourAreRead(t *testing.T) {
	for _, v := range []uint32{0, 1, 5, 6} {
		assert.Equal(t, CauseVersion, scanCause(t, buildIndex(v, []idxEntry{{path: "a"}})), "version %d", v)
	}
	for _, v := range []uint32{2, 3, 4} {
		gitDir := filepath.Join(t.TempDir(), ".git")
		writeIndex(t, gitDir, buildIndex(v, []idxEntry{{path: "a"}}))
		kind, err := ScanStoreInIndex(gitDir, storeRel)
		require.NoError(t, err, "version %d", v)
		assert.Equal(t, TrackedNone, kind)
	}
}

func TestIndexReader_NonNulEntryPadding_IsMalformed(t *testing.T) {
	raw := buildIndex(2, []idxEntry{{path: "ab"}}) // 62+3 bytes, seven bytes of padding
	require.Equal(t, byte(0), raw[indexHeaderLen+entryFixedLen+3])
	raw[indexHeaderLen+entryFixedLen+3] = 'x'
	assert.Equal(t, CauseMalformed, scanCause(t, raw))
}

func TestIndexReader_IndexFileIsASymlink_IsRefused(t *testing.T) {
	gitDir := filepath.Join(t.TempDir(), ".git")
	require.NoError(t, os.MkdirAll(gitDir, 0o755))
	target := filepath.Join(t.TempDir(), "target-index")
	require.NoError(t, os.WriteFile(target, buildIndex(2, []idxEntry{{path: "a"}}), 0o644))
	require.NoError(t, os.Symlink(target, filepath.Join(gitDir, "index")))
	_, err := ScanStoreInIndex(gitDir, storeRel)
	var ie *IndexError
	require.ErrorAs(t, err, &ie)
	assert.Equal(t, CauseMalformed, ie.Cause, "an index reached through a symlink is not read")
}

// linkIndex is an index whose only entry is entryPath and which links to the
// shared base named by base (a 20-byte object name).
func linkIndex(entryPath string, base []byte) []byte {
	return buildIndex(2, []idxEntry{{path: entryPath}}, ext("link", append(append([]byte{}, base...), 0, 0, 0, 0, 0, 0, 0, 0)))
}

func TestIndexReader_SplitIndexDeltaTracksStore_ReportsTrackedWithoutConsultingBase(t *testing.T) {
	gitDir := filepath.Join(t.TempDir(), ".git")
	base := bytes.Repeat([]byte{0xab}, 20)
	writeIndex(t, gitDir, linkIndex(".mtix/data/mtix.db", base))
	writeFile(t, filepath.Join(gitDir, "sharedindex."+"abababababababababababababababababababab"), string(buildIndex(2, []idxEntry{{path: "README"}})))
	kind, err := ScanStoreInIndex(gitDir, storeRel)
	require.NoError(t, err)
	assert.Equal(t, TrackedStore, kind, "a store tracked only in the delta must not read clean")
}

func TestIndexReader_SplitIndexBaseTracksStore_ReportsTracked(t *testing.T) {
	gitDir := filepath.Join(t.TempDir(), ".git")
	base := bytes.Repeat([]byte{0xcd}, 20)
	writeIndex(t, gitDir, linkIndex("README", base))
	writeFile(t, filepath.Join(gitDir, "sharedindex.cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd"), string(buildIndex(2, []idxEntry{{path: ".mtix/data/mtix.db"}})))
	kind, err := ScanStoreInIndex(gitDir, storeRel)
	require.NoError(t, err)
	assert.Equal(t, TrackedStore, kind)
}

// v4Index writes version 4 entries by hand: each is (strip, suffix).
func v4Index(entries ...struct {
	strip  byte
	suffix []byte
}) []byte {
	var b bytes.Buffer
	b.WriteString("DIRC")
	_ = binary.Write(&b, binary.BigEndian, uint32(4))
	_ = binary.Write(&b, binary.BigEndian, uint32(len(entries)))
	for _, e := range entries {
		b.Write(make([]byte, 62))
		b.WriteByte(e.strip)
		b.Write(e.suffix)
		b.WriteByte(0)
	}
	b.Write(make([]byte, 20))
	return b.Bytes()
}

type v4Entry = struct {
	strip  byte
	suffix []byte
}

func TestIndexReader_V4CumulativePathOverCap_FailsClosed(t *testing.T) {
	// Each suffix is under the cap, but the second name is the first plus 4000 more bytes.
	over := v4Index(v4Entry{0, bytes.Repeat([]byte("a"), 4000)}, v4Entry{0, bytes.Repeat([]byte("b"), 4000)})
	assert.Equal(t, CausePathLength, scanCause(t, over))

	within := v4Index(v4Entry{0, bytes.Repeat([]byte("a"), 4000)}, v4Entry{0, bytes.Repeat([]byte("b"), 90)})
	gitDir := filepath.Join(t.TempDir(), ".git")
	writeIndex(t, gitDir, within)
	_, err := ScanStoreInIndex(gitDir, storeRel)
	require.NoError(t, err, "a name exactly within the cap is read")
}

func TestIndexReader_SingleSuffixOverCap_HasItsOwnCause(t *testing.T) {
	assert.Equal(t, CausePathLength, scanCause(t, buildIndex(2, []idxEntry{{path: string(bytes.Repeat([]byte("p"), MaxPathBytes+1))}})))
}
