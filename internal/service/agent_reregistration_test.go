// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Repeat registration tests pin heartbeat-only updates and concurrent identity reuse.
package service_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestRegisterAgent_Existing_RefreshesHeartbeatPreservingWorkAndSession(t *testing.T) {
	for _, state := range []string{"idle", "working", "stuck", "done"} {
		t.Run(state, func(t *testing.T) {
			svc, _, s, _ := newTestAgentService(t)
			ctx := context.Background()
			past := "2026-03-09T12:00:00Z"
			_, err := s.WriteDB().ExecContext(ctx, `INSERT INTO agents (agent_id,project,state,state_changed_at,last_heartbeat,current_node_id) VALUES (?,?,?,?,?,?)`, "test-worker", "TEST", state, past, past, "TEST-1")
			require.NoError(t, err)
			_, err = s.WriteDB().ExecContext(ctx, `INSERT INTO sessions (id,agent_id,project,started_at,status) VALUES (?,?,?,?,?)`, "test-session", "test-worker", "TEST", past, "active")
			require.NoError(t, err)
			created, err := svc.RegisterAgentWithStatus(ctx, "test-worker", "OTHER")
			require.NoError(t, err)
			assert.False(t, created)
			var project, gotState, changed, hb, work string
			require.NoError(t, s.QueryRow(ctx, `SELECT project,state,state_changed_at,last_heartbeat,current_node_id FROM agents WHERE agent_id = ?`, "test-worker").Scan(&project, &gotState, &changed, &hb, &work))
			assert.Equal(t, "TEST", project)
			assert.Equal(t, state, gotState)
			assert.Equal(t, past, changed)
			assert.Equal(t, "TEST-1", work)
			assert.Equal(t, "2026-03-10T12:00:00Z", hb)
			var sessionID, status, started string
			require.NoError(t, s.QueryRow(ctx, `SELECT id,status,started_at FROM sessions WHERE agent_id = ?`, "test-worker").Scan(&sessionID, &status, &started))
			assert.Equal(t, "test-session", sessionID)
			assert.Equal(t, "active", status)
			assert.Equal(t, past, started)
		})
	}
}

func TestRegisterAgent_ConcurrentRepeats_OneIdentityAllSucceed(t *testing.T) {
	svc, _, s, _ := newTestAgentService(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.RegisterAgentWithStatus(ctx, "test-worker", "TEST")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	var count int
	require.NoError(t, s.QueryRow(ctx, `SELECT COUNT(*) FROM agents WHERE agent_id = ?`, "test-worker").Scan(&count))
	assert.Equal(t, 1, count)
}

func TestRegisterAgent_InvalidOrUnavailable_ReturnsError(t *testing.T) {
	for _, tc := range []struct {
		name, id, project string
		closed, cancelled bool
		want              error
	}{
		{name: "empty ID", project: "TEST", want: model.ErrInvalidInput},
		{name: "empty project", id: "test-worker", want: model.ErrInvalidInput},
		{name: "cancelled", id: "test-worker", project: "TEST", cancelled: true, want: context.Canceled},
		{name: "closed", id: "test-worker", project: "TEST", closed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, s, _ := newTestAgentService(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancelled {
				cancel()
			}
			if tc.closed {
				require.NoError(t, s.Close())
			}
			created, err := svc.RegisterAgentWithStatus(ctx, tc.id, tc.project)
			assert.False(t, created)
			require.Error(t, err)
			if tc.want != nil {
				assert.ErrorIs(t, err, tc.want)
			}
		})
	}
}

func TestRegisterAgent_RepeatHeartbeatFailure_ReturnsError(t *testing.T) {
	svc, _, s, _ := newTestAgentService(t)
	ctx := context.Background()
	created, err := svc.RegisterAgentWithStatus(ctx, "test-worker", "TEST")
	require.NoError(t, err)
	assert.True(t, created)
	// A write failure must be propagated, rather than reported as a successful repeat.
	_, err = s.WriteDB().ExecContext(ctx, `CREATE TRIGGER reject_registration_heartbeat BEFORE UPDATE OF last_heartbeat ON agents BEGIN SELECT RAISE(ABORT, 'test heartbeat failure'); END`)
	require.NoError(t, err)
	_, err = svc.RegisterAgentWithStatus(ctx, "test-worker", "TEST")
	require.Error(t, err)
	assert.Contains(t, fmt.Sprint(err), "test heartbeat failure")
}

func TestRegisterAgent_StorageFailure_ReturnsErrorWithoutIdentity(t *testing.T) {
	for _, tc := range []struct{ name, setup, want string }{
		{"insert", `CREATE TRIGGER reject_registration BEFORE INSERT ON agents BEGIN SELECT RAISE(ABORT, 'test insert failure'); END`, "test insert failure"},
		{"commit", `CREATE TABLE registration_parent (id TEXT PRIMARY KEY);
CREATE TABLE registration_child (parent_id TEXT REFERENCES registration_parent(id) DEFERRABLE INITIALLY DEFERRED);
CREATE TRIGGER reject_registration_commit AFTER INSERT ON agents BEGIN INSERT INTO registration_child(parent_id) VALUES ('missing'); END`, "commit registration"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, s, _ := newTestAgentService(t)
			ctx := context.Background()
			_, err := s.WriteDB().ExecContext(ctx, tc.setup)
			require.NoError(t, err)
			_, err = svc.RegisterAgentWithStatus(ctx, "test-worker", "TEST")
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			var count int
			require.NoError(t, s.QueryRow(ctx, `SELECT COUNT(*) FROM agents WHERE agent_id = ?`, "test-worker").Scan(&count))
			assert.Zero(t, count, "failed registration must roll back the identity")
		})
	}
}
