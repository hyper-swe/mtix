// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/stretchr/testify/require"
)

// PG-free CLI tests for mtix sync push / pull. Integration tests live
// in cli_integration_test.go and skip when MTIX_PG_TEST_DSN is unset.

func TestSyncPushCmd_Construction(t *testing.T) {
	cmd := newSyncPushCmd()
	require.Equal(t, "push [DSN]", cmd.Use)
	require.NotEmpty(t, cmd.Long)
	require.NotNil(t, cmd.Flags().Lookup("insecure-tls"))
	require.NotNil(t, cmd.Flags().Lookup("force"))
}

func TestSyncPullCmd_Construction(t *testing.T) {
	cmd := newSyncPullCmd()
	require.Equal(t, "pull [DSN]", cmd.Use)
	require.NotEmpty(t, cmd.Long)
	require.NotNil(t, cmd.Flags().Lookup("insecure-tls"))
	require.NotNil(t, cmd.Flags().Lookup("limit"))
}

func TestSyncCmd_RegistersAllFR18BootstrapAndFlow(t *testing.T) {
	cmd := newSyncCmd()
	subs := map[string]bool{}
	for _, c := range cmd.Commands() {
		subs[c.Name()] = true
	}
	for _, name := range []string{"init", "clone", "push", "pull"} {
		require.Truef(t, subs[name], "%s subcommand registered", name)
	}
}

func TestRunSyncPush_RefusesOutsideMtixProject(t *testing.T) {
	saved := app.mtixDir
	app.mtixDir = ""
	t.Cleanup(func() { app.mtixDir = saved })

	var stdout, stderr bytes.Buffer
	err := runSyncPush(context.Background(), &stdout, &stderr,
		[]string{"postgres://user:pass@localhost/db"},
		transport.Options{InsecureTLS: true}, false)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not in an mtix project")
}

func TestRunSyncPull_RefusesOutsideMtixProject(t *testing.T) {
	saved := app.mtixDir
	app.mtixDir = ""
	t.Cleanup(func() { app.mtixDir = saved })

	var stdout, stderr bytes.Buffer
	err := runSyncPull(context.Background(), &stdout, &stderr,
		[]string{"postgres://user:pass@localhost/db"},
		transport.Options{InsecureTLS: true}, 1000)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not in an mtix project")
}

func TestPullDefaultBatchSize(t *testing.T) {
	require.Equal(t, 1000, pullDefaultBatchSize,
		"pull default matches clone default for symmetry")
}

func TestPushBatchSize(t *testing.T) {
	require.Equal(t, 100, pushBatchSize,
		"push batch size keeps each PG round-trip small")
}
