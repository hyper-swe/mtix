// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Service completion retains ownership and reports omitted newer exchanges.
package service

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExchangePull_ClosedOrContendedOwnership_Refused(t *testing.T) {
	s, dir := exchangeFixture(t)
	owner, err := s.BeginExchangePull(t.Context(), dir)
	require.NoError(t, err)
	_, err = s.BeginExchangePull(t.Context(), dir)
	require.ErrorIs(t, err, ErrNoExchangeRecord)
	owner.Close()
	owner.Close()
	require.ErrorIs(t, owner.Complete(t.Context(), ExchangePullCompletion{}), ErrNoExchangeRecord)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = s.BeginExchangePull(ctx, dir)
	require.ErrorIs(t, err, context.Canceled)
}

func TestExchangePull_IncompleteOrUnresolved_PreservesPriorProvenance(t *testing.T) {
	for _, kind := range []string{"cursor", "sweep", "tracking", "unknown ID", "bad cap", "closed store"} {
		t.Run(kind, func(t *testing.T) {
			s, dir := exchangeFixture(t)
			expected := exchangePublishReference(t, s, dir)
			before, err := os.ReadFile(exchangePath(dir))
			require.NoError(t, err)
			owner, err := s.BeginExchangePull(t.Context(), dir)
			require.NoError(t, err)
			completion := ExchangePullCompletion{CursorDrained: true, SweepCompleted: true}
			switch kind {
			case "cursor":
				completion.CursorDrained = false
			case "sweep":
				completion.SweepCompleted = false
			case "tracking":
				completion.Reason = "tracking-limit"
			case "unknown ID":
				completion.Encountered = []string{"missing"}
			case "bad cap":
				s.MaxImportSize = 0
			case "closed store":
				require.NoError(t, s.store.Close())
			}
			err = owner.Complete(t.Context(), completion)
			owner.Close()
			if kind == "bad cap" || kind == "closed store" {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			after, err := os.ReadFile(exchangePath(dir))
			require.NoError(t, err)
			require.Equal(t, before, after)
			s.MaxImportSize = DefaultMaxImportSize
			got, err := s.LastExchanged(t.Context(), dir)
			require.NoError(t, err)
			require.Equal(t, expected, got)
		})
	}
}

func TestExchangeRecord_InvalidPublisherStateAndPaths_ReturnErrors(t *testing.T) {
	for _, kind := range []string{"cap", "size", "cancel", "missing directory", "invalid record", "hash"} {
		t.Run(kind, func(t *testing.T) {
			s, dir := exchangeFixture(t)
			r, err := buildExchangeRecord(exchangeReference(t, exchangeCurrentJSON), "git-import", strings.Repeat("a", 64), "2026-10-01T00:00:00Z")
			require.NoError(t, err)
			ctx := t.Context()
			switch kind {
			case "cap":
				s.MaxImportSize = 0
			case "size":
				s.MaxImportSize = 1
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "missing directory":
				dir = dir + "/missing"
			case "invalid record":
				r.Format = 2
			case "hash":
				r.BoardFileHash = "bad"
			}
			require.Error(t, s.publishReceived(ctx, dir, r))
			if kind == "cancel" {
				require.ErrorIs(t, s.publishReceived(ctx, dir, r), context.Canceled)
			}
		})
	}
}
