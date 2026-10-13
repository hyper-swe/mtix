// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

// MTIX-95.31.15.1: the refusal paths of the base-ordered import.

import (
	"bytes"
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func quietService() *SyncService {
	return NewSyncService(nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
}

func olderBaseAndBoard(t *testing.T) (*sqlite.ImportBase, *sqlite.ExportData) {
	t.Helper()
	data, err := sqlite.DecodeExportData(bytes.NewReader(boardBytes(t, "ancestor board")))
	require.NoError(t, err)
	data.SchemaVersion = "1.0.0"
	require.NoError(t, sqlite.RecomputeExportChecksum(data))
	base, err := sqlite.ImportBaseFromBoard(data, baseSourceBoard)
	require.NoError(t, err)
	require.True(t, base.Older)
	return base, data
}

func TestResolveOnBase_NoOlderBase_DoesNotResolve(t *testing.T) {
	svc := quietService()
	_, board := olderBaseAndBoard(t)
	assert.Nil(t, svc.resolveOnBase(nil, &importCheck{local: board, incoming: board}))
	current := sqlite.NewImportBase("record", false, nil)
	assert.Nil(t, svc.resolveOnBase(current, &importCheck{local: board, incoming: board}))
}

func TestResolveOnBase_InvalidFile_DoesNotResolve(t *testing.T) {
	svc := quietService()
	base, board := olderBaseAndBoard(t)
	broken := *board
	broken.Checksum = "not the checksum"
	assert.Nil(t, svc.resolveOnBase(base, &importCheck{local: board, incoming: &broken}))
}

func TestResolveOnBase_ResolverError_DoesNotResolve(t *testing.T) {
	svc := quietService()
	base, board := olderBaseAndBoard(t)
	assert.Nil(t, svc.resolveOnBase(base, &importCheck{local: nil, incoming: board}), "a failed resolution refuses")
}

func TestResolveOnBase_SettledBoard_Resolves(t *testing.T) {
	svc := quietService()
	base, board := olderBaseAndBoard(t)
	res := svc.resolveOnBase(base, &importCheck{local: board, incoming: board})
	require.NotNil(t, res)
	assert.False(t, res.Diff.Lossy())
}

func TestBaseNote_NamesWhatIsMissingOrUsed(t *testing.T) {
	base, _ := olderBaseAndBoard(t)
	assert.Contains(t, baseNote(nil, nil, "no usable exchange record (missing); no git merge-base board (x)"),
		"no usable exchange record (missing)")
	assert.Contains(t, baseNote(base, &sqlite.BaseResolution{}, ""), baseSourceBoard)
	assert.Empty(t, baseNote(base, nil, ""), "a base in the current form adds nothing")
}

func TestNoteLine_RendersALineOrNothing(t *testing.T) {
	assert.Empty(t, noteLine(""))
	assert.Equal(t, "mtix: because of x\n", noteLine("; because of x"))
}
