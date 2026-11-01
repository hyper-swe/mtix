// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package service_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
)

func TestHostDispatcher_InvalidCapabilities_Refused(t *testing.T) {
	f := newHostFixture(t)
	for _, kind := range []string{"state", "opener"} {
		t.Run(kind, func(t *testing.T) {
			o := service.HostDispatcherOptions{State: f.state, OpenBoard: func(context.Context, oplocal.ListedBoard) (*service.HostBoard, error) { return f.board(), nil }}
			if kind == "state" {
				o.State = nil
			} else {
				o.OpenBoard = nil
			}
			_, err := service.NewHostDispatcher(o)
			require.ErrorIs(t, err, model.ErrInvalidInput)
		})
	}
	for _, kind := range []string{"store", "state", "path", "host", "uid", "pid"} {
		t.Run(kind, func(t *testing.T) {
			o := service.HostBoardOptions{Store: f.store, State: f.state, Path: f.root, Host: service.RegistrationHost{HostID: f.host, UID: "test-uid"}, PID: 42}
			switch kind {
			case "store":
				o.Store = nil
			case "state":
				o.State = nil
			case "path":
				o.Path = ""
			case "host":
				o.Host.HostID = ""
			case "uid":
				o.Host.UID = ""
			case "pid":
				o.PID = 0
			}
			_, err := service.NewHostBoard(o)
			require.ErrorIs(t, err, model.ErrInvalidInput)
		})
	}
}
func TestHostDispatcher_InboxOnlyFallback_ClaimsNothing(t *testing.T) {
	f := newHostFixture(t)
	f.event()
	d, err := service.NewHostDispatcher(service.HostDispatcherOptions{State: f.state, OpenBoard: func(context.Context, oplocal.ListedBoard) (*service.HostBoard, error) {
		return service.NewHostBoard(service.HostBoardOptions{Store: f.store, State: f.state, Path: f.root, Host: service.RegistrationHost{HostID: f.host, UID: "test-uid"}, PID: 42})
	}})
	require.NoError(t, err)
	require.NoError(t, d.Pass(t.Context()))
	var count int
	require.NoError(t, f.store.ReadDB().QueryRowContext(t.Context(), "SELECT count(*) FROM wake_ledger").Scan(&count))
	require.Zero(t, count)
	events, err := f.store.InboxList(t.Context(), "worker")
	require.NoError(t, err)
	require.Len(t, events, 1)
}
func TestHostDispatcher_OpenFailure_DoesNotStopOtherBoards(t *testing.T) {
	one, two := newHostFixture(t), newHostFixture(t)
	two.state, two.host = one.state, one.host
	require.NoError(t, one.state.ListBoardInstance(t.Context(), oplocal.ListedBoard{Path: two.root, Key: two.key, StoreInstanceID: two.instance}))
	paths := []string{one.root, two.root}
	sort.Strings(paths)
	fixtures := map[string]*hostFixture{one.root: one, two.root: two}
	fail := errors.New("unavailable checkout")
	d, err := service.NewHostDispatcher(service.HostDispatcherOptions{State: one.state, OpenBoard: func(_ context.Context, row oplocal.ListedBoard) (*service.HostBoard, error) {
		if row.Path == paths[0] {
			return nil, fail
		}
		return fixtures[row.Path].board(), nil
	}})
	require.NoError(t, err)
	require.ErrorIs(t, d.Pass(t.Context()), fail)
	beats, err := fixtures[paths[1]].store.ListDispatcherHeartbeats(t.Context(), one.host, "test-uid")
	require.NoError(t, err)
	require.Len(t, beats, 1)
}
func TestHostDispatcher_RegistryAndCancellation_RefuseBeforeOpening(t *testing.T) {
	for _, kind := range []string{"corrupt", "legacy", "cancel", "nil-board"} {
		t.Run(kind, func(t *testing.T) {
			f := newHostFixture(t)
			opened := 0
			d, err := service.NewHostDispatcher(service.HostDispatcherOptions{State: f.state, OpenBoard: func(context.Context, oplocal.ListedBoard) (*service.HostBoard, error) { opened++; return nil, nil }})
			require.NoError(t, err)
			ctx := t.Context()
			switch kind {
			case "corrupt":
				require.NoError(t, f.state.WriteJSON("boards", []string{"invalid"}))
			case "legacy":
				require.NoError(t, f.state.WriteJSON("boards", oplocal.DispatchBoards{Listed: []oplocal.ListedBoard{{Key: f.key, Path: f.root}}}))
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			err = d.Pass(ctx)
			if kind == "legacy" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			if kind == "nil-board" {
				require.Equal(t, 1, opened)
			} else {
				require.Zero(t, opened)
			}
		})
	}
}
func TestHostDispatcher_CurrentIdentity_InvalidStoreRefused(t *testing.T) {
	for _, kind := range []string{"missing-instance", "changed-board", "conflicting-ids", "invalid-id", "symlink-data"} {
		t.Run(kind, func(t *testing.T) {
			f := newHostFixture(t)
			switch kind {
			case "missing-instance":
				_, err := f.store.WriteDB().ExecContext(t.Context(), "DELETE FROM meta WHERE key=?", "store_instance_id")
				require.NoError(t, err)
			case "changed-board":
				require.NoError(t, os.WriteFile(filepath.Join(f.root, ".mtix", "board-id"), []byte("22222222-2222-4222-8222-222222222222\n"), 0644))
			case "conflicting-ids":
				require.NoError(t, os.WriteFile(filepath.Join(f.root, ".mtix", "board-id"), []byte(f.key+"\n22222222-2222-4222-8222-222222222222\n"), 0644))
			case "invalid-id":
				require.NoError(t, os.WriteFile(filepath.Join(f.root, ".mtix", "board-id"), []byte("invalid\n"), 0644))
			case "symlink-data":
				path := filepath.Join(f.root, ".mtix", "data")
				require.NoError(t, os.Rename(path, path+"-original"))
				require.NoError(t, os.Symlink(path+"-original", path))
			}
			require.Error(t, f.pass())
			require.Zero(t, f.dials)
			require.Zero(t, f.hooks)
		})
	}
}
