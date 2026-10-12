// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package procinfo_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/procinfo"
)

type table map[int]procinfo.Proc

func (t table) read(pid int) (procinfo.Proc, error) {
	p, ok := t[pid]
	if !ok {
		return procinfo.Proc{}, procinfo.ErrNoProcess
	}
	return p, nil
}

func TestFindSessionProcess_SkipsShellsAndWrappers(t *testing.T) {
	tests := []struct {
		name      string
		procs     table
		start     int
		wantPID   int
		wantStart string
		wantSure  bool
	}{
		{"harness is the parent", table{50: {PPID: 10, Name: "claude", StartTime: "s50"}}, 50, 50, "s50", true},
		{"shell then harness", table{60: {PPID: 50, Name: "zsh", StartTime: "s60"}, 50: {PPID: 10, Name: "node", StartTime: "s50"}}, 60, 50, "s50", true},
		{"login shell dash prefix", table{60: {PPID: 50, Name: "-bash", StartTime: "s60"}, 50: {PPID: 10, Name: "codex", StartTime: "s50"}}, 60, 50, "s50", true},
		{"nested shell and wrapper chain", table{
			90: {PPID: 80, Name: "sh", StartTime: "a"}, 80: {PPID: 70, Name: "timeout", StartTime: "b"},
			70: {PPID: 60, Name: "bash", StartTime: "c"}, 60: {PPID: 50, Name: "env", StartTime: "d"},
			50: {PPID: 10, Name: "pi", StartTime: "s50"}}, 90, 50, "s50", true},
		{"path in name", table{60: {PPID: 50, Name: "/bin/dash", StartTime: "x"}, 50: {PPID: 10, Name: "claude", StartTime: "s50"}}, 60, 50, "s50", true},
		{"reaches pid 1 is unsure", table{60: {PPID: 1, Name: "bash", StartTime: "x"}}, 60, 60, "", false},
		{"read error is unsure", table{60: {PPID: 50, Name: "bash", StartTime: "x"}}, 60, 60, "", false},
		{"unreadable start of non-shell is unsure", table{50: {PPID: 10, Name: "claude"}}, 50, 50, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pid, start, sure := procinfo.FindSessionProcess(tt.procs.read, tt.start)
			assert.Equal(t, tt.wantPID, pid)
			assert.Equal(t, tt.wantStart, start)
			assert.Equal(t, tt.wantSure, sure)
		})
	}
}

func TestFindSessionProcess_DepthBound_IsUnsure(t *testing.T) {
	procs := table{}
	for pid := 100; pid < 120; pid++ {
		procs[pid] = procinfo.Proc{PPID: pid + 1, Name: "bash", StartTime: "x"}
	}
	pid, start, sure := procinfo.FindSessionProcess(procs.read, 100)
	assert.False(t, sure)
	assert.Equal(t, 100, pid, "an unsure walk falls back to the immediate parent with no start time")
	assert.Empty(t, start)
}

func TestFindSessionProcess_ReaderErrorOtherThanMissing_IsUnsure(t *testing.T) {
	_, _, sure := procinfo.FindSessionProcess(func(int) (procinfo.Proc, error) { return procinfo.Proc{}, errors.New("denied") }, 5)
	assert.False(t, sure)
}

func TestInfo_OwnProcess_HasParentAndName(t *testing.T) {
	p, err := procinfo.Info(procinfoSelf())
	if errors.Is(err, procinfo.ErrUnsupported) {
		t.Skip("unsupported platform")
	}
	require.NoError(t, err)
	assert.Positive(t, p.PPID)
	assert.NotEmpty(t, p.Name)
	assert.NotEmpty(t, p.StartTime)
}
