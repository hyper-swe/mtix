// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

func TestPullCompletion_ZeroBatches_RetiresExistingCloneWitness(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "delete-failure"}[fail], func(t *testing.T) {
			initTestApp(t)
			e := remoteCreateAt(t, "TEST-1", 1)
			require.NoError(t, applyBatch(t.Context(), testIngest(nil), app.store, []*model.SyncEvent{e}))
			// Reproduce a cursor and witness left together by the earlier implementation.
			// Bypass retirement deliberately to construct this legacy candidate state.
			require.NoError(t, app.store.WithTx(t.Context(), func(tx *sql.Tx) error {
				return upsertMeta(t.Context(), tx, [2]string{"meta.sync.last_pulled_clock", "1"}, [2]string{"meta.sync.last_pulled_event_id", e.EventID})
			}))
			before := cloneCompletionWitness(t)
			require.NotEmpty(t, before)
			if fail {
				_, err := app.store.WriteDB().ExecContext(t.Context(), `CREATE TRIGGER refuse_pull_retire BEFORE DELETE ON meta WHEN OLD.key='meta.sync.clone.wake_bootstrap' BEGIN SELECT RAISE(ABORT,'forced pull retirement failure'); END`)
				require.NoError(t, err)
			}
			pulled, batches, err := pullLoop(t.Context(), testIngest(&bytes.Buffer{}), &fakeLateHub{}, app.store, transport.CursorAt(e), 2)
			require.Zero(t, pulled)
			require.Zero(t, batches)
			if fail {
				require.ErrorContains(t, err, "forced pull retirement failure")
				assert.Equal(t, before, cloneCompletionWitness(t))
			} else {
				require.NoError(t, err)
				assert.Empty(t, cloneCompletionWitness(t))
			}
		})
	}
}

func TestPullCompletion_CursorWriteFailure_RollsBackCursorAndIntent(t *testing.T) {
	for _, tt := range []struct{ name, trigger string }{
		{"clock", `BEFORE INSERT ON meta WHEN NEW.key='meta.sync.last_pulled_clock'`},
		{"event-id", `BEFORE INSERT ON meta WHEN NEW.key='meta.sync.last_pulled_event_id'`},
		{"intent", `BEFORE DELETE ON meta WHEN OLD.key='meta.sync.clone.wake_bootstrap'`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			e := remoteCreateAt(t, "TEST-1", 1)
			require.NoError(t, applyBatch(t.Context(), testIngest(nil), app.store, []*model.SyncEvent{e}))
			before, err := readLastPulledClock(t.Context(), app.store)
			require.NoError(t, err)
			witness := cloneCompletionWitness(t)
			require.NotEmpty(t, witness)
			_, err = app.store.WriteDB().ExecContext(t.Context(), "CREATE TRIGGER refuse_pull_finish "+tt.trigger+" BEGIN SELECT RAISE(ABORT,'forced pull completion failure'); END")
			require.NoError(t, err)
			_, err = applyPullBatch(t.Context(), testIngest(nil), app.store, quarantineSourcePull, []*model.SyncEvent{e})
			require.ErrorContains(t, err, "forced pull completion failure")
			after, err := readLastPulledClock(t.Context(), app.store)
			require.NoError(t, err)
			assert.Equal(t, before, after)
			assert.Equal(t, witness, cloneCompletionWitness(t))
			_, err = app.store.WriteDB().ExecContext(t.Context(), `DROP TRIGGER refuse_pull_finish`)
			require.NoError(t, err)
			_, err = applyPullBatch(t.Context(), testIngest(nil), app.store, quarantineSourcePull, []*model.SyncEvent{e})
			require.NoError(t, err)
			after, err = readLastPulledClock(t.Context(), app.store)
			require.NoError(t, err)
			assert.Equal(t, transport.CursorAt(e), after)
			assert.Empty(t, cloneCompletionWitness(t))
		})
	}
}
