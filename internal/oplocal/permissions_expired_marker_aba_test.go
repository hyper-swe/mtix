// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type expiredMarkerSnapshot struct {
	snapshot permissionSnapshot
	err      error
}

func TestPermissions_ExpiredCompletedMarkerSnapshotCannotABA(t *testing.T) {
	s := alertFixture(t)
	intent := replayIntent()
	require.NoError(t, s.ApplyPermissionIntentScoped(t.Context(), intent.BoardID, intent, replayPrepare, nil))
	synthesizeExpiredCompletedPermissionHistory(t, s)
	beforeToken := expiredMarkerToken(t, s)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	paused, resume, done := pauseExpiredMarkerReader(ctx, s)
	released, joined := false, false
	defer func() {
		if !released {
			close(resume)
		}
		cancel()
		if !joined {
			<-done
		}
	}()
	select {
	case <-paused:
	case early := <-done:
		joined = true
		t.Fatalf("reader ended before pause: %v", early.err)
	case <-ctx.Done():
		t.Fatal("reader did not pause")
	}
	deny := intent
	deny.Changes = []PermissionChange{{AgentID: "fixture-agent", Action: "deny"}}
	advanced := alertTime().Add(time.Hour)
	prepare := func(_ context.Context, b PermissionBatch) (PermissionBatch, error) { b.Time = advanced; return b, nil }
	writer := New(s.env)
	writerErr := writer.ApplyPermissionIntentScoped(ctx, deny.BoardID, deny, prepare, nil)
	writerSnapshot, writerReadErr := writer.permissionSnapshot(ctx)
	close(resume)
	released = true
	got := <-done
	joined = true
	afterToken := expiredMarkerToken(t, writer)
	evidence := expiredMarkerEvidence{beforeToken, afterToken, got.snapshot, writerSnapshot, got.err, writerErr, writerReadErr}
	assertExpiredMarkerCoherence(t, evidence, intent, advanced)

}

// Synthetic history expiration is a controlled fixture, not actual eviction credit.
func synthesizeExpiredCompletedPermissionHistory(t *testing.T, s *State) {
	t.Helper()
	host, err := s.HostID()
	require.NoError(t, err)
	dir, err := s.directory(false)
	require.NoError(t, err)
	receipts, err := readPermissionReplays(dir, host)
	require.NoError(t, err)
	require.Len(t, receipts, 1)
	require.True(t, receipts[0].Completed)
	grantsBefore, err := readData(dir, "grants.json")
	require.NoError(t, err)
	tokenBefore, err := readData(dir, "permissions-pending.json")
	require.NoError(t, err)
	require.NoError(t, closeFile(dir))
	require.NoError(t, s.WriteJSON("permissions-replays", []permissionReplay{}))
	for i := range 3 {
		seedConfirmationSegment(t, s, i, []byte{})
	}
	dir, err = s.directory(false)
	require.NoError(t, err)
	grantsAfter, err := readData(dir, "grants.json")
	require.NoError(t, err)
	require.Equal(t, grantsBefore, grantsAfter)
	tokenAfter, err := readData(dir, "permissions-pending.json")
	require.NoError(t, err)
	require.Equal(t, tokenBefore, tokenAfter)
	require.NoError(t, closeFile(dir))
}
func expiredMarkerToken(t *testing.T, s *State) []byte {
	t.Helper()
	dir, err := s.directory(false)
	require.NoError(t, err)
	data, err := readPermissionPendingBytes(dir)
	require.NoError(t, err)
	require.NoError(t, closeFile(dir))
	return data
}
func pauseExpiredMarkerReader(ctx context.Context, s *State) (chan struct{}, chan struct{}, chan expiredMarkerSnapshot) {
	paused, resume, done := make(chan struct{}), make(chan struct{}), make(chan expiredMarkerSnapshot, 1)
	first := true
	s.permissionReadStage = func(string) {
		if first {
			first = false
			close(paused)
			select {
			case <-resume:
			case <-ctx.Done():
			}
		}
	}
	go func() { snapshot, err := s.permissionSnapshot(ctx); done <- expiredMarkerSnapshot{snapshot, err} }()
	return paused, resume, done
}

type expiredMarkerEvidence struct {
	beforeToken, afterToken            []byte
	reader, writer                     permissionSnapshot
	readerErr, writeErr, writerReadErr error
}

func assertExpiredMarkerCoherence(t *testing.T, e expiredMarkerEvidence, intent PermissionBatch, advanced time.Time) {
	t.Helper()
	require.NoError(t, e.writeErr)
	require.NoError(t, e.writerReadErr)
	require.NoError(t, e.readerErr)
	require.Len(t, e.writer.grants, 1)
	require.Len(t, e.writer.rows, 1)
	require.True(t, e.writer.grants[0].Denied)
	require.Equal(t, "deny", e.writer.rows[0].Action)
	require.Equal(t, advanced, e.writer.rows[0].Time)
	require.Len(t, e.reader.grants, 1)
	require.Len(t, e.reader.rows, 1)
	t.Logf("synthetic expiration before completed pending bytes: %s", e.beforeToken)
	t.Logf("synthetic expiration after completed pending bytes: %s", e.afterToken)
	t.Logf("writer denied=%t writer action=%s reader denied=%t reader action=%s token reused=%t", e.writer.grants[0].Denied, e.writer.rows[0].Action, e.reader.grants[0].Denied, e.reader.rows[0].Action, string(e.beforeToken) == string(e.afterToken))
	require.True(t, e.reader.grants[0].Denied, "snapshot must include the new deny grant with its new confirmation")
	require.Equal(t, "deny", e.reader.rows[0].Action)
	require.Equal(t, advanced, e.reader.rows[0].Time)
	require.Equal(t, intent.BoardID, e.reader.grants[0].BoardID)
	require.Equal(t, intent.BoardID, e.reader.rows[0].BoardID)
}
