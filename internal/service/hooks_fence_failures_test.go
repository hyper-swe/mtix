// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests that dispatch writer/storage errors are logged without becoming
// identity misses or escaping the asynchronous hook pass.
package service

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/hooks"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func TestHooksFence_StorageFailureIsLogged(t *testing.T) {
	for _, operation := range []string{"floor", "outcome", "stale scan", "stale claim"} {
		t.Run(operation, func(t *testing.T) {
			var logs bytes.Buffer
			d := newEncodingDispatcher(t, &logs)
			require.NoError(t, d.store.Close())
			ctx := context.Background()
			event := sqlite.JournalEvent{Seq: 1, EventID: "observed"}
			switch operation {
			case "floor":
				d.advanceHookSnapshot(ctx, []sqlite.JournalEvent{event})
			case "outcome":
				d.recordOutcome(ctx, "fenced", event, sqlite.OutcomeDelivered)
			case "stale scan":
				d.refireStaleClaims(ctx, hooks.Config{}, nil, hooks.ExecDispatchAny)
			case "stale claim":
				d.refireStaleClaim(ctx, hooks.Config{}, sqlite.HookClaim{Hook: "fenced", Seq: 1, EventID: event.EventID}, nil, hooks.ExecDispatchAny)
			}
			assert.Contains(t, logs.String(), `"level":"ERROR"`)
			assert.Contains(t, logs.String(), "closed")
			assert.Zero(t, d.HookIdentityFenceMisses())
		})
	}
}
