// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package relaygate_test

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/relay/lifecycle"
	"github.com/hyper-swe/mtix/internal/relay/metadata"
	"github.com/hyper-swe/mtix/internal/relay/segment"
	"github.com/hyper-swe/mtix/internal/relay/tick"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/testutil"
	"github.com/hyper-swe/mtix/internal/testutil/hostguard"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) { os.Exit(hostguard.Run(m)) }

const publisherID = "0123456789abcdef"

type prefixPeer struct {
	store *sqlite.Store
	stack *tick.Stack
}

func openPrefixPeer(t *testing.T, dir, id string, now func() time.Time) prefixPeer {
	t.Helper()
	st := testutil.NewTestStore(t).(*sqlite.Store)
	st.SetClock(now)
	stack, err := tick.Open(tick.StackConfig{Store: st, PeerID: id, RelayDir: dir,
		Now: now, Logger: slog.New(slog.DiscardHandler), MaxSegmentBytes: segment.HeaderSize + 1})
	require.NoError(t, err)
	return prefixPeer{st, stack}
}

func publishPrefixStream(t *testing.T) (prefixPeer, []segment.File) {
	t.Helper()
	now := testutil.FixedClock(time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC))
	dir := t.TempDir()
	_, err := lifecycle.Init(lifecycle.InitRequest{RelayDir: dir,
		RelayID: "01a0238b-d7d5-77cc-95c6-98a472ed7803", CreatedBy: publisherID,
		CreatedAt: now(), Projects: []metadata.Project{{Prefix: "TEST", FirstEventHash: "aaaa"}}})
	require.NoError(t, err)
	a := openPrefixPeer(t, dir, publisherID, now)
	for i := 1; i <= 2; i++ {
		id := fmt.Sprintf("TEST-%d", i)
		node := testutil.MakeNode(t, testutil.WithTitle(id))
		node.ID, node.Seq, node.Creator, node.NodeType = id, i, "planner", model.NodeTypeStory
		node.ContentHash = model.ComputeContentHash(id, "", "", "", nil)
		require.NoError(t, a.store.CreateNode(t.Context(), node))
	}
	result, err := a.stack.Pass(t.Context())
	require.NoError(t, err)
	require.Equal(t, 2, result.Published)
	files, _, err := segment.ListSegments(filepath.Join(a.stack.PeersDir, publisherID, tick.SegmentsDirName))
	require.NoError(t, err)
	require.Len(t, files, 2)
	return openPrefixPeer(t, dir, "fedcba9876543210", now), files
}

func checkStoredPrefix(t *testing.T, reader prefixPeer, count, cut int) {
	t.Helper()
	rows, err := reader.store.ReadRelayJournalSince(t.Context(), 0, 3)
	require.NoError(t, err)
	require.Len(t, rows, count, "byte prefix %d", cut)
	for i := 1; i <= 2; i++ {
		id := fmt.Sprintf("TEST-%d", i)
		node, err := reader.store.GetNode(t.Context(), id)
		if i > count {
			require.ErrorIs(t, err, model.ErrNotFound, "byte prefix %d", cut)
			continue
		}
		require.NoError(t, err)
		require.Equal(t, id, node.Title)
		require.Equal(t, id, rows[i-1].Event.NodeID)
	}
}

// Exercise every incomplete header through a real ingest tick (FR-21 §11.3).
func TestRelayGate_IncompleteHeader_SealedStallsActiveRetries(t *testing.T) {
	for _, mode := range []string{"sealed", "active"} {
		t.Run(mode, func(t *testing.T) {
			reader, files := publishPrefixStream(t)
			if mode == "active" {
				require.NoError(t, os.Remove(files[1].Path))
			}
			file := files[0]
			full, err := os.ReadFile(file.Path)
			require.NoError(t, err)
			for cut := 0; cut < segment.HeaderSize; cut++ {
				require.NoError(t, os.WriteFile(file.Path, full[:cut], 0o600))
				res, err := tick.Run(t.Context(), nil, reader.stack.Ingestor)
				require.NoError(t, err)
				checkStoredPrefix(t, reader, 0, cut)
				if mode == "sealed" {
					require.Len(t, res.Ingest.Stalls, 1)
					stall := res.Ingest.Stalls[0]
					require.Equal(t, publisherID, stall.PeerID)
					require.Equal(t, file.No, stall.Segment)
					assert.Equal(t, segment.CodeSegmentCorrupt, stall.Code, "sealed header at %d bytes", cut)
				} else {
					for _, stall := range res.Ingest.Stalls {
						require.Empty(t, stall.Code, "active header at %d bytes", cut)
					}
				}
			}
			require.NoError(t, os.WriteFile(file.Path, full, 0o600))
			res, err := tick.Run(t.Context(), nil, reader.stack.Ingestor)
			require.NoError(t, err)
			require.Empty(t, res.Ingest.Stalls)
			want := 1
			if mode == "sealed" {
				want = 2
			}
			checkStoredPrefix(t, reader, want, len(full))
		})
	}
}
