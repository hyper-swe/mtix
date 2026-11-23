// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package bootstrap_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/relay/bootstrap"
)

// TestImportSnapshot_MalformedPeer_RefusesBeforeApplyingRows checks the FR-21
// §5.2 identity grammar at the §6.7 clone boundary (MTIX-287).
func TestImportSnapshot_MalformedPeer_RefusesBeforeApplyingRows(t *testing.T) {
	source := newStore(t)
	seedNodes(t, source, 1)
	req := exportReq(t.TempDir(), source)
	req.Epochs = map[string]uint16{peerB: 1}
	res, err := bootstrap.ExportSnapshot(t.Context(), req)
	require.NoError(t, err)
	body, err := os.ReadFile(res.Path)
	require.NoError(t, err)

	for _, tc := range []struct {
		name   string
		change func(*bootstrap.Snapshot)
	}{
		{"valid", nil},
		{"exported_by", func(s *bootstrap.Snapshot) { s.ExportedBy = "NOT A PEER" }},
		{"positions", func(s *bootstrap.Snapshot) { s.Positions["NOT A PEER"] = 3 }},
		{"pub_epochs", func(s *bootstrap.Snapshot) { s.PubEpochs["NOT A PEER"] = 1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var snapshot bootstrap.Snapshot
			require.NoError(t, json.Unmarshal(body, &snapshot))
			if tc.change != nil {
				tc.change(&snapshot)
			}
			data, err := json.Marshal(snapshot)
			require.NoError(t, err)
			path := filepath.Join(t.TempDir(), filepath.Base(res.Path))
			require.NoError(t, os.WriteFile(path, data, 0o600))

			joiner := newStore(t)
			got, err := bootstrap.ImportSnapshot(t.Context(), bootstrap.ImportRequest{Store: joiner, Path: path})
			if tc.change == nil {
				require.NoError(t, err)
				require.Equal(t, req.Positions, got.Positions)
				require.Equal(t, req.Epochs, got.Epochs)
				_, err = joiner.GetNode(t.Context(), "PROJ-1")
				require.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, model.ErrInvalidInput)
			assert.ErrorIs(t, err, bootstrap.ErrSnapshotUnusable)
			assert.ErrorContains(t, err, tc.name)
			assert.ErrorContains(t, err, "NOT A PEER")
			_, err = joiner.GetNode(t.Context(), "PROJ-1")
			assert.ErrorIs(t, err, model.ErrNotFound, "refusal must precede import")
		})
	}
}
