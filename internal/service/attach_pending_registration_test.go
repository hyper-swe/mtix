// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package service_test

import (
	"os"
	"reflect"
	"testing"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/stretchr/testify/require"
)

// The old API has no routing dependency. Optional configuration keeps this
// witness compilable on the untouched base; the assertion tests actual board
// registration and queue state, rather than the presence of a new method.
func configurePendingWitness(t *testing.T, f *regFixture, state any, root string) {
	t.Helper()
	routing := reflect.ValueOf(&f.svc.RegistrationDeps).Elem().FieldByName("Routing")
	if !routing.IsValid() {
		return
	}
	value := reflect.New(routing.Type().Elem())
	value.Elem().FieldByName("State").Set(reflect.ValueOf(state))
	value.Elem().FieldByName("LanePath").SetString(root)
	board := value.Elem().FieldByName("Board")
	board.FieldByName("Key").SetString("11111111-1111-4111-8111-111111111111")
	board.FieldByName("Path").SetString(root)
	routing.Set(value)
}

func TestRegisterSession_OperatorWriteDenied_QueuesLaneAndList(t *testing.T) {
	f := newRegFixture(t)
	state := operatorAlertsFixture(t)
	require.NoError(t, state.Ensure())
	dir, err := state.Path()
	require.NoError(t, err)
	require.NoError(t, os.Chmod(dir, 0500))
	t.Cleanup(func() { require.NoError(t, os.Chmod(dir, 0700)) })
	configurePendingWitness(t, f, state, t.TempDir())
	got, err := f.svc.RegisterSession(t.Context(), req("lane-a", 11))
	require.NoError(t, err)
	require.Equal(t, service.OutcomeRegistered, got.Outcome)
	var count int
	require.NoError(t, f.store.WriteDB().QueryRowContext(t.Context(), "SELECT count(*) FROM agent_registrations WHERE agent_id=?", "lane-a").Scan(&count))
	require.Equal(t, 1, count, "registration must remain durable")
	require.NoError(t, f.store.WriteDB().QueryRowContext(t.Context(), "SELECT count(*) FROM attach_pending WHERE kind IN (?,?)", "lane", "list").Scan(&count))
	require.Equal(t, 2, count, "denied operator side writes must be queued")
}
