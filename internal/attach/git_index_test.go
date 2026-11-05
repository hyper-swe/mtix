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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const storeRel = ".mtix/data/mtix.db"

type idxEntry struct {
	path     string
	mode     uint32
	extended bool
}

// buildIndex encodes a git index (SHA-1 object names) for the given version,
// with raw extension blocks appended before the trailer.
func buildIndex(version uint32, entries []idxEntry, exts ...[]byte) []byte {
	var b bytes.Buffer
	b.WriteString("DIRC")
	_ = binary.Write(&b, binary.BigEndian, version)
	_ = binary.Write(&b, binary.BigEndian, uint32(len(entries)))
	prev := ""
	for _, e := range entries {
		start := b.Len()
		fixed := make([]byte, 62)
		mode := e.mode
		if mode == 0 {
			mode = 0o100644
		}
		binary.BigEndian.PutUint32(fixed[24:], mode)
		flags := uint16(len(e.path))
		if e.extended {
			flags |= 0x4000
		}
		binary.BigEndian.PutUint16(fixed[60:], flags)
		b.Write(fixed)
		if e.extended {
			b.Write([]byte{0, 0})
		}
		if version == 4 {
			common := 0
			for common < len(prev) && common < len(e.path) && prev[common] == e.path[common] {
				common++
			}
			b.WriteByte(byte(len(prev) - common)) // strip count, single-byte varint for the fixtures
			b.WriteString(e.path[common:])
			b.WriteByte(0)
			prev = e.path
			continue
		}
		b.WriteString(e.path)
		b.WriteByte(0)
		for (b.Len()-start)%8 != 0 {
			b.WriteByte(0)
		}
	}
	for _, x := range exts {
		b.Write(x)
	}
	b.Write(make([]byte, 20)) // trailer checksum; the reader does not rely on it
	return b.Bytes()
}

func ext(sig string, payload []byte) []byte {
	out := []byte(sig)
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], uint32(len(payload)))
	return append(append(out, n[:]...), payload...)
}

func writeIndex(t *testing.T, gitDir string, raw []byte) {
	t.Helper()
	require.NoError(t, os.MkdirAll(gitDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(gitDir, "index"), raw, 0o644))
}

func TestIndexReader_TableOfLayouts_DetectsTrackedStore(t *testing.T) {
	tracked := []idxEntry{{path: "README.md"}, {path: ".mtix/data/mtix.db"}, {path: "z.go"}}
	clean := []idxEntry{{path: "README.md"}, {path: ".mtix/tasks.json"}, {path: "z.go"}}
	tests := []struct {
		name    string
		version uint32
		entries []idxEntry
		want    bool
	}{
		{"v2 tracked", 2, tracked, true},
		{"v2 clean", 2, clean, false},
		{"v3 extended flag tracked", 3, []idxEntry{{path: ".mtix/data/mtix.db", extended: true}}, true},
		{"v4 tracked", 4, tracked, true},
		{"v4 clean", 4, clean, false},
		{"wal sibling counts as committed store", 2, []idxEntry{{path: ".mtix/data/mtix.db-wal"}}, true},
		{"similar prefix is not the store", 2, []idxEntry{{path: ".mtix/database/x"}, {path: "sub/.mtix/data/mtix.db"}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gitDir := filepath.Join(t.TempDir(), ".git")
			writeIndex(t, gitDir, buildIndex(tt.version, tt.entries))
			got, err := StoreTrackedInIndex(gitDir, storeRel)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestIndexReader_V4PathCompression_DetectsTrackedStore(t *testing.T) {
	// Names share long prefixes, so the store path exists only as a stripped suffix.
	entries := []idxEntry{{path: ".mtix/config.yaml"}, {path: ".mtix/data/mtix.db"}, {path: ".mtix/data/zz"}}
	gitDir := filepath.Join(t.TempDir(), ".git")
	writeIndex(t, gitDir, buildIndex(4, entries))
	got, err := StoreTrackedInIndex(gitDir, storeRel)
	require.NoError(t, err)
	assert.True(t, got)
}

func TestIndexReader_MissingIndex_NotTracked(t *testing.T) {
	got, err := StoreTrackedInIndex(filepath.Join(t.TempDir(), ".git"), storeRel)
	require.NoError(t, err)
	assert.False(t, got, "a repository with no index tracks nothing")
}

func TestIndexReader_UnknownExtension_FailsClosed(t *testing.T) {
	gitDir := filepath.Join(t.TempDir(), ".git")
	writeIndex(t, gitDir, buildIndex(2, []idxEntry{{path: "a"}}, ext("XYZW", []byte("payload"))))
	_, err := StoreTrackedInIndex(gitDir, storeRel)
	require.ErrorIs(t, err, ErrIndexUnsupported)
}

func TestIndexReader_KnownExtensions_AreSkipped(t *testing.T) {
	gitDir := filepath.Join(t.TempDir(), ".git")
	raw := buildIndex(2, []idxEntry{{path: ".mtix/data/mtix.db"}}, ext("TREE", []byte("0 0\n")), ext("UNTR", []byte{1, 2, 3}))
	writeIndex(t, gitDir, raw)
	got, err := StoreTrackedInIndex(gitDir, storeRel)
	require.NoError(t, err)
	assert.True(t, got)
}

func TestIndexReader_UnknownVersionAndCorruption_FailClosed(t *testing.T) {
	good := buildIndex(2, []idxEntry{{path: "a"}, {path: "b"}})
	tests := map[string][]byte{
		"version 9":        buildIndex(9, []idxEntry{{path: "a"}}),
		"bad signature":    append([]byte("NOPE"), good[4:]...),
		"truncated header": good[:6],
		"truncated entry":  good[:40],
		"count too large":  func() []byte { c := bytes.Clone(good); binary.BigEndian.PutUint32(c[8:], 99); return c }(),
		"trailing garbage": append(bytes.Clone(good[:len(good)-20]), append([]byte("junk"), make([]byte, 20)...)...),
		"v2 extended flag": buildIndex(2, []idxEntry{{path: "a", extended: true}}),
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

func TestIndexReader_OverSizeCap_FailsClosedWithoutReading(t *testing.T) {
	gitDir := filepath.Join(t.TempDir(), ".git")
	require.NoError(t, os.MkdirAll(gitDir, 0o755))
	f, err := os.Create(filepath.Join(gitDir, "index"))
	require.NoError(t, err)
	require.NoError(t, f.Truncate(MaxIndexBytes+1)) // sparse file: no real disk used
	require.NoError(t, f.Close())
	_, err = StoreTrackedInIndex(gitDir, storeRel)
	require.ErrorIs(t, err, ErrIndexUnsupported)
}

func TestIndexReader_SparseDirectoryCoveringStore_FailsClosed(t *testing.T) {
	sparse := func(dir string) []idxEntry { return []idxEntry{{path: dir, mode: 0o040000}} }
	tests := []struct {
		name    string
		entries []idxEntry
		wantErr bool
	}{
		{"covers .mtix/", sparse(".mtix/"), true},
		{"covers .mtix/data/", sparse(".mtix/data/"), true},
		{"unrelated sparse dir", sparse("docs/"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gitDir := filepath.Join(t.TempDir(), ".git")
			writeIndex(t, gitDir, buildIndex(3, tt.entries, ext("sdir", nil)))
			_, err := StoreTrackedInIndex(gitDir, storeRel)
			if tt.wantErr {
				require.ErrorIs(t, err, ErrIndexUnsupported)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestIndexReader_SplitIndex_ConsultsSharedIndex(t *testing.T) {
	gitDir := filepath.Join(t.TempDir(), ".git")
	base := bytes.Repeat([]byte{0xab}, 20)
	shared := buildIndex(2, []idxEntry{{path: ".mtix/data/mtix.db"}})
	require.NoError(t, os.MkdirAll(gitDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(gitDir, "sharedindex."+hex.EncodeToString(base)), shared, 0o644))
	// The split index itself holds no entries; the link extension names its base.
	writeIndex(t, gitDir, buildIndex(2, nil, ext("link", append(bytes.Clone(base), 0, 0, 0, 0, 0, 0, 0, 0))))

	got, err := StoreTrackedInIndex(gitDir, storeRel)
	require.NoError(t, err)
	assert.True(t, got, "the store tracked in the shared base is tracked")
}

func TestIndexReader_SplitIndex_MissingOrBadBase_FailsClosed(t *testing.T) {
	gitDir := filepath.Join(t.TempDir(), ".git")
	base := bytes.Repeat([]byte{0xcd}, 20)
	writeIndex(t, gitDir, buildIndex(2, nil, ext("link", append(bytes.Clone(base), 0, 0, 0, 0, 0, 0, 0, 0))))
	_, err := StoreTrackedInIndex(gitDir, storeRel)
	require.ErrorIs(t, err, ErrIndexUnsupported, "a split index whose base is missing cannot be judged")

	writeIndex(t, gitDir, buildIndex(2, nil, ext("link", []byte{1, 2, 3})))
	_, err = StoreTrackedInIndex(gitDir, storeRel)
	require.ErrorIs(t, err, ErrIndexUnsupported, "a short link payload is malformed")
}
