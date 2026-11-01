// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

// scriptedTx records the order of calls a push makes and fails them on cue.
// Any method not overridden panics through the nil embedded Tx, so a test also
// proves that nothing else ran.
type scriptedTx struct {
	pgx.Tx
	calls    []string
	query    string
	args     []any
	execErr  error
	queryErr error
}

func (tx *scriptedTx) Exec(_ context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	tx.calls = append(tx.calls, "exec")
	tx.query, tx.args = query, args
	return pgconn.CommandTag{}, tx.execErr
}

type failRow struct{ err error }

func (r failRow) Scan(...any) error { return r.err }

func (tx *scriptedTx) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row {
	tx.calls = append(tx.calls, "queryrow")
	return failRow{err: tx.queryErr}
}

func TestSetPushTimeouts_BindsNamesAndValues_AndReturnsError(t *testing.T) {
	tests := []struct {
		name    string
		execErr error
	}{
		{"success", nil},
		{"set_config fails", errors.New("setting denied")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx := &scriptedTx{execErr: tt.execErr}
			err := setPushTimeouts(context.Background(), tx)
			if tt.execErr == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, tt.execErr)
			}
			require.Contains(t, tx.query, "set_config(b.name, b.value, true)")
			require.Equal(t, []any{
				[]string{"idle_in_transaction_session_timeout", "transaction_timeout"},
				[]string{"1min", "10min"},
			}, tx.args)
		})
	}
}

// TestPushEventsInTx_SetConfigFails_ReturnsErrorBeforeAnyHubRead drives the
// real push body with a failing set_config: the error must come back and no
// other statement may run (the nil embedded Tx would panic on one).
func TestPushEventsInTx_SetConfigFails_ReturnsErrorBeforeAnyHubRead(t *testing.T) {
	denied := errors.New("setting denied")
	tx := &scriptedTx{execErr: denied}
	p := &Pool{}
	_, err := p.pushEventsInTx(context.Background(), tx, nil)
	require.ErrorIs(t, err, denied)
	require.Equal(t, []string{"exec"}, tx.calls)
}

// TestPushEventsInTx_Succeeds_SetsTimeoutsBeforeFirstHubRead pins the order: a
// call that moved after, or was dropped before, the first hub read fails here.
func TestPushEventsInTx_Succeeds_SetsTimeoutsBeforeFirstHubRead(t *testing.T) {
	stop := errors.New("stop after first read")
	tx := &scriptedTx{queryErr: stop}
	p := &Pool{}
	_, err := p.pushEventsInTx(context.Background(), tx, nil)
	// The epoch read redacts and flattens the error, so match its text.
	require.ErrorContains(t, err, stop.Error())
	require.Equal(t, []string{"exec", "queryrow"}, tx.calls,
		"timeouts must be installed before the first hub read")
}

// TestIsTransient_PushTimeoutAborts_Retryable keeps a hub-side abort of a
// stalled or over-long push from wedging every later push.
func TestIsTransient_PushTimeoutAborts_Retryable(t *testing.T) {
	tests := []struct {
		name string
		code string
		want bool
	}{
		{"idle_in_transaction_session_timeout", "25P03", true},
		{"transaction_timeout", "25P04", true},
		{"statement_timeout stays retryable", "57014", true},
		{"unrelated integrity error stays permanent", "23514", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &pgconn.PgError{Code: tt.code}
			require.Equal(t, tt.want, isTransient(err))
			require.Equal(t, tt.want, isTransient(errors.Join(errors.New("commit"), err)))
		})
	}
}
