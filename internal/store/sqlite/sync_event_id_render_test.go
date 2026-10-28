// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

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
	// Printable non-ASCII: only the ASCII-escaping format.EventID hides it
	// (strconv.Quote and a percent-q verb would print it raw).
	"latin+cjk": "caf\u00e9-\u4e2d",
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
				var syntaxErr *json.SyntaxError
				require.ErrorAs(t, err, &syntaxErr, "the decode cause stays reachable through the wrapped chain")
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

// An update_field naming a column outside the whitelist is refused with a safe
// id, both by the LWW check IdempotentApply reaches first and by the handler
// itself, and the refusal stays ErrInvalidInput.
func TestIdempotentApply_HostileEventIDUnknownUpdateField_RendersIDSafely(t *testing.T) {
	for name, id := range hostileEventIDs {
		t.Run(name, func(t *testing.T) {
			s, _ := applyTestStore(t)
			e := makeApplyEvent(t, model.OpUpdateField, "MTIX-1", "alice", 1,
				&model.UpdateFieldPayload{FieldName: "bogus", NewValue: json.RawMessage(`"v"`)})
			e.EventID = id
			err := s.WithTx(context.Background(), func(tx *sql.Tx) error { return s.IdempotentApply(context.Background(), tx, e) })
			requireSafeEventIDError(t, err, id, model.ErrInvalidInput)

			// The handler alone, as a direct dispatch caller reaches it.
			handlerErr := s.WithTx(context.Background(), func(tx *sql.Tx) error {
				return (syncApplier{clock: time.Now, store: s}).applyUpdateField(context.Background(), tx, e)
			})
			requireSafeEventIDError(t, handlerErr, id, model.ErrInvalidInput)
		})
	}
}

// A create whose uid a local task already holds names the event id and the uid
// (which defaults to the event id) only in their safe rendering.
func TestIdempotentApply_HostileEventIDUIDHeld_RendersIDSafely(t *testing.T) {
	for name, id := range hostileEventIDs {
		t.Run(name, func(t *testing.T) {
			s, _ := applyTestStore(t)
			first := makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "alice", 1, &model.CreateNodePayload{Title: "a"})
			first.EventID = id
			require.NoError(t, applyOnce(t, s, first))
			other := makeApplyEvent(t, model.OpCreateNode, "MTIX-2", "alice", 2, &model.CreateNodePayload{Title: "b"})
			other.UID = id
			err := applyOnce(t, s, other)
			requireSafeEventIDError(t, err, other.EventID, model.ErrConflict)
			require.Contains(t, err.Error(), format.EventID(id))
		})
	}
}

// Every database call of these paths fails on a finished transaction: the
// error text still names the id safely and wraps the driver cause.
func TestEventIDPaths_FinishedTx_HostileEventIDRendersIDSafely(t *testing.T) {
	for name, id := range hostileEventIDs {
		t.Run(name, func(t *testing.T) {
			s, raw := applyTestStore(t)
			ctx := context.Background()
			done, err := raw.BeginTx(ctx, nil)
			require.NoError(t, err)
			require.NoError(t, done.Rollback())
			e := makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "alice", 1, &model.CreateNodePayload{Title: "x"})
			e.EventID = id

			requireSafeEventIDError(t, (syncApplier{clock: time.Now, store: s}).IdempotentApply(ctx, done, e), id, sql.ErrTxDone)
			_, err = QuarantinedCreateBehind(ctx, done, e)
			requireSafeEventIDError(t, err, id, sql.ErrTxDone)
			requireSafeEventIDError(t, refusePendingNumber(ctx, done, id, "MTIX-1"), id, sql.ErrTxDone)
		})
	}
}

// An update_field whose payload, or whose issue_type new_value, cannot be
// decoded is refused with a safe id; the refusal keeps both the sentinel
// ErrInvalidInput and the underlying json.SyntaxError (MTIX-187.4.3).
func TestIdempotentApply_UpdateFieldUndecodable_KeepsSentinelAndSyntaxError(t *testing.T) {
	payloads := map[string]string{
		"payload does not parse":     `{"field_name":`,
		"issue_type new_value unset": `{"field_name":"issue_type"}`,
	}
	for name, payload := range payloads {
		for idName, id := range hostileEventIDs {
			t.Run(name+"/"+idName, func(t *testing.T) {
				s, _ := applyTestStore(t)
				e := makeApplyEvent(t, model.OpUpdateField, "MTIX-1", "alice", 1, &model.CreateNodePayload{Title: "x"})
				e.EventID = id
				e.Payload = []byte(payload)
				err := s.WithTx(context.Background(), func(tx *sql.Tx) error { return s.IdempotentApply(context.Background(), tx, e) })
				requireSafeEventIDError(t, err, id, model.ErrInvalidInput)
				var syntaxErr *json.SyntaxError
				require.ErrorAs(t, err, &syntaxErr)
			})
		}
	}
}

// A uid that matches no live node is named only in its safe rendering by the
// not-found error of resolveNodeRef, which keeps ErrNotFound (MTIX-187.4.3).
func TestResolveNodeRef_HostileUIDNotFound_RendersUIDSafely(t *testing.T) {
	for name, id := range hostileEventIDs {
		t.Run(name, func(t *testing.T) {
			s, _ := applyTestStore(t)
			e := makeApplyEvent(t, model.OpComment, "MTIX-1", "alice", 1, &model.CreateNodePayload{Title: "x"})
			e.UID = id
			err := s.WithTx(context.Background(), func(tx *sql.Tx) error {
				_, resolveErr := resolveNodeRef(context.Background(), tx, e)
				return resolveErr
			})
			requireSafeEventIDError(t, err, id, model.ErrNotFound)
		})
	}
}
