// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"errors"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/testutil"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
	"time"
)

type fixedHopStore struct {
	*sqlite.Store
	hop int64
}

func (s fixedHopStore) WakeEventHop(context.Context, model.WakeKey) (int64, error) { return s.hop, nil }

func TestWakeHopGuard_AgentOverrideAndHeldDiagnostic(t *testing.T) {
	state := testutil.OperatorState(t)
	_, err := wakeConfig(t, state, "", true).Set("wake.max_hop_depth", "5")
	require.NoError(t, err)
	_, err = wakeConfig(t, state, "worker", true).Set("wake.max_hop_depth", "2")
	require.NoError(t, err)
	for _, agent := range []string{"worker", "other"} {
		t.Run(agent, func(t *testing.T) {
			store, _, alerts := wakeServiceStore(t)
			var lines []string
			sent := 0
			d, err := service.NewWakeDispatcher(service.WakeDispatcherOptions{Store: fixedHopStore{store, 3}, Alerts: alerts, Settings: state, BudgetState: state, Clock: time.Now, Held: func(line string) { lines = append(lines, line) }, Send: func(_ context.Context, attempt model.WakeAttempt) error {
				require.Equal(t, int64(3), attempt.Hop)
				sent++
				return nil
			}})
			require.NoError(t, err)
			key := wakeServiceKey()
			key.AgentID = agent
			require.NoError(t, d.Pass(t.Context(), []model.WakeKey{key}))
			if agent == "worker" {
				require.Zero(t, sent)
				require.Contains(t, strings.Join(lines, "\n"), "held wake: hop depth 3 exceeds 2")
				require.Contains(t, strings.Join(lines, "\n"), "mtix config set wake.max_hop_depth 3 --agent worker")
			} else {
				require.Equal(t, 1, sent)
				require.Empty(t, lines)
			}
		})
	}
}

type faultHopPolicy struct {
	values  map[string]model.WakeSetting
	reports []oplocal.Report
	err     error
}

func (p faultHopPolicy) WakeSettings(string) (map[string]model.WakeSetting, []oplocal.Report, error) {
	return p.values, p.reports, p.err
}

type faultHopStore struct {
	*sqlite.Store
	err error
}

func (s faultHopStore) WakeEventHop(context.Context, model.WakeKey) (int64, error) { return 0, s.err }

func TestWakeHopGuard_UnreadableInvalidOrUnrecordable_ClaimsNothing(t *testing.T) {
	cause := errors.New("wake dependency unavailable")
	for _, name := range []string{"event read", "negative hop", "policy read", "foreign host", "zero guard", "invalid guard", "alert write"} {
		t.Run(name, func(t *testing.T) {
			store, _, alerts := wakeServiceStore(t)
			var backend service.WakeStore = fixedHopStore{store, 33}
			options := service.WakeDispatcherOptions{Store: backend, Alerts: alerts, BudgetState: testutil.OperatorState(t), Clock: time.Now, Send: func(context.Context, model.WakeAttempt) error { t.Error("must not send"); return nil }}
			switch name {
			case "event read":
				options.Store = faultHopStore{store, cause}
			case "negative hop":
				options.Store = fixedHopStore{store, -1}
			case "policy read":
				options.Settings = faultHopPolicy{err: cause}
			case "foreign host":
				options.Settings = faultHopPolicy{reports: []oplocal.Report{{Name: "wake", Reason: "foreign"}}}
			case "zero guard":
				options.Settings = faultHopPolicy{values: map[string]model.WakeSetting{"wake.max_hop_depth": {Value: "0"}}}
			case "invalid guard":
				options.Settings = faultHopPolicy{values: map[string]model.WakeSetting{"wake.max_hop_depth": {Value: "bad"}}}
			case "alert write":
				options.Alerts = wakeAlertFault{cause}
			}
			d, err := service.NewWakeDispatcher(options)
			require.NoError(t, err)
			require.Error(t, d.Pass(t.Context(), []model.WakeKey{wakeServiceKey()}))
			var count int
			require.NoError(t, store.ReadDB().QueryRowContext(t.Context(), "SELECT count(*) FROM wake_ledger").Scan(&count))
			require.Zero(t, count)
		})
	}
}
