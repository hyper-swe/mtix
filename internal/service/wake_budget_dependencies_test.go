// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

func TestWakeBudget_MissingProtectedState_RefusesConstructor(t *testing.T) {
	f := newWakeBudgetFixture(t, "12")
	b := f.board(t, 1)
	_, err := service.NewWakeDispatcher(service.WakeDispatcherOptions{
		Store: b.store, Alerts: f.alerts, Send: func(context.Context, model.WakeAttempt) error {
			t.Error("missing protected accounting state must never send")
			return nil
		},
	})
	require.ErrorIs(t, err, model.ErrInvalidInput)
}
