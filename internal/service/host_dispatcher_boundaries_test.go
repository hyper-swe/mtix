// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package service_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/sync/pushlock"
)

func (f *hostFixture) localEnrollment() *service.HostEnrollment {
	return service.NewHostEnrollment(service.HostEnrollmentOptions{State: f.state, Clock: func() time.Time { return f.now }, OpenBoard: func(context.Context, string) (*service.HostBoard, error) { return f.board(), nil }})
}

func TestHostDispatcher_PendingRouting_DrainedOnNextKnownBoardPass(t *testing.T) {
	f := newHostFixture(t)
	payload, err := json.Marshal(map[string]string{"agent_id": "worker", "harness": "codex", "lane_path": f.root})
	require.NoError(t, err)
	_, err = f.store.EnqueueAttachPending(t.Context(), sqlite.AttachPending{Kind: "list", BoardKey: f.key, Path: f.root, Payload: string(payload), QueuedAt: f.now.Format(time.RFC3339Nano)})
	require.NoError(t, err)
	rows, err := f.store.ListAttachPending(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.NoError(t, f.pass())
	rows, err = f.store.ListAttachPending(t.Context())
	require.NoError(t, err)
	require.Empty(t, rows)
	listed, err := f.state.ListedBoards(t.Context())
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.Equal(t, f.instance, listed[0].StoreInstanceID)
}

func TestHostDispatcher_DispatchLockContention_ScanStillRunsAndEffectsRetry(t *testing.T) {
	f := newHostFixture(t)
	f.event()
	require.NoError(t, f.localEnrollment().Enroll(t.Context(), service.EnrollmentRequest{Path: f.root, Mode: "yes", Source: "test"}))
	lock, err := pushlock.AcquireNamed(filepath.Join(f.root, ".mtix"), "dispatch.lock")
	require.NoError(t, err)
	require.NoError(t, f.pass())
	require.Zero(t, f.hooks)
	require.Len(t, f.adapter.seqs(), 1)
	require.NoError(t, lock.Release())
	require.NoError(t, f.pass())
	require.Equal(t, 1, f.hooks)
	require.Len(t, f.adapter.seqs(), 1)
}

func TestHostDispatcher_WorktreeMembership_ChangesRefusedEachPass(t *testing.T) {
	for _, kind := range []string{"missing-backlink", "wrong-backlink", "direct-common"} {
		t.Run(kind, func(t *testing.T) {
			f := newHostFixture(t)
			common := filepath.Join(t.TempDir(), ".git")
			admin := filepath.Join(common, "worktrees", "lane")
			require.NoError(t, os.MkdirAll(admin, 0700))
			require.NoError(t, os.WriteFile(filepath.Join(f.root, ".git"), []byte("gitdir: "+admin+"\n"), 0600))
			require.NoError(t, os.WriteFile(filepath.Join(admin, "commondir"), []byte(common+"\n"), 0600))
			backlink := filepath.Join(admin, "gitdir")
			require.NoError(t, os.WriteFile(backlink, []byte(filepath.Join(f.root, ".git")+"\n"), 0600))
			require.NoError(t, f.pass())
			switch kind {
			case "missing-backlink":
				require.NoError(t, os.Remove(backlink))
			case "wrong-backlink":
				require.NoError(t, os.WriteFile(backlink, []byte(filepath.Join(common, "wrong")+"\n"), 0600))
			case "direct-common":
				require.NoError(t, os.WriteFile(filepath.Join(f.root, ".git"), []byte("gitdir: "+common+"\n"), 0600))
			}
			require.Error(t, f.pass())
			require.Zero(t, f.dials)
			require.Zero(t, f.hooks)
		})
	}
}

func TestHostEnrollment_InvalidSecret_GrantsNothing(t *testing.T) {
	for _, kind := range []string{"mode", "symlink", "directory", "oversize"} {
		t.Run(kind, func(t *testing.T) {
			f := newHostFixture(t)
			secret := filepath.Join(f.root, ".mtix", "secrets")
			switch kind {
			case "mode":
				require.NoError(t, os.WriteFile(secret, []byte("dummy"), 0644))
			case "symlink":
				target := filepath.Join(t.TempDir(), "other-checkout-secret")
				require.NoError(t, os.WriteFile(target, []byte("dummy"), 0600))
				require.NoError(t, os.Symlink(target, secret))
			case "directory":
				require.NoError(t, os.Mkdir(secret, 0700))
			case "oversize":
				require.NoError(t, os.WriteFile(secret, make([]byte, 65537), 0600))
			}
			require.Error(t, f.localEnrollment().Enroll(t.Context(), service.EnrollmentRequest{Path: f.root, Mode: "yes", Source: "test"}))
			registry, err := f.state.DispatchBoards(t.Context())
			require.NoError(t, err)
			require.Empty(t, registry.Enrolled)
			require.Empty(t, registry.History)
		})
	}
}

func TestHostEnrollment_ImplicitRequest_RefusedBeforeOpening(t *testing.T) {
	f := newHostFixture(t)
	opened := 0
	api := service.NewHostEnrollment(service.HostEnrollmentOptions{State: f.state, OpenBoard: func(context.Context, string) (*service.HostBoard, error) { opened++; return f.board(), nil }})
	for _, mode := range []string{"", "registration", "automatic"} {
		t.Run(mode, func(t *testing.T) {
			require.Error(t, api.Enroll(t.Context(), service.EnrollmentRequest{Path: f.root, Mode: mode, Source: "test"}))
		})
	}
	require.Zero(t, opened)
}

func (f *hostFixture) hubEnrollment() *service.HostEnrollment {
	return service.NewHostEnrollment(service.HostEnrollmentOptions{State: f.state, Clock: func() time.Time { return f.now }, OpenBoard: func(context.Context, string) (*service.HostBoard, error) { return f.board(), nil }, ObserveHub: func(context.Context, string) (oplocal.HubIdentity, error) {
		return oplocal.HubIdentity{Kind: "postgres", ProjectPrefix: "TEST", FirstEventHash: "observed"}, nil
	}})
}

func TestHostDispatcher_UnenrolledWorktree_NotPulled(t *testing.T) {
	main, wt := newHostFixture(t), newHostFixture(t)
	wt.state, wt.host, wt.key = main.state, main.host, main.key
	require.NotEqual(t, main.instance, wt.instance)
	require.NoError(t, os.WriteFile(filepath.Join(wt.root, ".mtix", "board-id"), []byte(main.key+"\n"), 0644))
	common := filepath.Join(main.root, ".git")
	admin := filepath.Join(common, "worktrees", "lane")
	require.NoError(t, os.MkdirAll(admin, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(wt.root, ".git"), []byte("gitdir: "+admin+"\n"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(admin, "commondir"), []byte(common+"\n"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(admin, "gitdir"), []byte(filepath.Join(wt.root, ".git")+"\n"), 0600))
	require.NoError(t, main.state.ListBoardInstance(t.Context(), oplocal.ListedBoard{Path: wt.root, Key: wt.key, StoreInstanceID: wt.instance}))
	for _, f := range []*hostFixture{main, wt} {
		require.NoError(t, os.WriteFile(filepath.Join(f.root, ".mtix", "secrets"), []byte("dummy-checkout-secret"), 0600))
	}
	require.NoError(t, main.hubEnrollment().Enroll(t.Context(), service.EnrollmentRequest{Path: main.root, Mode: "yes", Source: "test"}))
	d, err := service.NewHostDispatcher(service.HostDispatcherOptions{State: main.state, OpenBoard: func(_ context.Context, row oplocal.ListedBoard) (*service.HostBoard, error) {
		if row.Path == main.root {
			return main.board(), nil
		}
		return wt.board(), nil
	}})
	require.NoError(t, err)
	require.NoError(t, d.Pass(t.Context()))
	require.Equal(t, 1, main.dials)
	require.Equal(t, 1, main.hooks)
	require.Zero(t, wt.dials)
	require.Zero(t, wt.hooks)
	require.Equal(t, []string{"dummy-checkout-secret"}, main.secrets)
}

func TestHostDispatcher_ContactScheduler_PersistsAcrossPasses(t *testing.T) {
	f := newHostFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(f.root, ".mtix", "secrets"), []byte("checkout-secret"), 0600))
	require.NoError(t, f.hubEnrollment().Enroll(t.Context(), service.EnrollmentRequest{Path: f.root, Mode: "yes", Source: "test"}))
	d, err := service.NewHostDispatcher(service.HostDispatcherOptions{State: f.state, OpenBoard: func(context.Context, oplocal.ListedBoard) (*service.HostBoard, error) { return f.board(), nil }})
	require.NoError(t, err)
	require.NoError(t, d.Pass(t.Context()))
	require.NoError(t, d.Pass(t.Context()))
	require.Equal(t, 1, f.dials)
	require.Equal(t, 2, f.hooks)
	f.now = f.now.Add(2 * time.Second)
	require.NoError(t, d.Pass(t.Context()))
	require.Equal(t, 2, f.dials)
}

func TestHostEnrollment_EmptyBoard_PreservesExplicitLocalAudit(t *testing.T) {
	f := newHostFixture(t)
	require.NoError(t, f.store.DeleteNode(t.Context(), "TEST-1", false, "operator"))
	request := service.EnrollmentRequest{Path: f.root, Mode: "yes", Source: "test"}
	api := f.localEnrollment()
	require.NoError(t, api.Enroll(t.Context(), request))
	require.NoError(t, api.Unenroll(t.Context(), request))
	registry, err := f.state.DispatchBoards(t.Context())
	require.NoError(t, err)
	require.Empty(t, registry.Enrolled)
	require.Len(t, registry.History, 2)
}

func TestHostDispatcher_MultipleEnrollments_IsolateCheckoutSecrets(t *testing.T) {
	one, two, listed := newHostFixture(t), newHostFixture(t), newHostFixture(t)
	fixtures := map[string]*hostFixture{}
	for i, f := range []*hostFixture{one, two, listed} {
		f.state, f.host = one.state, one.host
		fixtures[f.root] = f
		require.NoError(t, one.state.ListBoardInstance(t.Context(), oplocal.ListedBoard{Path: f.root, Key: f.key, StoreInstanceID: f.instance}))
		secret := []string{"first-checkout-secret", "second-checkout-secret", "listed-checkout-secret"}[i]
		require.NoError(t, os.WriteFile(filepath.Join(f.root, ".mtix", "secrets"), []byte(secret), 0600))
		if i < 2 {
			require.NoError(t, f.hubEnrollment().Enroll(t.Context(), service.EnrollmentRequest{Path: f.root, Mode: "yes", Source: "test"}))
		}
	}
	t.Setenv("MTIX_SYNC_DSN", "exported-unused-secret")
	dispatcher, err := service.NewHostDispatcher(service.HostDispatcherOptions{State: one.state, OpenBoard: func(_ context.Context, row oplocal.ListedBoard) (*service.HostBoard, error) {
		return fixtures[row.Path].board(), nil
	}})
	require.NoError(t, err)
	require.NoError(t, dispatcher.Pass(t.Context()))
	require.Equal(t, []string{"first-checkout-secret"}, one.secrets)
	require.Equal(t, []string{"second-checkout-secret"}, two.secrets)
	require.Zero(t, listed.dials)
	require.Zero(t, listed.hooks)
}
