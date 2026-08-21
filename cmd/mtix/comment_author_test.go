// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// FR-21 §12.4 prerequisite regression (MTIX-24 gap): `mtix comment` hardcoded
// author "cli", so MTIX_AUTHOR_ID never applied to CLI comments — the
// addressee's inbox showed every sender as "cli", making agent comments
// unattributable and breaking the reply-routing contract the inbox prompt
// states ("Reply to a sender: mtix comment --to <sender>"). The command must
// use the process's resolved identity.
package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func seedOneNode(t *testing.T) string {
	t.Helper()
	node, err := app.nodeSvc.CreateNode(context.Background(),
		&service.CreateNodeRequest{Project: "TEST", Title: "T", Creator: "w"})
	require.NoError(t, err)
	return node.ID
}

func TestRunComment_UsesProcessAuthorIdentity(t *testing.T) {
	t.Setenv(sqlite.AuthorIDEnv, "planner")
	initTestApp(t) // resolves app.authorID from the env (MTIX-24 precedence)
	id := seedOneNode(t)

	require.NoError(t, runComment(id, "please implement the API", "developer"))

	got, err := app.store.InboxList(context.Background(), "developer")
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "planner", got[0].Author,
		"the addressee must see the sending agent's identity, not 'cli'")
}

func TestRunComment_DefaultsToCliWithoutIdentity(t *testing.T) {
	t.Setenv(sqlite.AuthorIDEnv, "")
	initTestApp(t)
	id := seedOneNode(t)

	require.NoError(t, runComment(id, "plain human comment", "developer"))

	got, err := app.store.InboxList(context.Background(), "developer")
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "cli", got[0].Author, "no identity configured → historical fallback")
}

func TestRunAnnotate_UsesProcessAuthorIdentity(t *testing.T) {
	t.Setenv(sqlite.AuthorIDEnv, "tester-2")
	initTestApp(t)
	id := seedOneNode(t)

	require.NoError(t, runAnnotate(id, "observed flake on retry"))

	node, err := app.store.GetNode(context.Background(), id)
	require.NoError(t, err)
	require.NotEmpty(t, node.Annotations)
	require.Equal(t, "tester-2", node.Annotations[len(node.Annotations)-1].Author,
		"annotate must carry the process identity too, not 'cli'")
}
