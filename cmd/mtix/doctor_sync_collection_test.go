// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Doctor collection tests pin transport selection before execution and retain legacy rows.
package main

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/testutil"
)

func doctorCollectionApp(t *testing.T) {
	t.Helper()
	testutil.OperatorState(t)
	t.Setenv("MTIX_SYNC_DSN", "")
	initTestApp(t)
	store := app.store
	t.Cleanup(func() {
		require.NoError(t, store.Close())
		if app.store == store {
			app.store = nil
		}
	})
}

func TestDoctorSync_ConfigurationSelectsApplicableTransports(t *testing.T) {
	for _, tc := range []struct {
		name     string
		postgres bool
		relay    bool
	}{{"none", false, false}, {"relay", false, true}, {"postgres", true, false}, {"both", true, true}} {
		t.Run(tc.name, func(t *testing.T) {
			doctorCollectionApp(t)
			if tc.postgres {
				t.Setenv("MTIX_SYNC_DSN", "postgres://fixture:synthetic_secret@/%zz")
			}
			if tc.relay {
				_, err := app.configSvc.Set("sync.relay.dir", filepath.Join(t.TempDir(), "absent"))
				require.NoError(t, err)
			}
			req := configuredDoctorCollection()
			assert.Equal(t, tc.postgres, req.Postgres)
			assert.Equal(t, tc.relay, req.Relay)
			svc, err := newConfiguredDoctorService()
			require.NoError(t, err)
			rows, err := svc.Run(t.Context(), service.DoctorRunOptions{})
			if tc.postgres || tc.relay {
				assert.ErrorIs(t, err, service.ErrDoctorChecksFailed)
				assert.NotEmpty(t, rows)
			} else {
				assert.NoError(t, err)
				for _, row := range rows {
					if assertEmptyWakeDoctorInfo(t, row.ID, string(row.Status), row.Detail, row.Fix) {
						continue
					}
					assert.Equal(t, "platform.windows", row.ID)
				}
			}
		})
	}
}

type doctorContact struct {
	accepted bool
	err      error
}

func doctorContactListener(t *testing.T) (net.Listener, <-chan doctorContact) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	result := make(chan doctorContact, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			result <- doctorContact{err: err}
			return
		}
		result <- doctorContact{accepted: true, err: conn.Close()}
	}()
	t.Cleanup(func() {
		assert.NoError(t, listener.Close())
		select {
		case outcome := <-result:
			assert.False(t, outcome.accepted, "unused Postgres must receive no connection")
			assert.ErrorIs(t, outcome.err, net.ErrClosed)
		case <-time.After(3 * time.Second):
			t.Error("owned listener worker did not join")
		}
	})
	return listener, result
}

func TestDoctorSync_RelayOnly_DoesNotContactUnusedPostgres(t *testing.T) {
	doctorCollectionApp(t)
	listener, _ := doctorContactListener(t)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	report := collectSyncDoctor(ctx, syncDoctorCollection{DSN: "postgres://fixture@" + listener.Addr().String() + "/fixture", Relay: true})
	for _, row := range report.Checks {
		assert.NotEqual(t, "PG reachable", row.Name)
		assert.NotEqual(t, "schema current", row.Name)
		assert.NotEqual(t, "hub-triggers", row.Name)
		assert.NotEqual(t, "hub-privileges", row.Name)
	}
	require.NoError(t, ctx.Err())
}

func TestDoctorSync_MappingRetainsFutureRowsAndStatus(t *testing.T) {
	report := DoctorReport{Checks: []DoctorCheck{
		{Name: "future.check", Pass: true, Detail: "future diagnostic", Fix: "future remedy"},
		{Name: "schema current", Pass: true, Warn: true}, {Name: "sync.exchange-record", Pass: false, Warn: true},
	}}
	rows := doctorSyncRows(report)
	require.Len(t, rows, 3)
	assert.Equal(t, service.DoctorResult{ID: "sync.future.check", Status: service.DoctorPass, Detail: "future diagnostic", Fix: "future remedy"}, rows[0])
	assert.Equal(t, "sync.schema-current", rows[1].ID)
	assert.Equal(t, service.DoctorWarn, rows[1].Status)
	assert.Equal(t, service.DoctorFail, rows[2].Status)
	assert.Equal(t, "sync.exchange-record", rows[2].ID)
}

func TestDoctor_RoutingAndAutoImport_ExplicitlyLocalDiagnostic(t *testing.T) {
	root := newRootCmd()
	doc, _, err := root.Find([]string{"doctor"})
	require.NoError(t, err)
	assert.True(t, isExemptCommand(doc))
	assert.True(t, shouldSkipAutoImport("doctor"))
	assert.False(t, isMutationCommand("doctor"))
	var names []string
	for _, cmd := range root.Commands() {
		names = append(names, cmd.Name())
	}
	assert.Len(t, names, 52, strings.Join(names, ","))
}

func TestDoctor_JSONFlagErrorAndOutsideProject(t *testing.T) {
	testutil.OperatorState(t)
	saveAndResetApp(t)
	t.Chdir(t.TempDir())
	svc, err := newConfiguredDoctorService()
	require.NoError(t, err)
	cmd := newDoctorCommand(func() (*service.DoctorService, error) { return svc, nil })
	err = cmd.ExecuteContext(t.Context())
	assert.ErrorContains(t, err, "JSON flag")
	assert.False(t, errors.Is(err, service.ErrDoctorChecksFailed))
	assert.NotNil(t, cmd)
}
