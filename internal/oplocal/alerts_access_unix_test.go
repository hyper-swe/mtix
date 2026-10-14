// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package oplocal

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestAlertState_ProtectedDestinations_RefusesBeforePrepare(t *testing.T) {
	for _, name := range []string{"alerts.jsonl", "alerts.1.jsonl", "alerts.2.jsonl", "alerts-pending.json", "alerts.lock", "rate"} {
		for _, kind := range []string{"mode", "symlink"} {
			t.Run(name+"-"+kind, func(t *testing.T) {
				s := alertFixture(t)
				row, err := s.RecordAlert(t.Context(), alertInput(), alertTime())
				require.NoError(t, err)
				dir, err := s.Path()
				require.NoError(t, err)
				target := name
				if name == "rate" {
					target = alertRateName(row.Agent, row.Class)
				}
				path := filepath.Join(dir, target)
				if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
					require.NoError(t, os.WriteFile(path, []byte{}, 0600))
				}
				before, err := os.ReadFile(filepath.Join(dir, "alerts-pending.json"))
				require.NoError(t, err)
				if kind == "mode" {
					require.NoError(t, os.Chmod(path, 0644))
				} else {
					require.NoError(t, os.Rename(path, path+"-original"))
					require.NoError(t, os.Symlink(path+"-original", path))
				}
				_, err = s.RecordAlert(t.Context(), alertInput(), alertTime())
				require.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
				after, err := os.ReadFile(filepath.Join(dir, "alerts-pending.json"))
				require.NoError(t, err)
				require.Equal(t, before, after)
				_, err = s.RecentAlerts(t.Context(), 0)
				require.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
			})
		}
	}
}

func TestAlertRaise_DirectorySyncFailure_PreservesAcceptedPending(t *testing.T) {
	s := alertFixture(t)
	injected := errors.New("injected directory sync failure")
	s.syncDirectory = func(_ *os.File) error { return injected }
	_, err := s.RecordAlert(t.Context(), alertInput(), alertTime())
	require.ErrorIs(t, err, injected)
	restarted := New(s.env)
	dir, err := s.Path()
	require.NoError(t, err)
	before, err := os.ReadDir(dir)
	require.NoError(t, err)
	rows, err := restarted.RecentAlerts(t.Context(), 0)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, uint64(1), rows[0].Count)
	after, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Equal(t, before, after)
	row, err := restarted.RecordAlert(t.Context(), alertInput(), alertTime())
	require.NoError(t, err)
	require.Equal(t, uint64(2), row.Count)
}

func TestAlertLock_ExistingProtectedFile_PreservesInode(t *testing.T) {
	s := alertFixture(t)
	dir, err := s.directory(true)
	require.NoError(t, err)
	defer func() { require.NoError(t, closeFile(dir)) }()
	require.NoError(t, writeData(dir, "alerts.lock", []byte("existing lock bytes"), true, nil, nil))
	path, err := s.Path()
	require.NoError(t, err)
	before, err := os.Stat(filepath.Join(path, "alerts.lock"))
	require.NoError(t, err)
	for range 3 {
		f, openErr := openAlertLock(dir, true)
		require.NoError(t, openErr)
		require.NoError(t, closeFile(f))
	}
	after, err := os.Stat(filepath.Join(path, "alerts.lock"))
	require.NoError(t, err)
	require.True(t, os.SameFile(before, after))
	data, err := readData(dir, "alerts.lock")
	require.NoError(t, err)
	require.Equal(t, []byte("existing lock bytes"), data)
}
