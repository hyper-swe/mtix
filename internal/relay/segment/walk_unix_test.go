// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package segment_test

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/hyper-swe/mtix/internal/relay/segment"
	"github.com/stretchr/testify/require"
)

// TestListSegments_RefusesKernelRendezvousObjects covers the hazard
// ADR-005 rules out by name. A FIFO planted under a segment name is not
// a file with contents but a rendezvous point serviced by a kernel:
// opening it blocks until a writer appears, so a reader that treated it
// as a segment would hang forever on a medium other software can write.
// The walker refuses it by type before anything opens it.
func TestListSegments_RefusesKernelRendezvousObjects(t *testing.T) {
	dir := t.TempDir()
	writeSeg(t, dir, 1)
	fifo := filepath.Join(dir, segment.FileName(2))
	require.NoError(t, syscall.Mkfifo(fifo, 0o600))

	_, _, err := segment.ListSegments(dir)
	require.ErrorIs(t, err, segment.ErrForeignEntry)
	require.Contains(t, err.Error(), "not a regular file")
}

// TestListSegments_IgnoresIrregularEntriesWithForeignNames keeps the
// refusal proportionate: an irregular entry that does not claim to be a
// segment is reported for doctor, not fatal. Operators mount all sorts
// of things next to a relay directory.
func TestListSegments_IgnoresIrregularEntriesWithForeignNames(t *testing.T) {
	dir := t.TempDir()
	writeSeg(t, dir, 1)
	require.NoError(t, syscall.Mkfifo(filepath.Join(dir, "some.pipe"), 0o600))

	segs, foreign, err := segment.ListSegments(dir)
	require.NoError(t, err)
	require.Len(t, segs, 1)
	require.Contains(t, foreign, "some.pipe")
}

// TestListPeers_RefusesIrregularPeerEntries applies the same rule to a
// peer directory slot.
func TestListPeers_RefusesIrregularPeerEntries(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, "0123456789abcdef"), 0o700))
	require.NoError(t, syscall.Mkfifo(filepath.Join(dir, "fedcba9876543210"), 0o600))

	_, _, err := segment.ListPeers(dir)
	require.ErrorIs(t, err, segment.ErrForeignEntry)
	require.Contains(t, err.Error(), "not a directory")
}
