// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package metadata_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/relay/metadata"
	"github.com/stretchr/testify/require"
)

// Vary the history inside a known-valid envelope so unrelated corruption
// cannot mask an epoch error. Keep the original failing medium record as a seed.
func FuzzRelayEpochHistory(f *testing.F) {
	f.Add([]byte(`{"key_epochs":[]}`))
	f.Add([]byte(`{"key_epochs":null}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		var input metadata.Relay
		if json.Unmarshal(data, &input) != nil {
			return
		}
		doc, err := metadata.Init(initConfig())
		require.NoError(t, err)
		doc.KeyEpochs = input.KeyEpochs
		body, err := json.Marshal(doc)
		require.NoError(t, err)
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, metadata.FileName), body, 0o600))
		got, err := metadata.Read(dir)
		for i := 1; i < len(input.KeyEpochs); i++ {
			if input.KeyEpochs[i].Epoch <= input.KeyEpochs[i-1].Epoch {
				require.Nil(t, got)
				require.ErrorIs(t, err, metadata.ErrRelayCorrupt)
				require.ErrorIs(t, err, model.ErrInvalidInput)
				require.ErrorContains(t, err, fmt.Sprintf("key_epochs[%d]", i))
				return
			}
		}
		require.NoError(t, err)
		var want metadata.Relay
		require.NoError(t, json.Unmarshal(body, &want))
		require.Equal(t, &want, got)
		out := t.TempDir()
		require.NoError(t, metadata.Write(out, got))
		again, err := metadata.Read(out)
		require.NoError(t, err)
		require.Equal(t, &want, again)
	})
}

func TestEpochHistory_InvalidSuffixRefusedBeforeWrite(t *testing.T) {
	for _, epoch := range []uint16{1, 3} {
		t.Run(fmt.Sprint(epoch), func(t *testing.T) {
			doc, err := metadata.Init(initConfig())
			require.NoError(t, err)
			dir := t.TempDir()
			path := filepath.Join(dir, metadata.FileName)
			require.NoError(t, metadata.Write(dir, doc))
			before, err := os.ReadFile(path)
			require.NoError(t, err)
			doc.KeyEpochs = append(doc.KeyEpochs, metadata.KeyEpochBoundary{Epoch: 3}, metadata.KeyEpochBoundary{Epoch: epoch})
			err = metadata.Rewrite(dir, doc)
			require.ErrorIs(t, err, model.ErrInvalidInput)
			require.ErrorContains(t, err, "key_epochs[2]")
			after, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, before, after)
		})
		t.Run(fmt.Sprintf("create-%d", epoch), func(t *testing.T) {
			doc, err := metadata.Init(initConfig())
			require.NoError(t, err)
			doc.KeyEpochs = append(doc.KeyEpochs, metadata.KeyEpochBoundary{Epoch: 3}, metadata.KeyEpochBoundary{Epoch: epoch})
			dir := t.TempDir()
			err = metadata.Write(dir, doc)
			require.ErrorIs(t, err, model.ErrInvalidInput)
			require.ErrorContains(t, err, "key_epochs[2]")
			require.NoFileExists(t, filepath.Join(dir, metadata.FileName))
		})
	}
}

func TestRewrite_RefusesReplacingPeerAtZeroSequence(t *testing.T) {
	doc, err := metadata.Init(initConfig())
	require.NoError(t, err)
	require.NoError(t, doc.AppendKeyEpoch(2, map[string]uint64{peerA: 0}))
	dir := t.TempDir()
	require.NoError(t, metadata.Write(dir, doc))
	doc.KeyEpochs[1].FromRSByPeer = map[string]uint64{peerB: 0}
	require.ErrorIs(t, metadata.Rewrite(dir, doc), metadata.ErrRelayImmutable)
}

func TestEpochHistory_RoundTripPreservesEmptyPeerPositions(t *testing.T) {
	dir := t.TempDir()
	body := []byte(`{"format_version":1,"relay_id":"relay","created_by":"0123456789abcdef","projects":[{"project_prefix":"MTIX","first_event_hash":"aaaa1111"}],"authenticated":true,"key_epochs":[{"epoch":1,"from_rs_by_peer":{}}]}`)
	require.NoError(t, os.WriteFile(filepath.Join(dir, metadata.FileName), body, 0o600))
	first, err := metadata.Read(dir)
	require.NoError(t, err)
	out := t.TempDir()
	require.NoError(t, metadata.Write(out, first))
	second, err := metadata.Read(out)
	require.NoError(t, err)
	require.Equal(t, first, second)
}
