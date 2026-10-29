// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRegisterAgentWithStatus_CommitFails_NextRegistrationSucceeds makes the
// registration's COMMIT fail on a deferred foreign key and checks the writer
// connection is left usable, so the next registration succeeds (MTIX-198).
func TestRegisterAgentWithStatus_CommitFails_NextRegistrationSucceeds(t *testing.T) {
	agentSvc, _, s, _ := newTestAgentService(t)
	ctx := context.Background()
	_, err := s.WriteDB().ExecContext(ctx, `
CREATE TABLE fault_parent (id INTEGER PRIMARY KEY);
CREATE TABLE fault_child (p INTEGER REFERENCES fault_parent(id) DEFERRABLE INITIALLY DEFERRED);
CREATE TRIGGER fault_trg AFTER INSERT ON agents BEGIN INSERT INTO fault_child(p) VALUES (999); END;`)
	require.NoError(t, err)

	_, err = agentSvc.RegisterAgentWithStatus(ctx, "agent-fault", "PROJ")
	require.ErrorContains(t, err, "FOREIGN KEY", "the commit failure must be reported")

	_, err = s.WriteDB().ExecContext(ctx, `DROP TRIGGER fault_trg`)
	require.NoError(t, err)
	created, err := agentSvc.RegisterAgentWithStatus(ctx, "agent-fault", "PROJ")
	require.NoError(t, err, "the next registration after a failed commit must succeed")
	require.True(t, created)
}

// TestRegisterAgentWithStatus_RecoveryFails_ReportsBothErrors makes the
// recovery ROLLBACK fail too: registration must report both errors wrapped,
// and the writer must still recover (MTIX-198).
func TestRegisterAgentWithStatus_RecoveryFails_ReportsBothErrors(t *testing.T) {
	agentSvc, _, s, _ := newTestAgentService(t)
	ctx := context.Background()
	_, err := s.WriteDB().ExecContext(ctx, `
CREATE TABLE fault_parent (id INTEGER PRIMARY KEY);
CREATE TABLE fault_child (p INTEGER REFERENCES fault_parent(id) DEFERRABLE INITIALLY DEFERRED);
CREATE TRIGGER fault_trg AFTER INSERT ON agents BEGIN INSERT INTO fault_child(p) VALUES (999); END;`)
	require.NoError(t, err)
	injected := errors.New("injected rollback failure")
	agentSvc.SetWriterRollbackForTest(func(context.Context, *sql.Conn) error { return injected })

	_, err = agentSvc.RegisterAgentWithStatus(ctx, "agent-fault", "PROJ")
	require.ErrorIs(t, err, injected, "the recovery error must be reported with %w")
	require.ErrorContains(t, err, "FOREIGN KEY")

	agentSvc.SetWriterRollbackForTest(nil)
	_, err = s.WriteDB().ExecContext(ctx, `DROP TRIGGER fault_trg`)
	require.NoError(t, err)
	created, err := agentSvc.RegisterAgentWithStatus(ctx, "agent-fault", "PROJ")
	require.NoError(t, err)
	require.True(t, created)
}
