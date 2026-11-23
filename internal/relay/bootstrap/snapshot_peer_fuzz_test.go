// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/relay/segment"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func FuzzReadSnapshotPeerIDs(f *testing.F) {
	const exporter = "0123456789abcdef"
	f.Add("NOT A PEER", false)
	f.Add("", true)
	f.Add(exporter+"-"+strings.Repeat("a", 32), true)
	f.Add(exporter+"-"+strings.Repeat("a", 33), false)
	f.Fuzz(func(t *testing.T, peer string, epoch bool) {
		snap := Snapshot{ExportedBy: exporter, Export: &sqlite.ExportData{}}
		if epoch {
			snap.PubEpochs = map[string]uint16{peer: 1}
		} else {
			snap.Positions = map[string]uint64{peer: 3}
		}
		body, err := json.Marshal(snap)
		require.NoError(t, err)
		path := filepath.Join(t.TempDir(), "snapshot.mrsnap")
		require.NoError(t, os.WriteFile(path, body, 0o600))
		_, err = readSnapshot(path)
		if segment.ValidatePeerID(peer) != nil {
			require.ErrorIs(t, err, model.ErrInvalidInput)
			return
		}
		require.NoError(t, err)
	})
}
