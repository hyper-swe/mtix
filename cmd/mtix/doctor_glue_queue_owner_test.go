// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/attach"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

const (
	boardIDOne = "0b9a4e0e-6f5d-4f4f-9c3c-1c2d3e4f5a6b"
	boardIDTwo = "7c1d2e3f-4a5b-4c6d-8e7f-0a1b2c3d4e5f"
)

// The key a registration writes on its queue rows is the committed board id
// when there is exactly one, else the store instance id; a legacy store, an
// ambiguous board id or an unreadable one owns no rows.
func TestOwnerKey_BoardIDInstanceAndErrors_PicksTheKeyOrNone(t *testing.T) {
	tests := []struct {
		name     string
		instance string
		ids      []string
		err      error
		want     string
	}{
		{"one board id wins over the instance id", "inst", []string{boardIDOne}, nil, boardIDOne},
		{"two board ids are ambiguous", "inst", []string{boardIDOne, boardIDTwo}, nil, ""},
		{"no board id file falls back to the instance id", "inst", nil, os.ErrNotExist, "inst"},
		{"a wrapped missing board id file falls back too", "inst", nil, errors.Join(os.ErrNotExist), "inst"},
		{"an unreadable board id owns no rows", "inst", nil, os.ErrPermission, ""},
		{"a legacy store with no instance id owns no rows", "", []string{boardIDOne}, nil, ""},
		{"a legacy store with no board id owns no rows", "", nil, os.ErrNotExist, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ownerKey(tt.instance, tt.ids, tt.err))
		})
	}
}

// With a real board-id file, the queue owner key is that id: the normal key a
// production registration writes (a fixture on the instance id alone misses it).
func TestQueueOwnerKey_RealBoardIDFile_IsTheCommittedBoardID(t *testing.T) {
	saveAndResetApp(t)
	root := t.TempDir()
	instance := initStoreAt(t, root)
	store, err := sqlite.New(filepath.Join(root, ".mtix", "data"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	app.store, app.mtixDir = store, filepath.Join(root, ".mtix")

	assert.Equal(t, instance, queueOwnerKey(t.Context()), "no board-id file: the instance id")
	require.NoError(t, os.WriteFile(filepath.Join(root, ".mtix", "board-id"), []byte(boardIDOne+"\n"), 0o600))
	assert.Equal(t, boardIDOne, queueOwnerKey(t.Context()))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".mtix", "board-id"), []byte(boardIDOne+"\n"+boardIDTwo+"\n"), 0o600))
	assert.Empty(t, queueOwnerKey(t.Context()))
}

// A row keyed by the committed board id is accepted through the whole doctor
// path, and one keyed by the instance id is not once a board id exists.
func TestDoctorGlueStrandedQueue_BoardIDKeyedRows_AreTheOnesAccepted(t *testing.T) {
	saveAndResetApp(t)
	root := t.TempDir()
	initStoreAt(t, root)
	require.NoError(t, os.WriteFile(filepath.Join(root, ".mtix", "board-id"), []byte(boardIDOne+"\n"), 0o600))
	good := laneRow(t, "keyed", "pi", root)
	good.BoardKey, good.Path = boardIDOne, root
	stale := ownedLaneRow(t, root, "instance", "pi", root)
	enqueueRow(t, root, good)
	enqueueRow(t, root, stale)
	row := strandedRowFor(t, root)

	assert.Contains(t, row.Fix, "mtix agent register 'keyed'")
	assert.NotContains(t, row.Fix, "'instance'")
	assert.Contains(t, row.Detail, "1 queued row(s) rejected")
}

// Doctor reads a queue only through the store it opened itself.
func TestProductionPendingRows_NoOpenedStoreOrDir_ReadsNothing(t *testing.T) {
	saveAndResetApp(t)
	root := t.TempDir()
	initStoreAt(t, root)
	enqueueRow(t, root, ownedLaneRow(t, root, "a", "pi", root))
	store, err := sqlite.New(filepath.Join(root, ".mtix", "data"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	d := attach.GlueDecision{StoreRoot: root}

	app.store, app.mtixDir = nil, filepath.Join(root, ".mtix")
	rows, err := productionPendingRows(t.Context(), d)
	require.NoError(t, err)
	assert.Empty(t, rows.Valid, "no opened store")
	app.store, app.mtixDir = store, ""
	rows, err = productionPendingRows(t.Context(), d)
	require.NoError(t, err)
	assert.Empty(t, rows.Valid, "no opened board directory")
	app.store, app.mtixDir = store, filepath.Join(root, ".mtix")
	rows, err = productionPendingRows(t.Context(), d)
	require.NoError(t, err)
	assert.Len(t, rows.Valid, 1)
}

// A queue that cannot be read produces no stranded row and no command.
func TestStrandedQueueCheck_PendingReadFails_RegistersNothing(t *testing.T) {
	deps := attachDoctorDeps{
		glue: func(context.Context) attach.GlueDecision {
			return attach.GlueDecision{Reason: attach.ReasonUnlisted, StoreRoot: "/work/store"}
		},
		pending: func(context.Context, attach.GlueDecision) (queuedRows, error) {
			return queuedRows{Valid: []sqlite.AttachPending{laneRow(t, "a", "pi", "/work/store")}, Rejected: 1}, errors.New("queue unreadable")
		},
	}
	_, ok := strandedQueueCheck(t.Context(), deps)
	assert.False(t, ok)
	deps.pending = func(context.Context, attach.GlueDecision) (queuedRows, error) {
		return queuedRows{Valid: []sqlite.AttachPending{laneRow(t, "a", "pi", "/work/store")}}, nil
	}
	check, ok := strandedQueueCheck(t.Context(), deps)
	require.True(t, ok)
	assert.Equal(t, "glue.stranded-queue", check.ID)
	assert.Equal(t, service.DoctorWarn, check.Run(t.Context()).Status)
}
