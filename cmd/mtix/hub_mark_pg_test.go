// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

// Successful connections mark the board (MTIX-112.17.3, ADR-007 A9). These
// run against the PG test hub (MTIX_PG_TEST_DSN) and skip without it.

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSyncPull_ConnectedThroughEnvDSN_MarksBoardAndMirror(t *testing.T) {
	hub := openCmdHub(t)
	initTestApp(t)
	state := identityOperatorState(t)
	pushToHub(t, hub, remoteCreates(t, 2)...)
	t.Setenv(transport.EnvDSN, os.Getenv(transport.EnvDSN))
	var out, diagnostic bytes.Buffer
	require.NoError(t, runSyncPull(context.Background(), &out, &diagnostic, nil, transport.Options{InsecureTLS: true}, 100), diagnostic.String())
	mark, err := app.store.HubMark(context.Background())
	require.NoError(t, err)
	assert.True(t, mark.Set)
	mirror, _, err := state.HubMarkMirror(filepath.Dir(app.mtixDir))
	require.NoError(t, err)
	assert.Equal(t, mark.Source, mirror.Source)
	assert.NotEqual(t, oplocal.HubMarkMirror{}, mirror)
}

func TestSyncPush_ConnectedThroughEnvDSN_MarksBoard(t *testing.T) {
	openCmdHub(t)
	initTestApp(t)
	var out, diagnostic bytes.Buffer
	require.NoError(t, runSyncPush(context.Background(), &out, &diagnostic, nil, transport.Options{InsecureTLS: true}, false), diagnostic.String())
	mark, err := app.store.HubMark(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "env_dsn", mark.Source)
}
