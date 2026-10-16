//go:build !windows

// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"errors"
	"os"
	"testing"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/stretchr/testify/require"
)

func TestRegisterSession_TakeOverQueueFailure_StillAttemptsNotice(t *testing.T) {
	f, s, _ := identityFixture(t)
	_, err := f.svc.RegisterSession(t.Context(), req("lane-a", 1))
	require.NoError(t, err)
	f.live[1] = service.LiveAlive
	dir, err := s.Path()
	require.NoError(t, err)
	require.NoError(t, os.Chmod(dir, 0500))
	t.Cleanup(func() { require.NoError(t, os.Chmod(dir, 0700)) })
	cause := errors.New("enqueue unavailable")
	f.svc.Store = &pendingFaultStore{RegistrationStore: f.store, pending: f.store, enqueueErr: cause}
	request := req("lane-a", 2)
	request.TakeOver = true
	result, err := f.svc.RegisterSession(t.Context(), request)
	require.ErrorIs(t, err, cause)
	require.NotNil(t, result)
	require.Equal(t, service.OutcomeTakenOver, result.Outcome)
	require.NotNil(t, result.Registration)
	require.Equal(t, int64(2), result.Registration.PID)
	require.True(t, result.NoticeSent)
	require.Equal(t, []string{"lane-a"}, f.notice.sent)
	require.NotEmpty(t, f.log.recs)
	require.True(t, f.log.recs[len(f.log.recs)-1].NoticeAttempted)
}
