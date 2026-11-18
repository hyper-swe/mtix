// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

// marshalFixture is a one-node board with an injectable clock.
type marshalFixture struct {
	dir  string
	st   *sqlite.Store
	svc  *SyncService
	node *model.Node
	now  *time.Time
}

func newMarshalFixture(t *testing.T) *marshalFixture {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "data"), 0755))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := sqlite.New(filepath.Join(dir, "data", "mtix.db"), logger)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	st.SetClock(clock)
	node := &model.Node{ID: "TEST-1", Project: "TEST", Seq: 1, Title: "n", Status: model.StatusOpen,
		Priority: model.PriorityMedium, Weight: 1, NodeType: model.NodeTypeIssue, CreatedAt: now, UpdatedAt: now}
	node.ContentHash = node.ComputeHash()
	require.NoError(t, st.CreateNode(context.Background(), node))
	return &marshalFixture{dir: dir, st: st, svc: NewSyncService(st, logger, clock), node: node, now: &now}
}

func (f *marshalFixture) rename(t *testing.T) {
	t.Helper()
	f.node.Title = "changed"
	require.NoError(t, f.st.UpdateNode(context.Background(), f.node.ID, &store.NodeUpdate{Title: &f.node.Title}))
}

// The automatic export of an unchanged board must not serialize the whole
// board it will then discard: on a large board that doubled the time the
// exclusive sync lock was held (MTIX-130.20, FR-15.3).
func TestAutoExport_UnchangedBoard_DoesNotMarshalDiscardedBytes(t *testing.T) {
	f := newMarshalFixture(t)
	ctx := context.Background()
	require.NoError(t, f.svc.AutoExport(ctx, f.dir)) // first publication writes the file
	path := filepath.Join(f.dir, "tasks.json")
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	infoBefore, err := os.Stat(path)
	require.NoError(t, err)

	marshals := 0
	f.svc.marshalExport = func(data *sqlite.ExportData) ([]byte, error) {
		marshals++
		return json.MarshalIndent(data, "", "  ")
	}
	*f.now = f.now.Add(time.Hour) // elapsed time alone is not a board change
	require.NoError(t, f.svc.AutoExport(ctx, f.dir))

	after, err := os.ReadFile(path)
	require.NoError(t, err)
	infoAfter, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, before, after, "unchanged board must keep its bytes")
	require.Equal(t, infoBefore.ModTime(), infoAfter.ModTime(), "unchanged board must not rewrite the file")
	require.Zero(t, marshals, "unchanged automatic export must not serialize the board")
}

func TestAutoExport_ChangedBoard_MarshalsOnceAndPublishes(t *testing.T) {
	f := newMarshalFixture(t)
	ctx := context.Background()
	require.NoError(t, f.svc.AutoExport(ctx, f.dir))
	path := filepath.Join(f.dir, "tasks.json")
	before, err := os.ReadFile(path)
	require.NoError(t, err)

	var got []byte
	marshals := 0
	f.svc.marshalExport = func(data *sqlite.ExportData) ([]byte, error) {
		marshals++
		b, mErr := json.MarshalIndent(data, "", "  ")
		got = b
		return b, mErr
	}
	f.rename(t)
	require.NoError(t, f.svc.AutoExport(ctx, f.dir))

	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, 1, marshals, "a changed board is serialized exactly once")
	require.NotEqual(t, before, after)
	require.Equal(t, got, after, "published bytes are the indented marshal output")
	require.Contains(t, string(after), "\n  \"", "output keeps the two-space indent format")
}

func TestAutoExport_ChangedBoard_MarshalError_ReturnsWrapped(t *testing.T) {
	f := newMarshalFixture(t)
	ctx := context.Background()
	require.NoError(t, f.svc.AutoExport(ctx, f.dir))
	path := filepath.Join(f.dir, "tasks.json")
	before, err := os.ReadFile(path)
	require.NoError(t, err)

	boom := errors.New("boom")
	f.svc.marshalExport = func(*sqlite.ExportData) ([]byte, error) { return nil, boom }
	f.rename(t)
	err = f.svc.AutoExport(ctx, f.dir)

	require.ErrorIs(t, err, boom)
	require.ErrorContains(t, err, "marshal export data")
	after, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	require.Equal(t, before, after, "nothing is written when marshalling fails")
}

// With the marshal seam left nil, the published bytes are exactly the
// two-space-indented, unprefixed JSON of the exported board, so the default
// format cannot drift (MTIX-130.20, FR-15.3).
func TestAutoExport_DefaultMarshal_PublishesTwoSpaceIndentedExport(t *testing.T) {
	f := newMarshalFixture(t)
	ctx := context.Background()
	require.Nil(t, f.svc.marshalExport, "default seam must be nil")
	require.NoError(t, f.svc.AutoExport(ctx, f.dir))
	f.rename(t)
	require.NoError(t, f.svc.AutoExport(ctx, f.dir))

	published, err := os.ReadFile(filepath.Join(f.dir, "tasks.json"))
	require.NoError(t, err)
	exported, err := f.st.Export(ctx, "", "")
	require.NoError(t, err)
	want, err := json.MarshalIndent(exported, "", "  ")
	require.NoError(t, err)
	require.Equal(t, string(want), string(published))
}
