// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// F17 (MTIX-112.18.1 r2): when the eligibility check itself fails, the count
// fails closed like a read: nothing pending, everything counted as held.
func TestInboxCountPolicy_EligibilityError_FailsClosed(t *testing.T) {
	backend := &inboxCountBackend{pending: 3, total: 4}
	svc := NewInboxService(backend).WithInboxPolicy(InboxPolicy{
		Eligibility: func(context.Context, string) (InboxEligibility, error) {
			return InboxEligibility{}, errors.New("hub state unreadable")
		}})
	pending, total, held, err := svc.InboxCountPolicy(t.Context(), "w")
	require.NoError(t, err)
	assert.Zero(t, pending, "an unreadable policy reports nothing pending")
	assert.Equal(t, int64(4), total)
	assert.Equal(t, int64(3), held)
}

// F18 (MTIX-112.18.1 r2): a wait with IncludeHeld on an all-held board returns
// the held bodies instead of timing out.
func TestInboxReadWait_IncludeHeldOnAllHeldBoard_ReturnsHeldBodies(t *testing.T) {
	backend := &policyBackend{inboxReadBackend: inboxReadBackend{events: []sqlite.InboxEvent{{Seq: 1, Body: "a"}, {Seq: 2, Body: "b"}}}}
	svc := NewInboxService(backend).WithInboxPolicy(InboxPolicy{
		Eligibility: func(context.Context, string) (InboxEligibility, error) {
			return InboxEligibility{Reason: "unconfirmed"}, nil
		}})
	started := time.Now()
	res, err := svc.InboxReadWait(t.Context(), "w", 30*time.Second, InboxReadOptions{IncludeHeld: true})
	require.NoError(t, err)
	assert.Empty(t, res.Events)
	assert.Len(t, res.HeldEvents, 2)
	assert.Equal(t, 2, res.Held.Count)
	assert.Less(t, time.Since(started), 5*time.Second, "the wait ended on the held bodies, not the timeout")
}

// Without IncludeHeld the same board waits for the timeout and keeps the count.
func TestInboxReadWait_AllHeldWithoutIncludeHeld_TimesOutWithCount(t *testing.T) {
	backend := &policyBackend{inboxReadBackend: inboxReadBackend{events: []sqlite.InboxEvent{{Seq: 1}}}}
	svc := NewInboxService(backend).WithInboxPolicy(InboxPolicy{
		Eligibility: func(context.Context, string) (InboxEligibility, error) {
			return InboxEligibility{Reason: "unconfirmed"}, nil
		}})
	res, err := svc.InboxReadWait(t.Context(), "w", 300*time.Millisecond, InboxReadOptions{})
	require.NoError(t, err)
	assert.Empty(t, res.Events)
	assert.Empty(t, res.HeldEvents)
	assert.Equal(t, 1, res.Held.Count)
}
