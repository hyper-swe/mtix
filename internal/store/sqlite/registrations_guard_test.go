// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Registration writes must pass the store's write guards (fail-stop, refused
// filesystems, free-space floor): none of them may write around withWriteTx.
func TestRegistrationWrites_AfterFailStop_AreRefused(t *testing.T) {
	s, _ := newDurabilityTestStore(t)
	ctx := context.Background()
	key := RegistrationKey{AgentID: "w", HostID: "h", UID: "1"}
	_, err := s.RegisterSessionAtomic(ctx, key, func(*Registration, int64) (*Registration, error) {
		return &Registration{AgentID: "w", HostID: "h", UID: "1", Harness: "codex", Origin: "manual", SessionHandle: "s",
			RegisteredAt: "r", LastSeen: "l", RegistrarVersion: "t", AttachProtocol: 1}, nil
	})
	require.NoError(t, err)

	require.Error(t, s.WithTx(ctx, func(*sql.Tx) error { return errors.New("database or disk is full (13)") }))

	_, err = s.TouchRegistration(ctx, key, "s", "now")
	require.ErrorContains(t, err, "fail-stop")
	_, err = s.DeleteRegistrationIf(ctx, key, RegistrationSnapshot{Epoch: 0, LastSeen: "l"})
	require.ErrorContains(t, err, "fail-stop")
	rows, err := s.ListRegistrations(ctx, "h", "1")
	require.NoError(t, err)
	assert.Len(t, rows, 1, "reads still work and the row was not deleted")

	// An inbox read on a fail-stopped store still answers; the last_seen refresh is skipped quietly.
	_, err = s.InboxList(WithSessionTouch(ctx, SessionTouch{HostID: "h", UID: "1", Handle: "s"}), "w")
	require.NoError(t, err)
}

func registrationFixture(t *testing.T) (*Store, RegistrationKey) {
	t.Helper()
	s, _ := newDurabilityTestStore(t)
	key := RegistrationKey{AgentID: "w", HostID: "h", UID: "1"}
	_, err := s.RegisterSessionAtomic(context.Background(), key, func(*Registration, int64) (*Registration, error) {
		return &Registration{AgentID: "w", HostID: "h", UID: "1", Harness: "codex", Origin: "manual", SessionHandle: "s",
			RegisteredAt: "r", LastSeen: "l", RegistrarVersion: "t", AttachProtocol: 1}, nil
	})
	require.NoError(t, err)
	return s, key
}

func TestRegisterSessionAtomic_WriteRefusedByTrigger_ReturnsWrappedError(t *testing.T) {
	s, key := registrationFixture(t)
	_, err := s.WriteDB().Exec(`CREATE TRIGGER block_registration BEFORE INSERT ON agent_registrations BEGIN SELECT RAISE(ABORT, 'blocked'); END`)
	require.NoError(t, err)
	_, err = s.RegisterSessionAtomic(context.Background(), key, func(e *Registration, _ int64) (*Registration, error) {
		next := *e
		next.Epoch++
		return &next, nil
	})
	require.ErrorContains(t, err, "blocked")
}

func TestRegistrationReads_CorruptRow_ReturnWrappedErrors(t *testing.T) {
	s, key := registrationFixture(t)
	ctx := context.Background()
	_, err := s.WriteDB().Exec(`UPDATE agent_registrations SET epoch='not-a-number'`)
	require.NoError(t, err)
	_, err = s.ListRegistrations(ctx, "h", "1")
	require.ErrorContains(t, err, "scan registration")
	_, err = s.RegisterSessionAtomic(ctx, key, func(*Registration, int64) (*Registration, error) { return nil, nil })
	require.ErrorContains(t, err, "read registration")
}

func TestRegistrationWrites_MissingTable_ReturnWrappedErrors(t *testing.T) {
	s, key := registrationFixture(t)
	ctx := context.Background()
	_, err := s.WriteDB().Exec(`DROP TABLE agent_registrations`)
	require.NoError(t, err)
	_, err = s.TouchRegistration(ctx, key, "s", "now")
	require.ErrorContains(t, err, "touch registration")
	_, err = s.DeleteRegistrationIf(ctx, key, RegistrationSnapshot{})
	require.ErrorContains(t, err, "delete registration")
	_, err = s.ListRegistrations(ctx, "h", "1")
	require.ErrorContains(t, err, "list registrations")
	// An inbox read logs the failed refresh and still answers.
	_, err = s.InboxList(WithSessionTouch(ctx, SessionTouch{HostID: "h", UID: "1", Handle: "s"}), "w")
	require.NoError(t, err)
}
