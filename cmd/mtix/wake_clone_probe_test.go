// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func TestRunSyncClone_NonEmptyFreshJournal_RefusesBeforeHub(t *testing.T) {
	initTestApp(t)
	t.Setenv(sqlite.AuthorIDEnv, "sender")
	t.Setenv(transport.EnvDSN, "postgres://127.0.0.1:1/unused?sslmode=disable")
	_, err := app.nodeSvc.CreateNode(t.Context(), &service.CreateNodeRequest{Project: "TEST", Title: "existing", Creator: "sender"})
	require.NoError(t, err)
	var stdout, stderr bytes.Buffer
	err = runSyncClone(t.Context(), &stdout, &stderr, nil, cloudOpts, false, 2)
	require.ErrorContains(t, err, "local sync_events not empty")
	assert.Empty(t, stdout.String())
}

func TestRunSyncClone_UnreadableJournal_RefusesBeforeHub(t *testing.T) {
	for _, resume := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "resume"}[resume], func(t *testing.T) {
			initTestApp(t)
			t.Setenv(transport.EnvDSN, "postgres://127.0.0.1:1/unused?sslmode=disable")
			t.Setenv("MTIX_SYNC_HOOK", "")
			require.NoError(t, app.store.ReadDB().Close())
			var stdout, stderr bytes.Buffer
			err := runSyncClone(t.Context(), &stdout, &stderr, nil, cloudOpts, resume, 2)
			require.ErrorContains(t, err, "local probe")
			assert.ErrorContains(t, err, "database is closed")
			assert.Empty(t, stdout.String())
		})
	}
}

func TestInitCloneHookFloor_WriterFailure_WarnsOnlyForBootstrap(t *testing.T) {
	for _, bootstrap := range []bool{false, true} {
		t.Run(map[bool]string{false: "nonempty", true: "empty"}[bootstrap], func(t *testing.T) {
			initTestApp(t)
			require.NoError(t, app.store.WriteDB().Close())
			var stderr bytes.Buffer
			initCloneHookFloor(t.Context(), &stderr, bootstrap)
			if bootstrap {
				assert.Contains(t, stderr.String(), "mtix sync clone: hook floor init")
				assert.Contains(t, stderr.String(), "database is closed")
			} else {
				assert.Empty(t, stderr.String(), "a nonempty resume must not even attempt floor initialization")
			}
		})
	}
}
