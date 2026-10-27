// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package oplocal_test

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/testutil"
)

func seededWakeBudget(t *testing.T) (*oplocal.State, model.WakeRecipient, string, string) {
	t.Helper()
	state := testutil.OperatorState(t)
	key := model.WakeRecipient{AgentID: "worker", HostID: "fixture-host", UID: "fixture-user"}
	source := "00000000-0000-4000-8000-000000000001"
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	require.NoError(t, state.UpdateWakeBudget(t.Context(), key, func(m *oplocal.WakeBudgetMirror, save func() error) error {
		m.Sources[source] = oplocal.WakeBudgetSource{Total: 1, Committed: 1}
		m.Credits = []oplocal.WakeBudgetCredit{{At: now, Count: 1}}
		m.Unacked, m.LastWakeAt, m.ObservedAt = 1, now, now
		return save()
	}))
	dir, err := state.Path()
	require.NoError(t, err)
	files, err := filepath.Glob(filepath.Join(dir, "wake-budget-*.json"))
	require.NoError(t, err)
	require.Len(t, files, 1)
	return state, key, source, files[0]
}

type invalidWakeBudgetMirror struct {
	name   string
	change func(*oplocal.WakeBudgetMirror, string)
}

func invalidWakeBudgetMirrors() []invalidWakeBudgetMirror {
	return []invalidWakeBudgetMirror{
		{"version", func(m *oplocal.WakeBudgetMirror, _ string) { m.Version = 0 }},
		{"missing sources", func(m *oplocal.WakeBudgetMirror, _ string) { m.Sources = nil }},
		{"missing credits", func(m *oplocal.WakeBudgetMirror, _ string) { m.Credits = nil }},
		{"negative unacked", func(m *oplocal.WakeBudgetMirror, _ string) { m.Unacked = -1 }},
		{"missing last attempt", func(m *oplocal.WakeBudgetMirror, _ string) { m.LastWakeAt = time.Time{} }},
		{"wrong recipient", func(m *oplocal.WakeBudgetMirror, _ string) { m.Recipient.AgentID = "another-worker" }},
		{"invalid recipient", func(m *oplocal.WakeBudgetMirror, _ string) { m.Recipient.UID = "\n" }},
		{"zero credit", func(m *oplocal.WakeBudgetMirror, _ string) { m.Credits[0].Count = 0 }},
		{"zero credit time", func(m *oplocal.WakeBudgetMirror, _ string) { m.Credits[0].At = time.Time{} }},
		{"non UTC time", func(m *oplocal.WakeBudgetMirror, _ string) {
			m.NextWakeAt = m.LastWakeAt.In(time.FixedZone("offset", 3600))
		}},
		{"credit overflow", func(m *oplocal.WakeBudgetMirror, _ string) {
			m.Credits[0].Count = math.MaxInt64
			m.Credits = append(m.Credits, oplocal.WakeBudgetCredit{At: m.LastWakeAt, Count: 1})
		}},
		{"source id", func(m *oplocal.WakeBudgetMirror, _ string) { m.Sources["not-a-source"] = oplocal.WakeBudgetSource{} }},
		{"negative total", func(m *oplocal.WakeBudgetMirror, s string) { r := m.Sources[s]; r.Total = -1; m.Sources[s] = r }},
		{"committed exceeds floor", func(m *oplocal.WakeBudgetMirror, s string) { r := m.Sources[s]; r.Committed = 2; m.Sources[s] = r }},
		{"negative ack", func(m *oplocal.WakeBudgetMirror, s string) { r := m.Sources[s]; r.AckMark = -1; m.Sources[s] = r }},
		{"pending already committed", func(m *oplocal.WakeBudgetMirror, s string) {
			r := m.Sources[s]
			r.Pending = "pending"
			m.Sources[s] = r
		}},
		{"missing pending", func(m *oplocal.WakeBudgetMirror, s string) { r := m.Sources[s]; r.Total = 2; m.Sources[s] = r }},
	}
}

func TestWakeBudgetMirror_MalformedRecord_NeverAdoptedOrOverwritten(t *testing.T) {
	for _, tc := range invalidWakeBudgetMirrors() {
		t.Run(tc.name, func(t *testing.T) {
			state, key, source, file := seededWakeBudget(t)
			mirror, present, err := state.ReadWakeBudget(key)
			require.NoError(t, err)
			require.True(t, present)
			tc.change(&mirror, source)
			name := strings.TrimSuffix(filepath.Base(file), ".json")
			require.NoError(t, state.WriteJSON(name, mirror))
			before, err := os.ReadFile(file)
			require.NoError(t, err)
			_, _, err = state.ReadWakeBudget(key)
			require.Error(t, err)
			_, err = state.WakeBudgetMirrors(t.Context())
			require.Error(t, err)
			err = state.UpdateWakeBudget(t.Context(), key, func(*oplocal.WakeBudgetMirror, func() error) error {
				t.Error("invalid state must never reach a writer")
				return nil
			})
			require.Error(t, err)
			after, err := os.ReadFile(file)
			require.NoError(t, err)
			require.Equal(t, before, after)
		})
	}
}

func TestWakeBudgetMirror_AbsentAndCancelled_DoNotCreateState(t *testing.T) {
	state := testutil.OperatorState(t)
	key := model.WakeRecipient{AgentID: "worker", HostID: "host", UID: "user"}
	_, present, err := state.ReadWakeBudget(key)
	require.NoError(t, err)
	require.False(t, present)
	rows, err := state.WakeBudgetMirrors(t.Context())
	require.NoError(t, err)
	require.Empty(t, rows)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, state.UpdateWakeBudget(ctx, key, func(*oplocal.WakeBudgetMirror, func() error) error { t.Error("cancelled update ran"); return nil }), context.Canceled)
	_, err = state.WakeBudgetMirrors(ctx)
	require.ErrorIs(t, err, context.Canceled)
	dir, err := state.Path()
	require.NoError(t, err)
	_, err = os.Stat(dir)
	require.ErrorIs(t, err, os.ErrNotExist)
}
