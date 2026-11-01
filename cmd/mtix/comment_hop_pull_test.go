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

func TestPull_MalformedCommentHop_AppliesWithoutQuarantine(t *testing.T) {
	for _, hop := range []string{`null`, `-1`, `"2"`, `1.5`, `9223372036854775808`} {
		t.Run(hop, func(t *testing.T) {
			testutil.OperatorState(t)
			initTestApp(t)
			events := offlineEvents(t)
			comment := *events[1]
			comment.OpType = model.OpComment
			comment.Payload = json.RawMessage(`{"author_id":"sender","body":"preserve pulled comment","to":"worker","hop":` + hop + `}`)
			batch := []*model.SyncEvent{events[0], &comment}
			hub := &fakeLateHub{events: batch, pullEvents: batch, hubNows: []time.Time{sweepHubT1}}
			var stderr bytes.Buffer
			got, err := pullThenSweep(t.Context(), testIngest(&stderr), hub, app.store, transport.PullCursor{}, 100)
			require.NoError(t, err, stderr.String())
			require.Equal(t, 2, got.pulled, stderr.String())
			require.Empty(t, quarantined(t))
			inbox, err := app.store.InboxList(t.Context(), "worker")
			require.NoError(t, err)
			require.Len(t, inbox, 1)
			require.Equal(t, "preserve pulled comment", inbox[0].Body)
			var stored string
			require.NoError(t, app.store.QueryRow(t.Context(), "SELECT payload FROM sync_events WHERE event_id=?", comment.EventID).Scan(&stored))
			require.Equal(t, string(comment.Payload), stored)
		})
	}
}
