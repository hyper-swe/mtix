// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

// Lookup controls distinguish exhaustion from driver and iteration failures.

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

type identityQuery struct {
	rows pgx.Rows
	err  error
	t    *testing.T
}

func (q identityQuery) Query(_ context.Context, query string, args ...any) (pgx.Rows, error) {
	assert.Equal(q.t, `SELECT project_prefix, first_event_hash FROM sync_projects WHERE project_prefix = $1`, query)
	assert.Equal(q.t, []any{"TEST"}, args)
	return q.rows, q.err
}

type identityRows struct {
	pgx.Rows
	found        bool
	prefix, hash string
	err, scanErr error
	closed       bool
}

func (r *identityRows) Next() bool { return r.found }
func (r *identityRows) Scan(dest ...any) error {
	if r.scanErr != nil {
		return r.scanErr
	}
	*dest[0].(*string) = r.prefix
	*dest[1].(*string) = r.hash
	return nil
}
func (r *identityRows) Err() error { return r.err }
func (r *identityRows) Close()     { r.closed = true }

func TestQueryHubIdentity_ObservedRowsAndFailures_RemainDistinct(t *testing.T) {
	failure := errors.New("driver refused")
	cases := []struct {
		name              string
		rows              *identityRows
		queryErr, wantErr error
		prefix, hash      string
	}{
		{name: "Known", rows: &identityRows{found: true, prefix: "TEST", hash: "first"}, prefix: "TEST", hash: "first"},
		{name: "Missing", rows: &identityRows{}},
		{name: "EmptyHash", rows: &identityRows{found: true, prefix: "TEST"}},
		{name: "QueryFailure", queryErr: failure, wantErr: failure},
		{name: "IterationFailure", rows: &identityRows{err: failure}, wantErr: failure},
		{name: "ScanFailure", rows: &identityRows{found: true, scanErr: failure}, wantErr: failure},
		{name: "FollowingFailure", rows: &identityRows{found: true, err: failure}, wantErr: failure},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prefix, hash, err := queryHubIdentity(t.Context(), identityQuery{rows: tc.rows, err: tc.queryErr, t: t}, "TEST")
			if tc.wantErr != nil {
				assert.ErrorIs(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.prefix, prefix)
				assert.Equal(t, tc.hash, hash)
			}
			if tc.rows != nil {
				assert.True(t, tc.rows.closed, "driver rows are closed")
			}
		})
	}
}
