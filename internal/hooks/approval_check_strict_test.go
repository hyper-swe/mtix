// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hooks

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/testutil"
)

// TestCheck_UnreadableConfig_StrictOnlyErrors pins MTIX-282: hooks status keeps
// the lenient Check (warnings come from Load), while the trust listing uses
// CheckStrict and must never read a broken config as an empty trust list.
func TestCheck_UnreadableConfig_StrictOnlyErrors(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(t *testing.T, path string)
		wantErr string
	}{
		{"absent config", func(*testing.T, string) {}, ""},
		{"malformed config", func(t *testing.T, path string) {
			require.NoError(t, os.WriteFile(path, []byte("hooks: ["), 0o600))
		}, "parse hooks.yaml"},
		{"unreadable config", func(t *testing.T, path string) {
			require.NoError(t, os.Mkdir(path, 0o700))
		}, "read hooks"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			tt.prepare(t, filepath.Join(dir, "hooks.yaml"))
			approvals := NewApprovals(dir, testutil.OperatorState(t), false)

			decisions, err := approvals.Check()
			require.NoError(t, err)
			require.Empty(t, decisions)

			decisions, err = approvals.CheckStrict()
			require.Empty(t, decisions)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

// TestCheckStrict_InvalidAndDuplicateStanzas_AreNotListed pins that the listing
// is built from the validated config: a stanza the dispatcher drops (invalid or
// duplicate) is never listed, and cannot replace the approved stanza's digest.
func TestCheckStrict_InvalidAndDuplicateStanzas_AreNotListed(t *testing.T) {
	dir := t.TempDir()
	const config = `hooks:
  - name: good
    match: {events: [comment.addressed]}
    deliver: [exec]
    exec: {command: [sh, wake.sh]}
  - name: good
    match: {events: [comment.addressed]}
    deliver: [exec]
    exec: {command: [sh, other.sh]}
  - name: noevents
    match: {events: []}
    deliver: [exec]
    exec: {command: [sh, wake.sh]}
  - name: badevent
    match: {events: [not.an.event]}
    deliver: [exec]
    exec: {command: [sh, wake.sh]}
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hooks.yaml"), []byte(config), 0o600))
	approvals := NewApprovals(dir, testutil.OperatorState(t), false)
	require.NoError(t, approvals.Approve("good", true))

	decisions, err := approvals.CheckStrict()
	require.NoError(t, err)
	require.Len(t, decisions, 1, "only the validated first stanza is listed: %v", decisions)
	require.True(t, decisions["good"].Trusted)
	require.NotContains(t, decisions, "noevents")
	require.NotContains(t, decisions, "badevent")
}
