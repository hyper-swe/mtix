// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package metadata_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/relay/metadata"
	"github.com/stretchr/testify/require"
)

func TestRead_ValidatesEveryStampedPeerID(t *testing.T) {
	valid := peerB + "-" + strings.Repeat("_", 32)
	bad := valid + "x"
	dir := t.TempDir()
	for _, tc := range []struct {
		field  string
		mutate func(*metadata.Relay)
	}{
		{"", func(*metadata.Relay) {}},
		{"created_by", func(r *metadata.Relay) { r.CreatedBy = bad }},
		{"from_rs_by_peer", func(r *metadata.Relay) { r.KeyEpochs[1].FromRSByPeer[bad] = 2 }},
		{"retired_peers", func(r *metadata.Relay) { r.RetiredPeers = append(r.RetiredPeers, bad) }},
	} {
		t.Run("read_"+tc.field, func(t *testing.T) {
			doc, err := metadata.Init(initConfig())
			require.NoError(t, err)
			require.NoError(t, doc.AppendKeyEpoch(2, map[string]uint64{valid: 1}))
			doc.RetiredPeers = []string{valid}
			tc.mutate(doc)
			body, err := json.Marshal(doc)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(dir, metadata.FileName), body, 0600))
			got, err := metadata.Read(dir)
			if tc.field == "" {
				require.NoError(t, err)
				require.Equal(t, doc, got)
				return
			}
			require.ErrorIs(t, err, model.ErrInvalidInput)
			require.ErrorIs(t, err, metadata.ErrRelayCorrupt)
			require.ErrorContains(t, err, tc.field)
			require.ErrorContains(t, err, strconv.Quote(bad))
			require.Nil(t, got)
		})
	}
}

func TestRewrite_InvalidNewBoundaryPeer_LeavesRecordUsable(t *testing.T) {
	dir := t.TempDir()
	doc, err := metadata.Init(initConfig())
	require.NoError(t, err)
	require.NoError(t, metadata.Write(dir, doc))
	doc.KeyEpochs = append(doc.KeyEpochs, metadata.KeyEpochBoundary{
		Epoch: 2, FromRSByPeer: map[string]uint64{"bad": 1},
	})
	require.ErrorIs(t, metadata.Rewrite(dir, doc), model.ErrInvalidInput)
	got, err := metadata.Read(dir)
	require.NoError(t, err)
	require.Len(t, got.KeyEpochs, 1)
}
