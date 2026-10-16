// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func doctorMergeReadProjectDoc(t *testing.T, root, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, name))
	require.NoError(t, err)
	return data
}

func TestDoctorCLI_MergeProjectBriefing_DiscoversDoctorPreservesPointers(t *testing.T) {
	root := doctorCLIBoard(t, "")
	custom := map[string]string{
		"CLAUDE.md": "# Custom Claude\r\n@MTIX.md\noperator bytes\r\n",
		"AGENTS.md": "# Custom agents\r\nRead [MTIX.md](MTIX.md) for mtix instructions.\noperator bytes\r\n",
	}
	for name, value := range custom {
		require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte(value), 0o600))
	}
	out := doctorCLIExecute(t, root, "", "docs", "generate", "--force")
	require.Zero(t, out.exit, out.stderr)
	require.FileExists(t, filepath.Join(root, "MTIX.md"))
	briefing := doctorMergeReadProjectDoc(t, root, "MTIX.md")
	assert.Contains(t, string(briefing), "mtix doctor --json")
	assert.Contains(t, string(briefing), "mtix doctor --fix")
	assert.Contains(t, string(briefing), "MTIX-112.19.2")
	assert.Contains(t, string(briefing), "docs.stale")
	assert.Contains(t, string(briefing), "mtix -C <board-root> docs generate")
	for name, value := range custom {
		assert.Equal(t, []byte(value), doctorMergeReadProjectDoc(t, root, name))
	}
	out = doctorCLIExecute(t, root, "", "docs", "generate", "--force")
	require.Zero(t, out.exit, out.stderr)
	assert.Equal(t, briefing, doctorMergeReadProjectDoc(t, root, "MTIX.md"))
	for name, value := range custom {
		assert.Equal(t, []byte(value), doctorMergeReadProjectDoc(t, root, name))
	}
}
