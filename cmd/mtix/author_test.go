// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// MTIX-24: resolveProcessAuthor resolves this process's identity —
// MTIX_AUTHOR_ID env > author_id config > "cli" — and rejects an
// explicitly-set-but-invalid value loudly.

func TestResolveProcessAuthor_EnvBeatsConfig(t *testing.T) {
	t.Setenv(sqlite.AuthorIDEnv, "agent-7")
	app, meta, err := resolveProcessAuthor("proj-default")
	require.NoError(t, err)
	assert.Equal(t, "agent-7", app, "env wins for the process identity")
	assert.Equal(t, "proj-default", meta, "config value is still the persisted default")
}

func TestResolveProcessAuthor_ConfigWhenNoEnv(t *testing.T) {
	t.Setenv(sqlite.AuthorIDEnv, "")
	app, meta, err := resolveProcessAuthor("proj-default")
	require.NoError(t, err)
	assert.Equal(t, "proj-default", app)
	assert.Equal(t, "proj-default", meta)
}

func TestResolveProcessAuthor_FallsBackToCli(t *testing.T) {
	t.Setenv(sqlite.AuthorIDEnv, "")
	app, meta, err := resolveProcessAuthor("")
	require.NoError(t, err)
	assert.Equal(t, "cli", app)
	assert.Empty(t, meta, "no config default → meta stays empty (emit falls back to env/cli)")
}

func TestResolveProcessAuthor_RejectsInvalidEnv(t *testing.T) {
	t.Setenv(sqlite.AuthorIDEnv, "Not Valid!")
	_, _, err := resolveProcessAuthor("")
	require.Error(t, err, "an explicitly-set invalid env must be rejected, not normalized")
}

func TestResolveProcessAuthor_RejectsInvalidConfig(t *testing.T) {
	t.Setenv(sqlite.AuthorIDEnv, "")
	_, _, err := resolveProcessAuthor("Bad Config")
	require.Error(t, err)
}
