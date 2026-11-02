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

// failingMarks wraps a backend whose mark store fails on demand.
type failingMarks struct {
	policyBackend
	readErr, advanceErr error
}

func (f *failingMarks) InboxReturnedMark(ctx context.Context, a string) (int64, error) {
	if f.readErr != nil {
		return 0, f.readErr
	}
	return f.policyBackend.InboxReturnedMark(ctx, a)
}

func (f *failingMarks) AdvanceInboxReturnedMark(ctx context.Context, a string, s int64) error {
	if f.advanceErr != nil {
		return f.advanceErr
	}
	return f.policyBackend.AdvanceInboxReturnedMark(ctx, a, s)
}

type errBudget struct{}

func (errBudget) Admit(context.Context, string, int64) (int64, error) {
	return 0, errors.New("budget down")
}

func TestInboxRead_PolicyDependencyFailures_ReturnWrappedErrors(t *testing.T) {
	events := []sqlite.InboxEvent{{Seq: 1}}
	boom := errors.New("boom")
	tests := []struct {
		name    string
		backend *failingMarks
		budget  InboxBudget
	}{
		{"mark read fails", &failingMarks{policyBackend: policyBackend{inboxReadBackend: inboxReadBackend{events: events}}, readErr: boom}, &countingBudget{limit: 5}},
		{"budget fails", &failingMarks{policyBackend: policyBackend{inboxReadBackend: inboxReadBackend{events: events}}}, errBudget{}},
		{"mark advance fails", &failingMarks{policyBackend: policyBackend{inboxReadBackend: inboxReadBackend{events: events}}, advanceErr: boom}, &countingBudget{limit: 5}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewInboxService(tt.backend).WithInboxPolicy(InboxPolicy{Eligibility: eligible(true), Budget: tt.budget})
			_, err := svc.InboxRead(t.Context(), "w", InboxReadOptions{})
			require.Error(t, err)
		})
	}
}

func TestInboxRead_BackendWithoutMarkStore_ReturnsEverything(t *testing.T) {
	backend := &inboxReadBackend{events: []sqlite.InboxEvent{{Seq: 1}, {Seq: 2}}}
	svc := NewInboxService(backend).WithInboxPolicy(InboxPolicy{Eligibility: eligible(true), Budget: &countingBudget{limit: 1}})
	res, err := svc.InboxRead(t.Context(), "w", InboxReadOptions{})
	require.NoError(t, err)
	assert.Len(t, res.Events, 2)
}

func TestInboxRead_ReaderFailure_IsWrapped(t *testing.T) {
	backend := &inboxReadBackend{inboxAckBackend: inboxAckBackend{err: errors.New("journal down")}}
	svc := NewInboxService(backend).WithInboxPolicy(InboxPolicy{Eligibility: eligible(true)})
	_, err := svc.InboxRead(t.Context(), "w", InboxReadOptions{})
	require.ErrorContains(t, err, "journal down")
	_, err = svc.InboxReadWait(t.Context(), "w", time.Second, InboxReadOptions{})
	require.ErrorContains(t, err, "journal down")
	_, err = NewPolicyInboxReader(svc).InboxWait(t.Context(), "w", time.Second)
	require.Error(t, err)
}

func TestInboxReadWait_NoPolicy_UsesTheStoreWait(t *testing.T) {
	backend := &inboxReadBackend{events: []sqlite.InboxEvent{{Seq: 4}}}
	res, err := NewInboxService(backend).InboxReadWait(t.Context(), "w", 3*time.Second, InboxReadOptions{})
	require.NoError(t, err)
	require.Len(t, res.Events, 1)
	assert.Equal(t, 3*time.Second, backend.timeout, "no policy keeps the store's own wait")

	empty := &inboxReadBackend{}
	res, err = NewInboxService(empty).InboxReadWait(t.Context(), "w", time.Second, InboxReadOptions{})
	require.NoError(t, err)
	assert.NotNil(t, res.Events)
	assert.Empty(t, res.Events)

	failing := &inboxReadBackend{inboxAckBackend: inboxAckBackend{err: errors.New("down")}}
	_, err = NewInboxService(failing).InboxReadWait(t.Context(), "w", time.Second, InboxReadOptions{})
	require.Error(t, err)
}

func TestConnectedInboxPolicy_HoldsEverythingWithReason(t *testing.T) {
	el, err := ConnectedInboxPolicy("operator state unreadable").Eligibility(t.Context(), "w")
	require.NoError(t, err)
	assert.False(t, el.Eligible)
	assert.Equal(t, "operator state unreadable", el.Reason)
}
