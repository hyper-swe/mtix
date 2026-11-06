// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
)

// dialsWhileIdle counts the passes of ten minutes on an otherwise idle board
// that the real scheduler inputs allow to contact the hub.
func dialsWhileIdle(t *testing.T) int {
	t.Helper()
	src, err := newHubContactSource(app.mtixDir)
	require.NoError(t, err)
	now := utcNow()
	sched := service.NewContactScheduler(func() time.Time { return now }, func() (service.ContactInputs, error) {
		return service.ContactInputs{Mode: service.HubContactMode{Kind: service.HubContactActivity},
			LastActivity: now.Add(-24 * time.Hour), Reply: src.replyInputs()}, nil
	})
	dials := 0
	for end := now.Add(10 * time.Minute); now.Before(end); now = now.Add(5 * time.Second) {
		if sched.Decide().Contact {
			dials++
			sched.MarkContact()
		}
	}
	return dials
}

// An addressee registered on this host needs no hub contact, so the sender's
// window must not keep 60 s hub pulls (a scale-to-zero hub stays asleep).
func TestRunComment_AwaitReply_AddresseeOnThisHost_KeepsNoHubPulls(t *testing.T) {
	tests := []struct {
		name      string
		register  string
		to        string
		wantOpen  int
		wantDials func(int) bool
	}{
		{"addressee registered here opens no window", "local-agent", "local-agent", 0, func(d int) bool { return d == 0 }},
		{"addressee elsewhere opens a window and pulls every 60 s", "local-agent", "far-agent", 1, func(d int) bool { return d >= 9 && d <= 11 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initRegistrationApp(t)
			id := seedOneNode(t)
			require.NoError(t, runAgentRegister(tt.register, sessionOptions{}))
			out := captureStdout(t, func() {
				require.NoError(t, runCommentWith(id, "need an answer", commentOptions{To: tt.to, Await: "30m", AwaitSet: true}))
			})
			n, _ := replyWindowsOf(t)
			require.Equal(t, tt.wantOpen, n)
			dials := dialsWhileIdle(t)
			require.True(t, tt.wantDials(dials), "dials in ten idle minutes: %d", dials)
			if tt.wantOpen == 0 {
				require.Contains(t, out, "no hub contact", "the sender is told why no window was opened")
			}
		})
	}
}
