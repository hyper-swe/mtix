// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package attach

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// MaxIndexBytes caps a git index file (and each shared index) the reader will load.
const MaxIndexBytes = 32 << 20

// ErrIndexUnsupported means the index could not be judged: an unknown version
// or extension, a sparse directory entry covering the store, a split index
// whose base is missing, a malformed or oversize file. Callers fail closed.
var ErrIndexUnsupported = errors.New("git index unsupported")

const (
	indexHeaderLen = 12
	indexTrailer   = 20 // SHA-1 checksum; other object formats fail the structural checks
	entryFixedLen  = 62
	flagExtended   = 0x4000
	modeSparseDir  = 0o040000
)

// knownIndexExtensions are the extensions the reader understands well enough to
// skip. Any other signature fails closed.
var knownIndexExtensions = map[string]bool{
	"TREE": true, "REUC": true, "UNTR": true, "FSMN": true,
	"EOIE": true, "IEOT": true, "link": true, "sdir": true,
}

// indexScan is what one index file contributes to the question "is the store tracked".
type indexScan struct {
	tracked bool
	linkTo  string // hex object name of the shared base, empty when not split
}

// StoreTrackedInIndex reports whether the committed index under gitDir holds
// the store (storeRel, a slash path from the checkout root) or anything inside
// its directory. It is a bounded, read-only reader for index versions 2 to 4,
// including v4 path compression and split indexes (sharedindex.<sha>); it never
// runs git. A repository with no index tracks nothing. A split index is
// judged by the union of its entries and its base's, which can only over-report
// a tracked store, the safe direction (MTIX-112.19.8, ADR-007 A5).
func StoreTrackedInIndex(gitDir, storeRel string) (bool, error) {
	scan, err := scanIndexFile(filepath.Join(gitDir, "index"), storeRel, true)
	if err != nil || scan.tracked || scan.linkTo == "" {
		return scan.tracked, err
	}
	for _, dir := range sharedIndexDirs(gitDir) {
		base := filepath.Join(dir, "sharedindex."+scan.linkTo)
		if _, statErr := os.Lstat(base); statErr != nil {
			continue
		}
		shared, err := scanIndexFile(base, storeRel, false)
		return shared.tracked, err
	}
	return false, fmt.Errorf("split index base %s is missing: %w", scan.linkTo, ErrIndexUnsupported)
}

// sharedIndexDirs lists where a split index's base can live: the git dir, and
// for a linked worktree the common dir two levels up.
func sharedIndexDirs(gitDir string) []string {
	dirs := []string{gitDir}
	if filepath.Base(filepath.Dir(gitDir)) == "worktrees" {
		dirs = append(dirs, filepath.Dir(filepath.Dir(gitDir)))
	}
	return dirs
}

func unsupported(format string, args ...any) error {
	return fmt.Errorf("%s: %w", fmt.Sprintf(format, args...), ErrIndexUnsupported)
}

// scanIndexFile reads one index file within the size cap. allowMissing makes an
// absent file mean "nothing tracked".
func scanIndexFile(file, storeRel string, allowMissing bool) (indexScan, error) {
	info, err := os.Lstat(file)
	if errors.Is(err, os.ErrNotExist) && allowMissing {
		return indexScan{}, nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return indexScan{}, unsupported("index %s unreadable", filepath.Base(file))
	}
	if info.Size() > MaxIndexBytes {
		return indexScan{}, unsupported("index %s exceeds %d bytes", filepath.Base(file), MaxIndexBytes)
	}
	f, err := os.Open(file) // #nosec G304 -- fixed name under a git dir derived from a validated checkout
	if err != nil {
		return indexScan{}, unsupported("index %s unreadable", filepath.Base(file))
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, MaxIndexBytes+1))
	if err != nil || int64(len(raw)) > MaxIndexBytes {
		return indexScan{}, unsupported("index %s unreadable", filepath.Base(file))
	}
	return scanIndexBytes(raw, storeRel)
}

// scanIndexBytes parses header, entries and extensions, requiring the file to
// end exactly at the trailer.
func scanIndexBytes(raw []byte, storeRel string) (indexScan, error) {
	if len(raw) < indexHeaderLen+indexTrailer || string(raw[:4]) != "DIRC" {
		return indexScan{}, unsupported("not a git index")
	}
	version := binary.BigEndian.Uint32(raw[4:8])
	if version < 2 || version > 4 {
		return indexScan{}, unsupported("index version %d", version)
	}
	count := binary.BigEndian.Uint32(raw[8:12])
	body := raw[:len(raw)-indexTrailer]
	w := &entryWalker{buf: body, pos: indexHeaderLen, version: version, store: storeRel}
	if err := w.walk(count); err != nil {
		return indexScan{}, err
	}
	scan := indexScan{tracked: w.tracked}
	return scanExtensions(body, w.pos, scan)
}

// scanExtensions walks the extension blocks after the entries.
func scanExtensions(body []byte, pos int, scan indexScan) (indexScan, error) {
	for pos < len(body) {
		if len(body)-pos < 8 {
			return indexScan{}, unsupported("truncated extension header")
		}
		sig := string(body[pos : pos+4])
		size := int(binary.BigEndian.Uint32(body[pos+4 : pos+8]))
		pos += 8
		if size < 0 || size > len(body)-pos {
			return indexScan{}, unsupported("extension %q overruns the index", sig)
		}
		payload := body[pos : pos+size]
		pos += size
		if !knownIndexExtensions[sig] {
			return indexScan{}, unsupported("unknown extension %q", sig)
		}
		if sig == "link" {
			if len(payload) < 20 {
				return indexScan{}, unsupported("malformed link extension")
			}
			if base := hex.EncodeToString(payload[:20]); strings.Trim(base, "0") != "" {
				scan.linkTo = base
			}
		}
	}
	return scan, nil
}

// entryWalker decodes index entries, tracking the previous path for v4.
type entryWalker struct {
	buf     []byte
	pos     int
	version uint32
	store   string
	prev    string
	tracked bool
}

func (w *entryWalker) walk(count uint32) error {
	for i := uint32(0); i < count; i++ {
		if err := w.entry(); err != nil {
			return err
		}
	}
	return nil
}

// entry decodes one entry and applies the store tests to its path.
func (w *entryWalker) entry() error {
	start := w.pos
	if len(w.buf)-w.pos < entryFixedLen {
		return unsupported("truncated index entry")
	}
	mode := binary.BigEndian.Uint32(w.buf[w.pos+24:])
	flags := binary.BigEndian.Uint16(w.buf[w.pos+60:])
	w.pos += entryFixedLen
	if flags&flagExtended != 0 {
		if w.version < 3 || len(w.buf)-w.pos < 2 {
			return unsupported("extended flags in a version %d index", w.version)
		}
		w.pos += 2
	}
	name, err := w.name()
	if err != nil {
		return err
	}
	if w.version < 4 { // v2 and v3 pad each entry with NULs to a multiple of 8
		for (w.pos-start)%8 != 0 {
			if w.pos >= len(w.buf) || w.buf[w.pos] != 0 {
				return unsupported("bad entry padding")
			}
			w.pos++
		}
	}
	return w.judge(name, mode)
}

// name reads the entry path; v4 stores a varint count of bytes to strip from
// the previous path, then the suffix.
func (w *entryWalker) name() (string, error) {
	var strip uint64
	if w.version == 4 {
		n, used, ok := gitVarint(w.buf[w.pos:])
		if !ok || n > uint64(len(w.prev)) {
			return "", unsupported("bad path compression")
		}
		strip = n // bounded by len(w.prev) just above
		w.pos += used
	}
	end := w.pos
	for end < len(w.buf) && w.buf[end] != 0 {
		end++
	}
	if end >= len(w.buf) {
		return "", unsupported("unterminated entry path")
	}
	suffix := string(w.buf[w.pos:end])
	w.pos = end + 1
	name := suffix
	if w.version == 4 {
		name = w.prev[:uint64(len(w.prev))-strip] + suffix
		w.prev = name
	}
	return name, nil
}

// gitVarint decodes git's offset varint: the high bit continues, and each
// continuation adds one before shifting, which differs from LEB128.
func gitVarint(b []byte) (value uint64, used int, ok bool) {
	if len(b) == 0 {
		return 0, 0, false
	}
	c := b[0]
	value = uint64(c & 0x7f)
	used = 1
	for c&0x80 != 0 {
		if used >= len(b) || used >= 9 {
			return 0, 0, false
		}
		c = b[used]
		used++
		value = ((value + 1) << 7) | uint64(c&0x7f)
	}
	return value, used, true
}

// judge applies the store tests to one entry path.
func (w *entryWalker) judge(name string, mode uint32) error {
	dir := path.Dir(w.store) + "/"
	if mode&0o170000 == modeSparseDir && strings.HasSuffix(name, "/") {
		if strings.HasPrefix(w.store, name) {
			return unsupported("sparse directory %s covers the store", name)
		}
		return nil
	}
	if name == w.store || strings.HasPrefix(name, dir) {
		w.tracked = true
	}
	return nil
}
