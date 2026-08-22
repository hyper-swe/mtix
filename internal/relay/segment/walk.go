// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package segment

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// File is one segment file observed on the medium. Size is the size at
// listing time and is advisory only — the active segment grows under the
// reader, which is exactly why the reader validates content rather than
// trusting a length it was told.
type File struct {
	No   uint64
	Name string
	Path string
	Size int64
}

// PeerDir is one peer's directory under the relay's peers/ directory.
type PeerDir struct {
	PeerID string
	Path   string
}

// lstatDir verifies that dir is a real directory rather than a symlink
// to one. FR-21 §5.1 resolves every relay path with Lstat-based
// traversal, so no relay operation ever follows a link planted by other
// software sharing the medium.
func lstatDir(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("lstat %s: %w", dir, err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: %s", ErrSymlink, dir)
	}
	if !fi.IsDir() {
		return fmt.Errorf("%w: %s is not a directory", ErrForeignEntry, dir)
	}
	return nil
}

// readRelayDir lists dir with Lstat semantics and refuses any symlink
// found in it. os.ReadDir reports entry types from the directory read
// itself, without following links, which is the traversal FR-21 §5.1
// mandates.
func readRelayDir(dir string) ([]os.DirEntry, error) {
	if err := lstatDir(dir); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	for _, e := range entries {
		if e.Type()&os.ModeSymlink != 0 {
			// Hard refusal regardless of the entry's name: §5.1 admits
			// no symlink anywhere under the relay directory, and a
			// refusal is cheaper than reasoning about where it points.
			return nil, fmt.Errorf("%w: %s", ErrSymlink, filepath.Join(dir, e.Name()))
		}
	}
	return entries, nil
}

// ListSegments returns the segment files in a peer's segment directory,
// ordered by segment number, together with the names of entries that do
// not match the FR-21 §5.2 grammar.
//
// Foreign entries are reported, never parsed and never removed: the
// medium belongs to the operator and may legitimately hold other files.
// Two conditions are fatal instead of merely reported — a symlink
// anywhere (§5.1), and an entry that claims a segment name but is not a
// regular file. The second is the ADR-005 hazard made concrete: a FIFO
// or device node is not a file with contents but a kernel rendezvous
// point, and opening one under a segment name would block the reader
// indefinitely on a medium other software can write to.
func ListSegments(dir string) ([]File, []string, error) {
	entries, err := readRelayDir(dir)
	if err != nil {
		return nil, nil, err
	}

	var segs []File
	var foreign []string
	for _, e := range entries {
		no, nameErr := ParseFileName(e.Name())
		if nameErr != nil {
			foreign = append(foreign, e.Name())
			continue
		}
		if !e.Type().IsRegular() {
			return nil, nil, fmt.Errorf("%w: %s is not a regular file",
				ErrForeignEntry, filepath.Join(dir, e.Name()))
		}
		info, infoErr := e.Info()
		if infoErr != nil {
			return nil, nil, fmt.Errorf("stat %s: %w", filepath.Join(dir, e.Name()), infoErr)
		}
		segs = append(segs, File{
			No:   no,
			Name: e.Name(),
			Path: filepath.Join(dir, e.Name()),
			Size: info.Size(),
		})
	}
	sort.Slice(segs, func(i, j int) bool { return segs[i].No < segs[j].No })
	return segs, foreign, nil
}

// ListPeers returns the peer directories under a relay's peers/
// directory, ordered by peer id, plus the names of entries that do not
// match the §5.2 identity grammar.
//
// As in ListSegments, a name that conforms must be the right kind of
// object: an entry matching the peer grammar that is not a directory is
// fatal, while anything off-grammar is simply reported for doctor.
func ListPeers(dir string) ([]PeerDir, []string, error) {
	entries, err := readRelayDir(dir)
	if err != nil {
		return nil, nil, err
	}

	var peers []PeerDir
	var foreign []string
	for _, e := range entries {
		if ValidatePeerID(e.Name()) != nil {
			foreign = append(foreign, e.Name())
			continue
		}
		if !e.IsDir() {
			return nil, nil, fmt.Errorf("%w: %s is not a directory",
				ErrForeignEntry, filepath.Join(dir, e.Name()))
		}
		peers = append(peers, PeerDir{PeerID: e.Name(), Path: filepath.Join(dir, e.Name())})
	}
	sort.Slice(peers, func(i, j int) bool { return peers[i].PeerID < peers[j].PeerID })
	return peers, foreign, nil
}
