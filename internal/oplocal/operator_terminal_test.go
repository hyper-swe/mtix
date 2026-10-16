// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package oplocal

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHarnessSessionMarkers_BroadPrefixes_NameOnlySortedAndUnique(t *testing.T) {
	values := []string{"CLAUDECODE=synthetic-private", "CLAUDE_CODE_FUTURE=synthetic-private", "CODEX_SANDBOX_FUTURE=synthetic-private",
		"CODEX_THREAD_ID=synthetic-private", "PI_FUTURE=synthetic-private", "PI_FUTURE=synthetic-private", "PI_EMPTY=", "CLAUDE_ENV_FILE=legacy-only", "UNRELATED=synthetic-private", "BROKEN"}
	names := HarnessSessionMarkers(values)
	assert.Equal(t, []string{"CLAUDECODE", "CLAUDE_CODE_FUTURE", "CLAUDE_ENV_FILE", "CODEX_SANDBOX_FUTURE", "CODEX_THREAD_ID", "PI_FUTURE"}, names)
	raw, err := json.Marshal(names)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "synthetic-private")
}

func TestRequireOperatorTTY_NilFileAndPipe_Refuses(t *testing.T) {
	read, write, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, read.Close()); require.NoError(t, write.Close()) })
	for _, file := range []*os.File{nil, read, write} {
		assert.False(t, OperatorTerminal(file))
		err := RequireOperatorTTY(file)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--yes")
	}
}
