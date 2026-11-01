// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// A 0.5.x per-project service keeps pull and hook duty until setup migrates the board (ADR-007 D11).
package service_test

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/sync/pushlock"
)

type legacyProbe struct {
	live  bool
	err   error
	calls []string
}

func (p *legacyProbe) check(_ context.Context, dir string) (bool, error) {
	p.calls = append(p.calls, dir)
	return p.live, p.err
}

func (f *hostFixture) legacyDispatcher(probe *legacyProbe) *service.HostDispatcher {
	f.t.Helper()
	d, err := service.NewHostDispatcher(service.HostDispatcherOptions{State: f.state, OpenBoard: func(context.Context, oplocal.ListedBoard) (*service.HostBoard, error) {
		return service.NewHostBoard(service.HostBoardOptions{Store: f.store, State: f.state, Path: f.root, Host: service.RegistrationHost{HostID: f.host, UID: "test-uid", Version: "test", Protocol: model.AttachProtocol}, PID: 42, Clock: func() time.Time { return f.now }, Liveness: &scanLiveness{}, Adapters: []service.WakeAdapter{f.adapter},
			Contact: service.NewContactScheduler(func() time.Time { return f.now }, func() (service.ContactInputs, error) {
				return service.ContactInputs{Mode: service.HubContactMode{Kind: service.HubContactInterval, Interval: time.Second}}, nil
			}),
			Pull: func(_ context.Context, dsn string) error { f.dials++; f.secrets = append(f.secrets, dsn); return nil }, Hooks: func(context.Context) { f.hooks++ }, LegacyService: probe.check})
	}})
	require.NoError(f.t, err)
	return d
}

func (f *hostFixture) enrollForLegacy() *service.HostEnrollment {
	f.t.Helper()
	require.NoError(f.t, os.WriteFile(filepath.Join(f.root, ".mtix", "secrets"), []byte("checkout-secret"), 0600))
	return service.NewHostEnrollment(service.HostEnrollmentOptions{State: f.state, Clock: func() time.Time { return f.now }, OpenBoard: func(context.Context, string) (*service.HostBoard, error) { return f.board(), nil }, ObserveHub: func(context.Context, string) (oplocal.HubIdentity, error) {
		return oplocal.HubIdentity{Kind: "postgres", ProjectPrefix: "TEST", FirstEventHash: "observed"}, nil
	}})
}

func TestHostDispatcher_LegacyServiceLive_SkipsPullAndHooks(t *testing.T) {
	f := newHostFixture(t)
	enroll := f.enrollForLegacy()
	require.NoError(t, enroll.Enroll(t.Context(), service.EnrollmentRequest{Path: f.root, Mode: "yes", Source: "test"}))
	probe := &legacyProbe{live: true}
	require.NoError(t, f.legacyDispatcher(probe).Pass(t.Context()))
	require.Zero(t, f.dials, "legacy service keeps the pull")
	require.Zero(t, f.hooks, "legacy service keeps the hook pass")
	require.Equal(t, []string{filepath.Join(f.root, ".mtix")}, probe.calls[:1])
	_, statErr := os.Lstat(filepath.Join(f.root, ".mtix", "sync.daemon.pid"))
	require.ErrorIs(t, statErr, os.ErrNotExist, "the host dispatcher never writes the PID file")
	probe.live = false
	require.NoError(t, f.legacyDispatcher(probe).Pass(t.Context()))
	require.Equal(t, 1, f.dials, "duty returns to the host once the legacy service is gone")
	require.Equal(t, 1, f.hooks)
}

func TestHostDispatcher_LegacyServiceLive_ScannerStillRuns(t *testing.T) {
	f := newHostFixture(t)
	require.NoError(t, f.enrollForLegacy().Enroll(t.Context(), service.EnrollmentRequest{Path: f.root, Mode: "yes", Source: "test"}))
	require.NoError(t, f.store.SetMinDispatchProtocol(t.Context(), model.AttachProtocol+1))
	require.ErrorIs(t, f.legacyDispatcher(&legacyProbe{live: true}).Pass(t.Context()), service.ErrWakeProtocolUnsupported, "the wake guard and scanner keep running for a legacy board")
	require.Zero(t, f.dials)
	require.Zero(t, f.hooks)
}

func TestHostDispatcher_LegacyProbeError_FailsClosedWithoutEffects(t *testing.T) {
	f := newHostFixture(t)
	require.NoError(t, f.enrollForLegacy().Enroll(t.Context(), service.EnrollmentRequest{Path: f.root, Mode: "yes", Source: "test"}))
	probe := &legacyProbe{err: errors.New("pid file unreadable")}
	require.Error(t, f.legacyDispatcher(probe).Pass(t.Context()))
	require.Zero(t, f.dials)
	require.Zero(t, f.hooks)
}

func TestHostDispatcher_054DaemonStartedSecond_NoDoubleFire(t *testing.T) {
	f := newHostFixture(t)
	require.NoError(t, f.enrollForLegacy().Enroll(t.Context(), service.EnrollmentRequest{Path: f.root, Mode: "yes", Source: "test"}))
	probe := &legacyProbe{}
	require.NoError(t, f.legacyDispatcher(probe).Pass(t.Context()))
	require.Equal(t, 1, f.dials)
	require.Equal(t, 1, f.hooks)
	probe.live = true // a 0.5.4 daemon starts after the host dispatcher
	f.now = f.now.Add(5 * time.Second)
	require.NoError(t, f.legacyDispatcher(probe).Pass(t.Context()))
	require.Equal(t, 1, f.dials, "no second pull while the daemon is live")
	require.Equal(t, 1, f.hooks, "no second hook firing while the daemon is live")
}

func TestHostDispatcher_LegacyDaemonFirst_BothOrdersKeepOneOwner(t *testing.T) {
	f := newHostFixture(t)
	require.NoError(t, f.enrollForLegacy().Enroll(t.Context(), service.EnrollmentRequest{Path: f.root, Mode: "yes", Source: "test"}))
	probe := &legacyProbe{live: true}
	for range 3 {
		f.now = f.now.Add(5 * time.Second)
		require.NoError(t, f.legacyDispatcher(probe).Pass(t.Context()))
	}
	require.Zero(t, f.dials)
	require.Zero(t, f.hooks)
}

func TestMigrateLegacyService_UninstallsAndEnrols(t *testing.T) {
	f := newHostFixture(t)
	var uninstalled []string
	var enrolledAtUninstall int
	var enroll *service.HostEnrollment
	enroll = service.NewHostEnrollment(service.HostEnrollmentOptions{State: f.state, Clock: func() time.Time { return f.now },
		OpenBoard: func(context.Context, string) (*service.HostBoard, error) { return f.board(), nil },
		ObserveHub: func(context.Context, string) (oplocal.HubIdentity, error) {
			return oplocal.HubIdentity{Kind: "postgres", ProjectPrefix: "TEST", FirstEventHash: "observed"}, nil
		},
		UninstallLegacy: func(ctx context.Context, root string) error {
			rows, err := enroll.List(ctx)
			require.NoError(t, err)
			enrolledAtUninstall = len(rows)
			uninstalled = append(uninstalled, root)
			return nil
		},
		LegacyLive: func(context.Context, string) (bool, error) { return false, nil }})
	require.NoError(t, enroll.MigrateLegacyService(t.Context(), service.EnrollmentRequest{Path: f.root, Mode: "yes", Source: "test"}))
	canonical, err := filepath.EvalSymlinks(f.root)
	require.NoError(t, err)
	require.Equal(t, []string{canonical}, uninstalled)
	require.Zero(t, enrolledAtUninstall, "the unit is removed before the board is enrolled")
	rows, err := enroll.List(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 1)
}

func TestMigrateLegacyService_RefusedMigration_LeavesBoardUnenrolled(t *testing.T) {
	for _, tc := range []struct {
		name      string
		uninstall func(context.Context, string) error
		live      func(context.Context, string) (bool, error)
		want      error
	}{
		{"uninstall-fails", func(context.Context, string) error { return errors.New("launchctl failed") }, func(context.Context, string) (bool, error) { return false, nil }, nil},
		{"daemon-still-live", func(context.Context, string) error { return nil }, func(context.Context, string) (bool, error) { return true, nil }, model.ErrConflict},
		{"probe-fails", func(context.Context, string) error { return nil }, func(context.Context, string) (bool, error) { return false, errors.New("unreadable") }, nil},
		{"no-uninstaller", nil, nil, model.ErrInvalidInput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newHostFixture(t)
			enroll := service.NewHostEnrollment(service.HostEnrollmentOptions{State: f.state, Clock: func() time.Time { return f.now },
				OpenBoard: func(context.Context, string) (*service.HostBoard, error) { return f.board(), nil },
				ObserveHub: func(context.Context, string) (oplocal.HubIdentity, error) {
					return oplocal.HubIdentity{Kind: "postgres", ProjectPrefix: "TEST", FirstEventHash: "observed"}, nil
				}, UninstallLegacy: tc.uninstall, LegacyLive: tc.live})
			err := enroll.MigrateLegacyService(t.Context(), service.EnrollmentRequest{Path: f.root, Mode: "yes", Source: "test"})
			require.Error(t, err)
			if tc.want != nil {
				require.ErrorIs(t, err, tc.want)
			}
			rows, listErr := enroll.List(t.Context())
			require.NoError(t, listErr)
			require.Empty(t, rows)
		})
	}
}

// migrationRig records, in one ordered slice, every step a migration takes.
type migrationRig struct {
	calls       []string
	uninstalled bool
}

func (f *hostFixture) migrationEnrollment(rig *migrationRig, observe func() (oplocal.HubIdentity, error)) *service.HostEnrollment {
	f.t.Helper()
	return service.NewHostEnrollment(service.HostEnrollmentOptions{State: f.state, Clock: func() time.Time { return f.now },
		OpenBoard: func(context.Context, string) (*service.HostBoard, error) { return f.board(), nil },
		ObserveHub: func(context.Context, string) (oplocal.HubIdentity, error) {
			rig.calls = append(rig.calls, "observe")
			return observe()
		},
		UninstallLegacy: func(context.Context, string) error {
			rig.calls = append(rig.calls, "uninstall")
			rig.uninstalled = true
			return nil
		},
		LegacyLive: func(context.Context, string) (bool, error) {
			rig.calls = append(rig.calls, "probe")
			return !rig.uninstalled, nil // a daemon runs until its unit is uninstalled
		}})
}

func observedHub() (oplocal.HubIdentity, error) {
	return oplocal.HubIdentity{Kind: "postgres", ProjectPrefix: "TEST", FirstEventHash: "observed"}, nil
}

func TestMigrateLegacyService_StepOrder_ObserveUninstallProbeThenEnrol(t *testing.T) {
	f := newHostFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(f.root, ".mtix", "secrets"), []byte("checkout-secret"), 0600))
	rig := &migrationRig{}
	enroll := f.migrationEnrollment(rig, observedHub)
	require.NoError(t, enroll.MigrateLegacyService(t.Context(), service.EnrollmentRequest{Path: f.root, Mode: "yes", Source: "test"}))
	require.Equal(t, []string{"observe", "uninstall", "probe"}, rig.calls,
		"preconditions first, then the uninstall, then the liveness check that must see the uninstalled state")
	rows, err := enroll.List(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 1)
}

func TestMigrateLegacyService_PreconditionFails_LegacyServiceUntouched(t *testing.T) {
	for _, tc := range []struct {
		name   string
		secret os.FileMode
		hubErr error
		hold   bool
	}{
		{"hub-unreachable", 0600, errors.New("hub unreachable"), false},
		{"secret-not-0600", 0644, nil, false},
		{"dispatch-lock-held", 0600, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newHostFixture(t)
			secrets := filepath.Join(f.root, ".mtix", "secrets")
			require.NoError(t, os.WriteFile(secrets, []byte("checkout-secret"), tc.secret))
			require.NoError(t, os.Chmod(secrets, tc.secret))
			if tc.hold {
				lock, err := pushlock.AcquireNamed(filepath.Join(f.root, ".mtix"), "dispatch.lock")
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, lock.Release()) })
			}
			rig := &migrationRig{}
			enroll := f.migrationEnrollment(rig, func() (oplocal.HubIdentity, error) { return oplocal.HubIdentity{}, tc.hubErr })
			if tc.hubErr == nil {
				enroll = f.migrationEnrollment(rig, observedHub)
			}
			require.Error(t, enroll.MigrateLegacyService(t.Context(), service.EnrollmentRequest{Path: f.root, Mode: "yes", Source: "test"}))
			require.NotContains(t, rig.calls, "uninstall", "no failed precondition may remove the service that owns the board")
			rows, err := enroll.List(t.Context())
			require.NoError(t, err)
			require.Empty(t, rows)
		})
	}
}

// TestMigrateLegacyService_LocalWriteFailsAfterUninstall_NamesResidualAndRerunConverges drives
// the one step that cannot be made safe in advance: a failure after the unit is removed. The
// board's store is closed by the uninstall hook, so publishing the enrollment fails.
func TestMigrateLegacyService_LocalWriteFailsAfterUninstall_NamesResidualAndRerunConverges(t *testing.T) {
	f := newHostFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(f.root, ".mtix", "secrets"), []byte("checkout-secret"), 0600))
	doomed, err := sqlite.New(filepath.Join(f.root, ".mtix", "data", "mtix.db"), slog.Default())
	require.NoError(t, err)
	var uninstalls int
	failing := true
	enroll := service.NewHostEnrollment(service.HostEnrollmentOptions{State: f.state, Clock: func() time.Time { return f.now },
		OpenBoard: func(context.Context, string) (*service.HostBoard, error) {
			if !failing {
				return f.board(), nil
			}
			return service.NewHostBoard(service.HostBoardOptions{Store: doomed, State: f.state, Path: f.root, Host: service.RegistrationHost{HostID: f.host, UID: "test-uid", Version: "test", Protocol: model.AttachProtocol}, PID: 42, Clock: func() time.Time { return f.now }, Liveness: &scanLiveness{}, Adapters: []service.WakeAdapter{f.adapter},
				Contact: service.NewContactScheduler(func() time.Time { return f.now }, func() (service.ContactInputs, error) {
					return service.ContactInputs{Mode: service.HubContactMode{Kind: service.HubContactInterval, Interval: time.Second}}, nil
				})})
		},
		ObserveHub: func(context.Context, string) (oplocal.HubIdentity, error) { return observedHub() },
		UninstallLegacy: func(context.Context, string) error {
			uninstalls++
			if failing {
				return doomed.Close()
			}
			return nil
		},
		LegacyLive: func(context.Context, string) (bool, error) { return false, nil }})
	request := service.EnrollmentRequest{Path: f.root, Mode: "yes", Source: "test"}

	err = enroll.MigrateLegacyService(t.Context(), request)
	require.Error(t, err)
	require.Contains(t, err.Error(), "re-run the migration", "the operator is told the service is already gone")
	require.Equal(t, 1, uninstalls)
	rows, listErr := enroll.List(t.Context())
	require.NoError(t, listErr)
	require.Empty(t, rows, "a board whose enrollment did not complete stays unenrolled")

	failing = false
	require.NoError(t, enroll.MigrateLegacyService(t.Context(), request), "the re-run completes")
	require.Equal(t, 2, uninstalls)
	rows, listErr = enroll.List(t.Context())
	require.NoError(t, listErr)
	require.Len(t, rows, 1)
}
