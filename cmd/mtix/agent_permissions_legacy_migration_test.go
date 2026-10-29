// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/hyper-swe/mtix/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const permissionLegacyFixtureSHA = "56d9aebeee98fece773a5663c5e9874c08f19f0501fa8fc13d5c8a13ac4f511c"

func permissionLegacyBoard(t *testing.T) (string, []byte) {
	t.Helper()
	testutil.OperatorState(t)
	t.Setenv("MTIX_SYNC_DSN", "")
	data, err := os.ReadFile(filepath.Join("testdata", "agent-permissions", "legacy-054-no-id.db"))
	require.NoError(t, err, "published-binary compatibility fixture must be available")
	sum := sha256.Sum256(data)
	require.Equal(t, permissionLegacyFixtureSHA, hex.EncodeToString(sum[:]))
	root := t.TempDir()
	dir := filepath.Join(root, ".mtix")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "data"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("prefix: PROOF\nsync:\n  auto_sync: false\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "data", "mtix.db"), data, 0o600))
	_, err = os.Stat(filepath.Join(dir, "board-id"))
	require.True(t, os.IsNotExist(err))
	return root, data
}

func permissionLegacyAssertRetainedNode(t *testing.T, body string) {
	t.Helper()
	var data struct {
		Nodes []struct {
			ID          string `json:"id"`
			Title       string `json:"title"`
			Annotations []struct {
				Author string `json:"author"`
				Text   string `json:"text"`
				To     string `json:"addressee"`
			} `json:"annotations"`
		} `json:"nodes"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &data))
	require.Len(t, data.Nodes, 1)
	assert.Equal(t, "PROOF-1", data.Nodes[0].ID)
	assert.Equal(t, "Compatibility fixture", data.Nodes[0].Title)
	require.Len(t, data.Nodes[0].Annotations, 1)
	line := data.Nodes[0].Annotations[0]
	assert.Equal(t, "wake permissions changed on a host (see that host's mtix doctor)", line.Text)
	assert.Equal(t, "mtix-permissions", line.Author)
	assert.Empty(t, line.To)
}

func TestAgentPermissions_Published054Migration_AllowAndDenyMintMissingBoardIdentity(t *testing.T) {
	for _, action := range []string{"allow", "deny"} {
		t.Run(action, func(t *testing.T) {
			root, original := permissionLegacyBoard(t)
			permissionDraftClearMarkers(t)
			permissionDraftRegister(t, root, "permission-fixture")
			_, err := os.Stat(filepath.Join(root, ".mtix", "board-id"))
			require.True(t, os.IsNotExist(err), "registration alone must not mint legacy identity")
			args := []string{"agent", action, "permission-fixture"}
			if action == "allow" {
				args = append(args, "--yes")
			}
			out := doctorCLIExecute(t, root, "", args...)
			require.Equal(t, 0, out.exit, out.stderr)
			identity := permissionDraftIdentity(t, root)
			rows := permissionDraftLog(t)
			require.Len(t, rows, 1)
			assert.Equal(t, action, rows[0]["action"])
			assert.Equal(t, identity, rows[0]["board_id"])
			assert.NotEqual(t, "skipped_no_node", rows[0]["board_line"])
			result := doctorCLIExecute(t, root, "", "export")
			require.Equal(t, 0, result.exit, result.stderr)
			permissionLegacyAssertRetainedNode(t, result.stdout)
			unchanged, err := os.ReadFile(filepath.Join("testdata", "agent-permissions", "legacy-054-no-id.db"))
			require.NoError(t, err)
			assert.Equal(t, original, unchanged, "migration must use a private copy of the published fixture")
		})
	}
}
