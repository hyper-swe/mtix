// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hooks

import (
	"os"
	"path/filepath"
	"sync"
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
			a.readLegacy = func(string) legacyRecord { opened++; return legacyRecord{} }
			require.NoError(t, a.Migrate())
			require.Equal(t, map[bool]int{true: 0, false: 1}[tracked], opened)
		})
	}
}

func TestApprovals_LegacyRecordChangedAfterFirstRun_IsNotMigrated(t *testing.T) {
	state := testutil.OperatorState(t)
	starter, _ := setupExecHook(t, "#!/bin/sh\nexit 0\n")
	_, err := NewApprovals(starter, state, false).firstRun()
	require.NoError(t, err)
	time.Sleep(30 * time.Millisecond)
	for _, name := range []string{"written later", "extracted with old times"} {
		t.Run(name, func(t *testing.T) {
			dir, _ := setupExecHook(t, "#!/bin/sh\nexit 0\n")
			record := filepath.Join(dir, "hooks.trust")
			require.NoError(t, os.WriteFile(record, []byte(ConfigHash(dir)+"\n"), 0600))
			if name != "written later" {
				old := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
				require.NoError(t, os.Chtimes(record, old, old))
			}
			b := NewApprovals(dir, state, false)
			require.NoError(t, b.Migrate())
			decisions, err := b.Check()
			require.NoError(t, err)
			require.False(t, decisions["wake"].Trusted)
		})
	}
}

func TestApprovals_LegacyRecordBeforeFirstRun_IsMigrated(t *testing.T) {
	dir, _ := setupExecHook(t, "#!/bin/sh\nexit 0\n")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hooks.trust"), []byte(ConfigHash(dir)+"\n"), 0600))
	time.Sleep(30 * time.Millisecond)
	a := NewApprovals(dir, testutil.OperatorState(t), false)
	require.NoError(t, a.Migrate())
	decisions, err := a.Check()
	require.NoError(t, err)
	require.True(t, decisions["wake"].Trusted)
}

func TestApprovals_LegacyChangeTimeUnreadable_IsNotMigrated(t *testing.T) {
	dir, _ := setupExecHook(t, "#!/bin/sh\nexit 0\n")
	state := testutil.OperatorState(t)
	a := NewApprovals(dir, state, false)
	a.readLegacy = func(string) legacyRecord {
		return legacyRecord{Refusal: "legacy workspace record change time cannot be read"}
	}
	require.NoError(t, a.Migrate())
	var rec migrationRecord
	project, err := projectIdentity(dir)
	require.NoError(t, err)
	_, err = state.ReadJSON(recordKey("migration-", project, ""), &rec)
	require.NoError(t, err)
	require.Contains(t, rec.Detail, "change time cannot be read")
	require.Empty(t, rec.Pins)
	decisions, err := a.Check()
	require.NoError(t, err)
	require.False(t, decisions["wake"].Trusted)
}

func TestRecordFirstRun_NoApprovalInput_RecordsOnceForHost(t *testing.T) {
	state := testutil.OperatorState(t)
	require.NoError(t, RecordFirstRun())
	reader := &Approvals{state: state, clock: time.Now}
	first, err := reader.firstRun()
	require.NoError(t, err)
	time.Sleep(20 * time.Millisecond)
	require.NoError(t, RecordFirstRun())
	again, err := reader.firstRun()
	require.NoError(t, err)
	require.True(t, first.Equal(again))
}

func TestApprovals_FirstRun_ConcurrentWritersAgree(t *testing.T) {
	state := testutil.OperatorState(t)
	dir := filepath.Join(t.TempDir(), ".mtix")
	require.NoError(t, os.Mkdir(dir, 0700))
	const writers = 8
	results := make(chan time.Time, writers)
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(offset int) {
			defer wg.Done()
			a := NewApprovals(dir, state, false)
			a.clock = func() time.Time { return time.Unix(int64(1_700_000_000+offset), 0) }
			at, err := a.firstRun()
			if err == nil {
				results <- at
			}
		}(i)
	}
	wg.Wait()
	close(results)
	var got []time.Time
	for at := range results {
		got = append(got, at)
	}
	require.Len(t, got, writers)
	for _, at := range got {
		require.True(t, got[0].Equal(at))
	}
}
