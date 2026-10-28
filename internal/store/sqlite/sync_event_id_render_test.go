// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/model"
)

// hostileEventIDs are non-empty ids a hostile peer can send: receivers accept
// any non-empty event_id, so the producer must render them safely (MTIX-187.4.3).
var hostileEventIDs = map[string]string{
	"ansi escape":   "\x1b[31mevil\x1b[0m",
	"newline":       "line1\nline2",
	"nul":           "a\x00b",
	"carriage+bidi": "a\rb\u202eevil",
}

// requirePrintableASCII fails unless every byte of s is printable ASCII.
func requirePrintableASCII(t *testing.T, s string) {
	t.Helper()
	for i := 0; i < len(s); i++ {
		require.Truef(t, s[i] >= 0x20 && s[i] <= 0x7e, "byte %#x at %d of %q is not printable ASCII", s[i], i, s)
	}
}

// requireSafeEventIDError asserts the error names the id only in its
// format.EventID rendering, as printable ASCII, and keeps its sentinel.
func requireSafeEventIDError(t *testing.T, err error, id string, want error) {
	t.Helper()
	require.Error(t, err)
	require.Contains(t, err.Error(), format.EventID(id))
	requirePrintableASCII(t, err.Error())
	if want != nil {
		require.ErrorIs(t, err, want)
	}
}

// Apply paths render the event id safely in their errors, and the error chain
// still reaches the cause (MTIX-187.4.3).
func TestIdempotentApply_HostileEventID_ErrorRendersIDSafely(t *testing.T) {
	ops := []model.OpType{
		model.OpCreateNode, model.OpUpdateField, model.OpComment, model.OpLinkDep,
		model.OpUnlinkDep, model.OpSetAcceptance, model.OpSetPrompt,
	}
	for name, id := range hostileEventIDs {
		for _, op := range ops {
			t.Run(name+"/"+string(op), func(t *testing.T) {
				s, _ := applyTestStore(t)
				e := makeApplyEvent(t, op, "MTIX-1", "alice", 1, &model.CreateNodePayload{Title: "x"})
				e.EventID = id
				e.Payload = []byte(`{"title":`) // undecodable payload
				err := s.WithTx(context.Background(), func(tx *sql.Tx) error { return s.IdempotentApply(context.Background(), tx, e) })
				requireSafeEventIDError(t, err, id, nil)
			})
		}
	}
}

// An event that fails validation is reported with a safe id and keeps ErrInvalidInput.
func TestIdempotentApply_HostileEventIDFailsValidation_RendersIDSafely(t *testing.T) {
	for name, id := range hostileEventIDs {
		t.Run(name, func(t *testing.T) {
			s, _ := applyTestStore(t)
			e := makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "alice", 1, &model.CreateNodePayload{Title: "x"})
			e.EventID = id
			e.LamportClock = -1
			err := s.WithTx(context.Background(), func(tx *sql.Tx) error { return s.IdempotentApply(context.Background(), tx, e) })
			requireSafeEventIDError(t, err, id, model.ErrInvalidInput)
		})
	}
}

// The quarantine accessors render the id safely when the database call fails,
// and a successful quarantine stores the id byte for byte.
func TestQuarantineAccessors_HostileEventID_ErrorsRenderIDSafely(t *testing.T) {
	for name, id := range hostileEventIDs {
		t.Run(name, func(t *testing.T) {
			s, raw := applyTestStore(t)
			ctx := context.Background()
			closed, err := raw.BeginTx(ctx, nil)
			require.NoError(t, err)
			require.NoError(t, closed.Rollback())
			q := QuarantinedEvent{EventID: id, Source: "pull", RawEvent: "{}", Reason: "r", FirstSeen: "t", LastAttempt: "t"}
			requireSafeEventIDError(t, QuarantineEvent(ctx, closed, q), id, sql.ErrTxDone)
			requireSafeEventIDError(t, RemoveQuarantined(ctx, closed, id), id, sql.ErrTxDone)
			_, err = IsQuarantined(ctx, closed, id)
			requireSafeEventIDError(t, err, id, sql.ErrTxDone)
			_, err = EventApplied(ctx, closed, id)
			requireSafeEventIDError(t, err, id, sql.ErrTxDone)

			require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error { return QuarantineEvent(ctx, tx, q) }))
			var stored string
			require.NoError(t, raw.QueryRow(`SELECT event_id FROM sync_quarantine`).Scan(&stored))
			require.Equal(t, id, stored, "the stored id is never rewritten")
		})
	}
}
