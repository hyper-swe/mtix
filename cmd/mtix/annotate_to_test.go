// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// MTIX-112.31: `mtix annotate <id> <text> --to <agent>` addresses the
// annotation through the same service path as `mtix comment --to` (CLI/MCP
// parity with mtix_annotate's `to`), and annotate without --to is unchanged.
package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// sendAnnotate runs the annotate command and returns its stdout.
func sendAnnotate(t *testing.T, args ...string) string {
	t.Helper()
	var err error
	out := captureStdout(t, func() {
		_, err = executeCmd(newAnnotateCmd(), args...)
	})
	require.NoError(t, err)
	return out
}

func TestAnnotateCmd_To_DeliversToRecipientInbox(t *testing.T) {
	t.Setenv(sqlite.AuthorIDEnv, "planner")
	initTestApp(t)
	id := seedOneNode(t)

	out := sendAnnotate(t, id, "please review the plan", "--to", "developer")

	got, err := app.store.InboxList(context.Background(), "developer")
	require.NoError(t, err)
	require.Len(t, got, 1, "the addressee's inbox holds the annotation")
	assert.Equal(t, "planner", got[0].Author, "sender is the process identity")
	assert.Equal(t, id, got[0].NodeID)
	assert.Contains(t, got[0].Body, "please review the plan")
	assert.Contains(t, out, "developer", "output names the recipient like comment --to")
}

func TestAnnotateCmd_To_MatchesCommentToDelivery(t *testing.T) {
	for _, to := range []string{"developer", "never-registered-agent", "a,b"} {
		t.Run(to, func(t *testing.T) {
			t.Setenv(sqlite.AuthorIDEnv, "planner")
			initTestApp(t)
			id := seedOneNode(t)

			captureStdout(t, func() {
				_, err := executeCmd(newCommentCmd(), id, "same text", "--to", to)
				require.NoError(t, err)
			})
			sendAnnotate(t, id, "same text", "--to", to)

			got, err := app.store.InboxList(context.Background(), to)
			require.NoError(t, err)
			require.Len(t, got, 2, "annotate --to and comment --to deliver identically, no refusal")
			assert.Equal(t, got[0].Author, got[1].Author)
			assert.Equal(t, got[0].NodeID, got[1].NodeID)
			assert.Equal(t, got[0].Body, got[1].Body)
		})
	}
}

func TestAnnotateCmd_EmptyTo_AddressesNobody(t *testing.T) {
	t.Setenv(sqlite.AuthorIDEnv, "planner")
	initTestApp(t)
	id := seedOneNode(t)

	out := sendAnnotate(t, id, "plain note", "--to", "")

	assert.Contains(t, out, "Added annotation to "+id)
	assert.NotContains(t, out, "inbox", "an empty --to is no addressee, as in comment")
	node, err := app.store.GetNode(context.Background(), id)
	require.NoError(t, err)
	require.Len(t, node.Annotations, 1)
	assert.Empty(t, node.Annotations[0].Addressee)
}

func TestAnnotateCmd_WithoutTo_Unchanged(t *testing.T) {
	t.Setenv(sqlite.AuthorIDEnv, "planner")
	initTestApp(t)
	id := seedOneNode(t)

	out := sendAnnotate(t, id, "plain note")

	assert.Equal(t, "Added annotation to "+id+"\n", out, "text output is exactly the historical line")
	node, err := app.store.GetNode(context.Background(), id)
	require.NoError(t, err)
	require.Len(t, node.Annotations, 1)
	assert.Empty(t, node.Annotations[0].Addressee)
	assert.Equal(t, "planner", node.Annotations[0].Author)
}

func TestAnnotateCmd_WithoutTo_JSONUnchanged(t *testing.T) {
	initTestApp(t)
	app.jsonOutput = true
	id := seedOneNode(t)

	out := sendAnnotate(t, id, "plain note")

	var got map[string]string
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	assert.Equal(t, map[string]string{"id": id, "status": "annotated"}, got)
}

func TestAnnotateCmd_WithoutTo_AtTokenStillAddresses(t *testing.T) {
	t.Setenv(sqlite.AuthorIDEnv, "planner")
	initTestApp(t)
	id := seedOneNode(t)

	sendAnnotate(t, id, "@developer look at this")

	got, err := app.store.InboxList(context.Background(), "developer")
	require.NoError(t, err)
	assert.Len(t, got, 1, "the existing @agent token path is untouched")
}

func TestAnnotateCmd_ToFlagDeclared_ExtraArgsRejected(t *testing.T) {
	initTestApp(t)
	cmd := newAnnotateCmd()
	flag := cmd.Flags().Lookup("to")
	require.NotNil(t, flag, "annotate declares --to")
	assert.Equal(t, "", flag.DefValue)

	id := seedOneNode(t)
	_, err := executeCmd(newAnnotateCmd(), id, "text", "extra", "--to", "developer")
	assert.Error(t, err, "annotate keeps exactly two positional arguments")
}

func TestAnnotateCmd_To_JSONNamesRecipient(t *testing.T) {
	t.Setenv(sqlite.AuthorIDEnv, "planner")
	initTestApp(t)
	app.jsonOutput = true
	id := seedOneNode(t)

	out := sendAnnotate(t, id, "ruling", "--to", "developer")

	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	assert.Equal(t, "developer", got["to"], "same JSON shape as comment --to")
	assert.Equal(t, id, got["id"])
}
