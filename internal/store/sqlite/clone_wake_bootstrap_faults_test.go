// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Failures must roll back the actual applied event, all floors and the witness.
func TestCloneWakeBootstrap_WriteFailure_RollsBackBatchFloorsAndWitness(t *testing.T) {
	for _, trigger := range []string{
		`CREATE TRIGGER refuse_clone BEFORE INSERT ON hook_dispatch_cursor BEGIN SELECT RAISE(ABORT,'forced clone failure'); END`,
		`CREATE TRIGGER refuse_clone BEFORE UPDATE ON wake_scan_cursor BEGIN SELECT RAISE(ABORT,'forced clone failure'); END`,
		`CREATE TRIGGER refuse_clone BEFORE UPDATE ON agent_registrations BEGIN SELECT RAISE(ABORT,'forced clone failure'); END`,
		`CREATE TRIGGER refuse_clone BEFORE UPDATE ON wake_leases BEGIN SELECT RAISE(ABORT,'forced clone failure'); END`,
		`CREATE TRIGGER refuse_clone BEFORE INSERT ON meta WHEN NEW.key='meta.sync.clone.wake_bootstrap' BEGIN SELECT RAISE(ABORT,'forced clone failure'); END`,
	} {
		t.Run(trigger, func(t *testing.T) {
			s, raw := cloneBootstrapFixture(t, "pi")
			_, err := raw.Exec(trigger)
			require.NoError(t, err)
			require.ErrorContains(t, applyCloneBootstrapEvent(t, s, cloneBootstrapEvent(t, "TEST-1", 1)), "forced clone failure")
			assert.Zero(t, countNodes(t, raw))
			assert.Zero(t, countEvents(t, raw))
			assert.Zero(t, countApplied(t, raw))
			assert.Equal(t, []int64{0, 0, 0, 0}, cloneBootstrapMarks(t, raw))
			assert.Empty(t, cloneBootstrapWitness(t, raw))
		})
	}
}

func TestCloneWakeBootstrap_CallbackAndSnapshotFailure_RollBack(t *testing.T) {
	for _, fault := range []string{"callback", "floor-tail", "after-witness"} {
		t.Run(fault, func(t *testing.T) {
			s, raw := cloneBootstrapFixture(t, "claude")
			e := cloneBootstrapEvent(t, "TEST-1", 1)
			err := s.WithCloneWakeBootstrap(t.Context(), func(tx *sql.Tx) error {
				if err := s.IdempotentApply(t.Context(), tx, e); err != nil {
					return err
				}
				switch fault {
				case "callback":
					return errors.New("forced callback failure")
				case "floor-tail":
					_, err := tx.ExecContext(t.Context(), `DROP TABLE sync_events`)
					return err
				default:
					_, err := tx.ExecContext(t.Context(), `DROP TABLE meta`)
					return err
				}
			})
			want := map[string]string{"callback": "forced callback failure", "floor-tail": "no such table: sync_events", "after-witness": "read clone journal witness"}[fault]
			require.ErrorContains(t, err, want)
			assert.Zero(t, countNodes(t, raw))
			assert.Zero(t, countEvents(t, raw))
			assert.Zero(t, countApplied(t, raw))
			assert.Equal(t, []int64{0, 0, 0, 0}, cloneBootstrapMarks(t, raw))
			assert.Empty(t, cloneBootstrapWitness(t, raw))
		})
	}
}

func TestCloneWakeBootstrap_InitialSnapshotFailure_DoesNotApply(t *testing.T) {
	s, raw := cloneBootstrapFixture(t, "pi")
	_, err := raw.Exec(`DROP TABLE sync_events`)
	require.NoError(t, err)
	called := false
	err = s.WithCloneWakeBootstrap(t.Context(), func(*sql.Tx) error { called = true; return nil })
	require.ErrorContains(t, err, "read clone journal witness")
	assert.False(t, called)
}

func TestCloneWakeBootstrap_WitnessReadFailure_DoesNotApply(t *testing.T) {
	s, _ := cloneBootstrapFixture(t, "pi")
	require.NoError(t, applyCloneBootstrapEvent(t, s, cloneBootstrapEvent(t, "TEST-1", 1)))
	_, err := s.WriteDB().ExecContext(t.Context(), `CREATE TEMP VIEW meta AS SELECT key,
		CASE WHEN key='meta.sync.clone.wake_bootstrap' THEN abs(-9223372036854775808) ELSE value END AS value FROM main.meta`)
	require.NoError(t, err)
	called := false
	err = s.WithCloneWakeBootstrap(t.Context(), func(*sql.Tx) error { called = true; return nil })
	require.ErrorContains(t, err, "read clone wake bootstrap")
	assert.False(t, called)
	_, err = s.WriteDB().ExecContext(t.Context(), `DROP VIEW temp.meta`)
	require.NoError(t, err)
}

func TestCloneWakeBootstrap_InvalidationFailure_DoesNotApplyOrLoseIntent(t *testing.T) {
	s, raw := cloneBootstrapFixture(t, "pi")
	require.NoError(t, applyCloneBootstrapEvent(t, s, cloneBootstrapEvent(t, "TEST-1", 1)))
	witness := cloneBootstrapWitness(t, raw)
	require.NoError(t, applyOnce(t, s, cloneBootstrapEvent(t, "TEST-2", 2)))
	_, err := raw.Exec(`CREATE TRIGGER refuse_clone_delete BEFORE DELETE ON meta
		WHEN OLD.key='meta.sync.clone.wake_bootstrap' BEGIN SELECT RAISE(ABORT,'forced clone invalidation'); END`)
	require.NoError(t, err)
	called := false
	err = s.WithCloneWakeBootstrap(t.Context(), func(*sql.Tx) error { called = true; return nil })
	require.ErrorContains(t, err, "forced clone invalidation")
	assert.False(t, called)
	assert.Equal(t, witness, cloneBootstrapWitness(t, raw))
	assert.Equal(t, 2, countEvents(t, raw))
	assert.Equal(t, []int64{1, 1, 1, 1}, cloneBootstrapMarks(t, raw))
}
