// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
)

var guardNow = time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

func guardSeen(ago time.Duration) string { return guardNow.Add(-ago).Format(time.RFC3339) }

func TestEvaluateHistoryExportGuard_Signals_BlockOrClear(t *testing.T) {
	marker := store.HistoryReaderCapability
	tests := []struct {
		name        string
		signals     []GuardSignal
		wantBlocked []string // sources
	}{
		{"no signals", nil, nil},
		{"registration without the marker", []GuardSignal{{Source: "registration", Name: "a1", Version: "0.5.4-beta", SeenAt: guardSeen(time.Hour)}}, []string{"registration"}},
		{"daemon without the marker", []GuardSignal{{Source: "daemon", Name: "h:1", Version: "v0.5.2", SeenAt: guardSeen(time.Hour)}}, []string{"daemon"}},
		{"marker present clears whatever the version string says", []GuardSignal{{Source: "registration", Name: "a1", Version: "v0.5.4-beta-312-g7ed998a8-dirty", Capabilities: marker, SeenAt: guardSeen(time.Hour)}}, nil},
		{"marker among other capabilities", []GuardSignal{{Source: "daemon", Name: "h:1", Version: "dev", Capabilities: "x," + marker + ",y", SeenAt: guardSeen(time.Hour)}}, nil},
		{"a newer version without the marker still blocks", []GuardSignal{{Source: "registration", Name: "a1", Version: "0.9.0", SeenAt: guardSeen(time.Hour)}}, []string{"registration"}},
		{"development build without the marker blocks", []GuardSignal{{Source: "registration", Name: "a1", Version: "dev", SeenAt: guardSeen(time.Hour)}}, []string{"registration"}},
		{"marker named as a prefix only does not count", []GuardSignal{{Source: "registration", Name: "a1", Capabilities: marker + "_v2", SeenAt: guardSeen(time.Hour)}}, []string{"registration"}},
		{"unreadable seen time counts as recent", []GuardSignal{{Source: "registration", Name: "a1", SeenAt: "never"}}, []string{"registration"}},
		{"exactly at the window edge still blocks", []GuardSignal{{Source: "registration", Name: "a1", SeenAt: guardSeen(HistoryGuardWindow)}}, []string{"registration"}},
		{"one second past the window edge no longer blocks", []GuardSignal{{Source: "registration", Name: "a1", SeenAt: guardSeen(HistoryGuardWindow + time.Second)}}, nil},
		{"two unmarked signals", []GuardSignal{
			{Source: "registration", Name: "a1", SeenAt: guardSeen(time.Hour)},
			{Source: "daemon", Name: "h:1", SeenAt: guardSeen(time.Hour)}}, []string{"registration", "daemon"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rep := EvaluateHistoryExportGuard(tt.signals, guardNow)
			var got []string
			for _, b := range rep.Blockers {
				got = append(got, b.Source)
			}
			assert.Equal(t, tt.wantBlocked, got)
			assert.Equal(t, len(tt.wantBlocked) == 0, rep.Clear())
			assert.Equal(t, FirstHistoryVersion, rep.MinVersion)
		})
	}
}

func TestHistoryGuardReport_Refusal_NamesMinimumVersionReaderBehaviourAndFix(t *testing.T) {
	rep := EvaluateHistoryExportGuard([]GuardSignal{
		{Source: "registration", Name: "worker", Version: "0.5.4-beta", SeenAt: guardSeen(time.Hour)}}, guardNow)
	msg := rep.Refusal()
	assert.Contains(t, msg, FirstHistoryVersion, "names the minimum version")
	assert.Contains(t, msg, "checksum verification failed", "says what a released reader does")
	assert.Contains(t, msg, "worker")
	assert.Contains(t, msg, "0.5.4-beta")
	assert.Contains(t, msg, "upgrade", "gives the fix")
	assert.Contains(t, msg, "mtix doctor")
	assert.Contains(t, HistoryGuardBlindSpot, "git", "the blind spot is stated")
}

func guardConfig(t *testing.T, rep HistoryGuardReport, err error) (*ConfigService, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	cs, nerr := NewConfigService(path)
	require.NoError(t, nerr)
	cs.SetHistoryExportGuard(func() (HistoryGuardReport, error) { return rep, err })
	return cs, path
}

func TestConfigSet_HistoryExportOn_RefusedWhileOlderReaderDetected_WritesNothing(t *testing.T) {
	blocked := EvaluateHistoryExportGuard([]GuardSignal{
		{Source: "daemon", Name: "h:1", Version: "0.5.4-beta", SeenAt: guardSeen(time.Hour)}}, guardNow)
	cs, path := guardConfig(t, blocked, nil)
	_, err := cs.Set("history.export", "true")
	require.Error(t, err)
	assert.True(t, errors.Is(err, model.ErrInvalidInput))
	assert.Contains(t, err.Error(), FirstHistoryVersion)
	assert.False(t, cs.HistoryExport())
	_, statErr := os.Stat(path)
	assert.True(t, errors.Is(statErr, os.ErrNotExist), "nothing is written when refused")
}

func TestConfigSet_HistoryExport_GuardAppliesOnlyToTurningOn(t *testing.T) {
	blocked := EvaluateHistoryExportGuard([]GuardSignal{{Source: "registration", Name: "a", SeenAt: guardSeen(time.Hour)}}, guardNow)
	cs, _ := guardConfig(t, blocked, nil)
	for _, v := range []string{"false", "FALSE", "0"} {
		_, err := cs.Set("history.export", v)
		require.NoError(t, err, v)
	}
	_, err := cs.Set("history.max_entries_per_task", "100")
	require.NoError(t, err, "other keys are not guarded")
}

func TestConfigSet_HistoryExportOn_AllowedWhenClear_AndWhenAlreadyOn(t *testing.T) {
	cs, _ := guardConfig(t, EvaluateHistoryExportGuard(nil, guardNow), nil)
	_, err := cs.Set("history.export", "true")
	require.NoError(t, err)
	assert.True(t, cs.HistoryExport())
	cs.SetHistoryExportGuard(func() (HistoryGuardReport, error) {
		return EvaluateHistoryExportGuard([]GuardSignal{{Source: "registration", Name: "a", SeenAt: guardSeen(time.Hour)}}, guardNow), nil
	})
	_, err = cs.Set("history.export", "true")
	require.NoError(t, err, "re-asserting an already enabled switch is not a new enabling")
}

func TestConfigSet_HistoryExportOn_GuardUnreadable_FailsClosed(t *testing.T) {
	cs, path := guardConfig(t, HistoryGuardReport{}, errors.New("database locked"))
	_, err := cs.Set("history.export", "true")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "database locked")
	assert.Contains(t, err.Error(), "mtix doctor")
	_, statErr := os.Stat(path)
	assert.True(t, errors.Is(statErr, os.ErrNotExist))
}

func TestConfigSet_HistoryExportOn_NoGuardInstalled_Allowed(t *testing.T) {
	cs, err := NewConfigService(filepath.Join(t.TempDir(), "config.yaml"))
	require.NoError(t, err)
	_, err = cs.Set("history.export", "true")
	require.NoError(t, err)
}

type fakeSignalSource struct {
	signals []store.BinarySignal
	err     error
}

func (f fakeSignalSource) BinarySignals(context.Context) ([]store.BinarySignal, error) {
	return f.signals, f.err
}

func TestHistoryExportGuardService_Evaluate_MapsStoreSignalsAndUsesInjectedClock(t *testing.T) {
	src := fakeSignalSource{signals: []store.BinarySignal{
		{Source: "registration", Name: "old", Version: "0.5.4-beta", SeenAt: guardSeen(time.Hour)},
		{Source: "daemon", Name: "h:1", Version: "dev", Capabilities: store.HistoryReaderCapability, SeenAt: guardSeen(time.Hour)},
		{Source: "registration", Name: "stale", SeenAt: guardSeen(HistoryGuardWindow + time.Hour)},
	}}
	rep, err := NewHistoryExportGuardService(src, func() time.Time { return guardNow }).Evaluate(t.Context())
	require.NoError(t, err)
	require.Len(t, rep.Blockers, 1)
	assert.Equal(t, "old", rep.Blockers[0].Name)
	late, err := NewHistoryExportGuardService(src, func() time.Time { return guardNow.Add(HistoryGuardWindow) }).Evaluate(t.Context())
	require.NoError(t, err)
	assert.Empty(t, late.Blockers, "the injected clock decides which signals are recent")
}

func TestHistoryExportGuardService_Evaluate_SourceErrorOrMissing_ReturnsError(t *testing.T) {
	_, err := NewHistoryExportGuardService(fakeSignalSource{err: errors.New("locked")}, func() time.Time { return guardNow }).Evaluate(t.Context())
	require.ErrorContains(t, err, "locked")
	var nilSvc *HistoryExportGuardService
	_, err = nilSvc.Evaluate(t.Context())
	require.Error(t, err)
	_, err = NewHistoryExportGuardService(nil, nil).Evaluate(t.Context())
	require.Error(t, err)
}
