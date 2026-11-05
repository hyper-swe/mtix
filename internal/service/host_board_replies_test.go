// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// scanSpy counts reply scans on top of the real journal.
type scanSpy struct {
	*sqlite.Store
	scans int
}

func (s *scanSpy) ReplyByEvents(ctx context.Context, after int64, limit int) ([]sqlite.ReplyByEvent, error) {
	s.scans++
	return s.Store.ReplyByEvents(ctx, after, limit)
}

func (f *hostFixture) repliesDispatcher(kind service.HubContactKind, spy *scanSpy) *service.HostDispatcher {
	f.t.Helper()
	d, err := service.NewHostDispatcher(service.HostDispatcherOptions{State: f.state, OpenBoard: func(context.Context, oplocal.ListedBoard) (*service.HostBoard, error) {
		return service.NewHostBoard(service.HostBoardOptions{Store: f.store, State: f.state, Path: f.root,
			Host: service.RegistrationHost{HostID: f.host, UID: "test-uid", Version: "test", Protocol: model.AttachProtocol},
			PID:  42, Clock: func() time.Time { return f.now }, Liveness: &scanLiveness{}, Adapters: []service.WakeAdapter{f.adapter},
			Contact: service.NewContactScheduler(func() time.Time { return f.now }, func() (service.ContactInputs, error) {
				return service.ContactInputs{Mode: service.HubContactMode{Kind: kind, Interval: time.Second}}, nil
			}),
			Replies: &service.ReplyProcessor{Journal: spy, State: f.state, Key: replyBoardKey, Host: service.RegistrationHost{HostID: f.host, UID: "test-uid"},
				Settings: f.state, Clock: func() time.Time { return f.now }},
			Pull: func(context.Context, string) error { f.dials++; return nil }, Hooks: func(context.Context) { f.hooks++ }})
	}})
	require.NoError(f.t, err)
	return d
}

func TestHostBoard_Replies_ScanRunsAfterAPullOnly(t *testing.T) {
	for name, tt := range map[string]struct {
		kind  service.HubContactKind
		scans int
	}{
		"a pull happened":          {service.HubContactInterval, 1},
		"manual: no pull, no scan": {service.HubContactManual, 0},
	} {
		t.Run(name, func(t *testing.T) {
			f := newHostFixture(t)
			require.NoError(t, f.enrollForLegacy().Enroll(t.Context(), service.EnrollmentRequest{Path: f.root, Mode: "yes", Source: "test"}))
			spy := &scanSpy{Store: f.store}
			require.NoError(t, f.repliesDispatcher(tt.kind, spy).Pass(t.Context()))
			require.Equal(t, tt.scans, spy.scans)
			require.Equal(t, tt.scans, f.dials)
		})
	}
}
