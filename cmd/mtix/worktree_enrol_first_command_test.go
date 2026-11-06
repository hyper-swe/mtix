// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// boardFileHoldingNodes writes <mtixDir>/tasks.json exported from a scratch
// store holding the given nodes (id, title).
func boardFileHoldingNodes(t *testing.T, mtixDir string, nodes map[string]string) {
	t.Helper()
	ctx := context.Background()
	scratch, err := sqlite.New(filepath.Join(t.TempDir(), "data"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = scratch.Close() })
	seq := 0
	for id, title := range nodes {
		seq++
		require.NoError(t, scratch.CreateNode(ctx, mkPGNode(id, "", 0, seq, title)))
	}
	exportDir := filepath.Join(t.TempDir(), ".mtix")
	require.NoError(t, os.MkdirAll(exportDir, 0o700))
	require.NoError(t, service.NewSyncService(scratch, slog.Default(), time.Now).AutoExport(ctx, exportDir))
	body, err := os.ReadFile(filepath.Join(exportDir, "tasks.json"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(mtixDir, "tasks.json"), body, 0o600))
}

func TestSeedFreshStore_StoreHoldsANodeButNoEvent_Refused(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	hub := &fakeLateHub{pullEvents: offlineEvents(t)}
	st, err := sqlite.New(filepath.Join(t.TempDir(), "fresh"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	require.NoError(t, st.CreateNode(ctx, mkPGNode("TEST-1", "", 0, 1, "imported from tasks.json")))
	_, err = st.WriteDB().ExecContext(ctx, `DELETE FROM sync_events`) // an import writes nodes without journal rows
	require.NoError(t, err)

	err = seedFreshStore(ctx, st, io.Discard, hub)

	require.ErrorIs(t, err, model.ErrConflict)
	assert.Empty(t, hub.pullCalls, "nothing is cloned over a store that holds a node")
}

func TestSeedFreshStore_StoreCannotBeProbed_Refused(t *testing.T) {
	initTestApp(t)
	hub := &fakeLateHub{pullEvents: offlineEvents(t)}
	st, err := sqlite.New(filepath.Join(t.TempDir(), "fresh"), slog.Default())
	require.NoError(t, err)
	require.NoError(t, st.Close())

	err = seedFreshStore(context.Background(), st, io.Discard, hub)

	require.Error(t, err)
	assert.Empty(t, hub.pullCalls, "an unreadable store is never cloned over")
}

func TestSeedFreshStore_NodesCannotBeCounted_Refused(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	hub := &fakeLateHub{pullEvents: offlineEvents(t)}
	st, err := sqlite.New(filepath.Join(t.TempDir(), "fresh"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	_, err = st.WriteDB().ExecContext(ctx, `ALTER TABLE nodes RENAME TO nodes_unreadable`) // the event probe still works
	require.NoError(t, err)

	err = seedFreshStore(ctx, st, io.Discard, hub)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "count nodes")
	assert.Empty(t, hub.pullCalls)
}

// TestDoctor_EnrolInterrupted_ReachableOnAnUnlistedHost goes through the real
// doctor registration: the first enrolment on a host has no listed board and no
// installed dispatcher, which is exactly when the row is needed.
func TestDoctor_EnrolInterrupted_ReachableOnAnUnlistedHost(t *testing.T) {
	initTestApp(t)
	require.NoError(t, service.WriteEnrolMarker(app.mtixDir, service.EnrolMarker{Aside: "/x/data.pre-enrol-T", Backup: "/x/bk.db", Started: "2026-11-03T01:02:03Z", PID: 9}))

	svc, err := newConfiguredDoctorService()
	require.NoError(t, err)
	rows, _ := svc.Run(context.Background(), service.DoctorRunOptions{})

	var found *service.DoctorResult
	for i := range rows {
		if rows[i].ID == "board.enrol-interrupted" {
			found = &rows[i]
		}
	}
	require.NotNil(t, found, "board.enrol-interrupted is registered whatever the host registry holds")
	assert.Equal(t, service.DoctorFail, found.Status)
}

func TestInitApp_DoctorWithAnInterruptedMarker_IsNotRefused(t *testing.T) {
	identityOperatorState(t)
	saveAndResetApp(t)
	tmp := t.TempDir()
	mtixDir := filepath.Join(tmp, ".mtix")
	require.NoError(t, os.MkdirAll(mtixDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(mtixDir, "config.yaml"), []byte("prefix: TEST\n"), 0o644))
	old, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Chdir(old) })
	require.NoError(t, os.Chdir(tmp))
	require.NoError(t, service.WriteEnrolMarker(mtixDir, service.EnrolMarker{Aside: "aside", Backup: "bk", Started: "t", PID: 77}))
	t.Cleanup(func() {
		if app.store != nil {
			_ = app.store.Close()
		}
	})

	err = initApp(&cobra.Command{Use: "doctor"}, "")

	require.NoError(t, err, "doctor must run to report the interrupted enrolment")
}

// TestDoctor_EnrolInterrupted_FixDependsOnWhetherTheStoreMoved: a crash after the
// marker and before the move leaves the live store in data and no aside
// directory; the printed fix must not move that store away.
func TestDoctor_EnrolInterrupted_FixDependsOnWhetherTheStoreMoved(t *testing.T) {
	for _, moved := range []bool{false, true} {
		name := "store never moved"
		if moved {
			name = "store moved aside"
		}
		t.Run(name, func(t *testing.T) {
			initTestApp(t)
			aside := filepath.Join(filepath.Dir(app.mtixDir), "aside-store")
			if moved {
				require.NoError(t, os.MkdirAll(aside, 0o700))
			}
			require.NoError(t, service.WriteEnrolMarker(app.mtixDir, service.EnrolMarker{Aside: aside, Backup: "/x/bk.db", Started: "t", PID: 9}))
			svc := service.NewDoctorService(service.DoctorOptions{})
			require.NoError(t, registerEnrolInterruptedDoctor(svc, app.mtixDir))

			rows, _ := svc.Run(context.Background(), service.DoctorRunOptions{})

			require.Len(t, rows, 1)
			fix := rows[0].Fix
			assert.Contains(t, fix, "in-progress.json")
			if moved {
				assert.Contains(t, fix, "kept-aside")
				assert.Contains(t, rows[0].Detail, "original store is in "+aside)
				return
			}
			assert.NotContains(t, fix, "kept-aside", "the live store is not moved when it never left")
			assert.NotContains(t, fix, "mv ")
			assert.Contains(t, rows[0].Detail, "never moved")
		})
	}
}
