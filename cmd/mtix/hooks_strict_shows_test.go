// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/hooks"
)

func TestHooksTrustProgram_ShowsTheResolvedProgramBeforeApproving(t *testing.T) {
	state := reviewProject(t)
	approvals := hooks.NewApprovals(app.mtixDir, state, false)
	var notice string
	captureStdout(t, func() {
		notice = captureStderr(t, func() { require.NoError(t, runHooksTrustProgram(approvals, "runner", false, true)) })
	})
	require.Contains(t, notice, "Program to approve for hook \"runner\"")
	require.Contains(t, notice, "find")
}

// recordingWriter runs check at the moment the notice is written.
type recordingWriter struct{ check func() }

func (w recordingWriter) Write(p []byte) (int, error) {
	w.check()
	return len(p), nil
}

func TestHooksTrustProgram_NoticeIsWrittenBeforeTheApproval(t *testing.T) {
	state := reviewProject(t)
	approvals := hooks.NewApprovals(app.mtixDir, state, false)
	cfg, _ := hooks.Load(app.mtixDir)
	var selected hooks.Hook
	for _, h := range cfg.Hooks {
		if h.Name == "runner" {
			selected = h
		}
	}
	approvedAtNotice := true
	writer := recordingWriter{check: func() {
		st := approvals.StrictStatus(hooks.Config{Hooks: []hooks.Hook{selected}})
		approvedAtNotice = len(st.Programs) == 1 && st.Programs[0].Approved
	}}
	captureStdout(t, func() { require.NoError(t, runHooksTrustProgramTo(writer, approvals, "runner", false, true)) })
	require.False(t, approvedAtNotice, "nothing is approved yet when the notice is written")
	st := approvals.StrictStatus(hooks.Config{Hooks: []hooks.Hook{selected}})
	require.True(t, st.Programs[0].Approved, "and the approval is written afterwards")
}
