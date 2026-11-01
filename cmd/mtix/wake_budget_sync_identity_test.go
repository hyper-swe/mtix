// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestWakeBudgetIdentity_SyncPushPullNeverCarriesLocalUUID(t *testing.T) {
	pool := openCmdHub(t)
	testutil.OperatorState(t)
	initTestApp(t)
	require.NoError(t, runCreate("identity boundary", "", "", 3, "", "", "", "", ""))
	key := model.WakeKey{AgentID: "identity-worker", HostID: "identity-host", UID: "identity-user", EventSeq: 1, Epoch: 1}
	_, err := app.store.ClaimWakeBudget(t.Context(), key, time.Minute, func(model.WakeBudgetProposal) (model.WakeBudgetDecision, error) {
		return model.WakeBudgetDecision{Allow: true}, nil
	})
	require.NoError(t, err)
	id, err := app.store.WakeBudgetBoardID(t.Context())
	require.NoError(t, err)
	require.NotEmpty(t, id)
	var stderr bytes.Buffer
	pushed, err := pushLoop(t.Context(), &stderr, pool, app.store)
	require.NoError(t, err)
	require.Positive(t, pushed.pushed())
	events, _, err := pool.PullEvents(t.Context(), transport.PullCursor{}, 100)
	require.NoError(t, err)
	require.NotEmpty(t, events)
	raw, err := json.Marshal(events)
	require.NoError(t, err)
	require.NotContains(t, string(raw), id)
	require.NotContains(t, string(raw), "wake_budget_board_id")
	// A fresh consumer imports only hub events; local identity is never a payload.
	resetLocalForFreshPull(t)
	_, err = app.store.WriteDB().Exec("DELETE FROM meta WHERE key=?", "wake_budget_board_id")
	require.NoError(t, err)
	pulled, _, err := pullLoop(t.Context(), testIngest(&stderr), pool, app.store, transport.PullCursor{}, 100)
	require.NoError(t, err)
	require.Positive(t, pulled)
	after, err := app.store.WakeBudgetBoardID(t.Context())
	require.NoError(t, err)
	require.Empty(t, after, "pull must not install another board's local UUID")
}
