// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hooks

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestApprovals_LegacyRecordViaOutsideLink_RemainsTracked(t *testing.T) {
	for _, via := range []string{"direct", "link", "link in other repository"} {
		t.Run(via, func(t *testing.T) {
			dir, _ := setupExecHook(t, "#!/bin/sh\nexit 0\n")
			repo := t.TempDir()
			require.NoError(t, os.Mkdir(filepath.Join(repo, ".git"), 0700))
			index := approvalIndexFixture(2, "sub/.mtix/hooks.trust", "")
			require.NoError(t, os.WriteFile(filepath.Join(repo, ".git", "index"), index, 0600))
			sub := filepath.Join(repo, "sub")
			require.NoError(t, os.Rename(filepath.Dir(dir), sub))
			dir = filepath.Join(sub, ".mtix")
			if via != "direct" {
				parent := t.TempDir()
				if via == "link in other repository" {
					require.NoError(t, os.Mkdir(filepath.Join(parent, ".git"), 0700))
					require.NoError(t, os.WriteFile(filepath.Join(parent, ".git", "index"), approvalIndexFixture(2, "other", ""), 0600))
				}
				link := filepath.Join(parent, "project")
				if err := os.Symlink(sub, link); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
				dir = filepath.Join(link, ".mtix")
			}
			require.NoError(t, os.WriteFile(filepath.Join(dir, "hooks.trust"), []byte(ConfigHash(dir)+"\n"), 0600))
			a := NewApprovals(dir, testutil.OperatorState(t), false)
			require.NoError(t, a.Migrate())
			decisions, err := a.Check()
			require.NoError(t, err)
			require.False(t, decisions["wake"].Trusted)
		})
	}
}

func TestApprovals_LegacyProvenance_DecidedBeforeRecordOpened(t *testing.T) {
	for _, tracked := range []bool{true, false} {
		name := "untracked"
		if tracked {
			name = "tracked"
		}
		t.Run(name, func(t *testing.T) {
			dir, _ := setupExecHook(t, "#!/bin/sh\nexit 0\n")
			root := filepath.Dir(dir)
			require.NoError(t, os.Mkdir(filepath.Join(root, ".git"), 0700))
			entry := "other"
			if tracked {
				entry = ".mtix/hooks.trust"
			}
			require.NoError(t, os.WriteFile(filepath.Join(root, ".git", "index"), approvalIndexFixture(2, entry, ""), 0600))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "hooks.trust"), []byte(ConfigHash(dir)+"\n"), 0600))
			a := NewApprovals(dir, testutil.OperatorState(t), false)
			opened := 0
			a.readLegacy = func(string) string { opened++; return "" }
			require.NoError(t, a.Migrate())
			require.Equal(t, map[bool]int{true: 0, false: 1}[tracked], opened)
		})
	}
}

func TestApprovals_LegacyRecordAfterFirstRun_IsNotMigrated(t *testing.T) {
	first := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	state := testutil.OperatorState(t)
	starter, _ := setupExecHook(t, "#!/bin/sh\nexit 0\n")
	a := NewApprovals(starter, state, false)
	a.clock = func() time.Time { return first }
	require.NoError(t, a.Migrate())
	for name, offset := range map[string]time.Duration{"before": -time.Hour, "after": time.Hour} {
		t.Run(name, func(t *testing.T) {
			dir, _ := setupExecHook(t, "#!/bin/sh\nexit 0\n")
			record := filepath.Join(dir, "hooks.trust")
			require.NoError(t, os.WriteFile(record, []byte(ConfigHash(dir)+"\n"), 0600))
			require.NoError(t, os.Chtimes(record, first.Add(offset), first.Add(offset)))
			b := NewApprovals(dir, state, false)
			b.clock = func() time.Time { return first.Add(2 * time.Hour) }
			require.NoError(t, b.Migrate())
			decisions, err := b.Check()
			require.NoError(t, err)
			require.Equal(t, offset < 0, decisions["wake"].Trusted)
		})
	}
}
