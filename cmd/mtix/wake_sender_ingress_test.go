// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	grpcapi "github.com/hyper-swe/mtix/internal/api/grpc"
	"github.com/hyper-swe/mtix/internal/mcp"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/testutil"
)

func TestWakeSenderIngress_UnrepresentableAuthorDoesNotBlockDispatch(t *testing.T) {
	for _, ingress := range []string{"mcp", "grpc", "sync"} {
		for _, author := range []struct{ name, value, want string }{
			{"tab", "bad\tsender", ""}, {"DEL", "bad\x7fsender", ""},
			{"C1", "bad\u0085sender", ""}, {"newline", "bad\nsender", ""},
			{"257 bytes", strings.Repeat("a", 257), ""}, {"empty", "", "ingress-envelope"},
		} {
			for _, limit := range []string{"", "2"} {
				t.Run(ingress+"/"+author.name+"/limit="+limit, func(t *testing.T) {
					t.Setenv(sqlite.AuthorIDEnv, "ingress-envelope")
					initTestApp(t)
					state := senderIngressState(t, limit)
					node := seedOneNode(t)
					node = writeSenderIngress(t, ingress, node, author.value)
					assertSenderIngressWake(t, state, author.want)
					require.NoError(t, runComment(node, "later valid event", "worker"))
					sent := 0
					d, keys := cliBackoffDispatcher(t, app.store, state, &sent)
					require.Len(t, keys, 2)
					require.NoError(t, d.Pass(t.Context(), keys))
					require.Equal(t, 1, sent, "the later event must still wake; the first is already delivered")
					stats, err := app.store.WakeLedgerStats(t.Context())
					require.NoError(t, err)
					require.Equal(t, int64(2), stats.Rows)
				})
			}
		}
	}
}

func senderIngressState(t *testing.T, limit string) *oplocal.State {
	t.Helper()
	state := testutil.OperatorState(t)
	t.Setenv("MTIX_WAKE_HOP", "0")
	threshold := "1000"
	require.NoError(t, state.UpdateWakeSetting(t.Context(), oplocal.WakeSettingUpdate{Key: "wake.backoff_after_unacked", Value: &threshold}, nil))
	if limit != "" {
		require.NoError(t, state.UpdateWakeSetting(t.Context(), oplocal.WakeSettingUpdate{Key: "wake.per_sender_per_hour", Value: &limit}, nil))
	}
	return state
}

func writeSenderIngress(t *testing.T, ingress, node, author string) string {
	t.Helper()
	switch ingress {
	case "mcp":
		reg := mcp.NewToolRegistry()
		mcp.RegisterContextTools(reg, nil, app.promptSvc)
		args, err := json.Marshal(map[string]string{"id": node, "text": "wake event", "author": author, "to": "worker"})
		require.NoError(t, err)
		result, err := reg.Call(t.Context(), "mtix_annotate", args)
		require.NoError(t, err)
		require.False(t, result.IsError)
	case "grpc":
		server := grpcapi.NewServer(grpcapi.Services{NodeSvc: app.nodeSvc, PromptSvc: app.promptSvc}, slog.Default(), grpcapi.ServerConfig{}, func() time.Time { return sweepHubT1 })
		t.Cleanup(server.Stop)
		require.NoError(t, server.HandleComment(t.Context(), node, "wake event", author, "worker"))
	case "sync":
		events := offlineEvents(t)
		comment := *events[1]
		comment.OpType = model.OpComment
		var err error
		comment.Payload, err = json.Marshal(model.CommentPayload{AuthorID: author, Body: "wake event", To: "worker"})
		require.NoError(t, err)
		comment.AuthorID = "ingress-envelope"
		batch := []*model.SyncEvent{events[0], &comment}
		hub := &fakeLateHub{events: batch, pullEvents: batch, hubNows: []time.Time{sweepHubT1}}
		var stderr bytes.Buffer
		got, err := pullThenSweep(t.Context(), testIngest(&stderr), hub, app.store, transport.PullCursor{}, 100)
		require.NoError(t, err, stderr.String())
		require.Equal(t, 2, got.pulled, stderr.String())
		require.Empty(t, quarantined(t))
		node = comment.NodeID
	default:
		t.Fatalf("unexpected ingress %s", ingress)
	}
	return node
}

func assertSenderIngressWake(t *testing.T, state *oplocal.State, want string) {
	t.Helper()
	sent := 0
	d, keys := cliBackoffDispatcher(t, app.store, state, &sent)
	require.Len(t, keys, 1)
	require.NoError(t, d.Pass(t.Context(), keys))
	require.Equal(t, 1, sent, "every accepted ingress event must wake")
	mirror, exists, err := state.ReadWakeBudget(keys[0].Recipient())
	require.NoError(t, err)
	require.True(t, exists)
	require.Len(t, mirror.Credits, 1)
	require.Equal(t, want, mirror.Credits[0].Sender)
	require.Equal(t, int64(1), mirror.Credits[0].Count)
}

func TestWakeSenderIngress_CLIRejectsInvalidIdentityAndEmptyDefaultsToCli(t *testing.T) {
	for _, author := range []string{"bad\tsender", "bad\x7fsender", strings.Repeat("a", 257), ""} {
		t.Run(author, func(t *testing.T) {
			t.Setenv(sqlite.AuthorIDEnv, "")
			initTestApp(t)
			state := senderIngressState(t, "")
			node := seedOneNode(t)
			t.Setenv(sqlite.AuthorIDEnv, author)
			err := initializeProcessAuthor()
			if author != "" {
				require.ErrorContains(t, err, "is invalid")
				entries, listErr := app.store.InboxList(t.Context(), "worker")
				require.NoError(t, listErr)
				require.Empty(t, entries)
				t.Setenv(sqlite.AuthorIDEnv, "")
				require.NoError(t, initializeProcessAuthor())
			} else {
				require.NoError(t, err)
			}
			require.NoError(t, runComment(node, "valid CLI comment", "worker"))
			assertSenderIngressWake(t, state, "cli")
		})
	}
}
