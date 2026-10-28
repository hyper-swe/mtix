// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// hostileIDs carry control characters, NUL, a newline, a bidirectional
// override and printable non-ASCII (MTIX-187.4.4).
var hostileIDs = map[string]string{
	"newline":     "ev\nFAKE LINE",
	"nul":         "ev\x00id",
	"escape":      "ev\x1b[31mred",
	"bidi":        "ev\u202eid",
	"printable-é": "evé-id",
}

var errScripted = errors.New("scripted failure")

// scriptRow is a pgx.Row whose Scan runs a scripted function.
type scriptRow func(dest ...any) error

func (r scriptRow) Scan(dest ...any) error { return r(dest...) }

// scriptTx is a pgx.Tx whose statements follow a script; every other method
// panics through the nil embedded Tx, which no path under test reaches.
type scriptTx struct {
	pgx.Tx
	execErr  error
	execTag  pgconn.CommandTag
	queryErr error
	rows     []scriptRow // consumed by successive QueryRow calls
	args     [][]any     // the arguments of every Exec and QueryRow, in order
}

func (s *scriptTx) Exec(_ context.Context, _ string, args ...any) (pgconn.CommandTag, error) {
	s.args = append(s.args, args)
	return s.execTag, s.execErr
}

func (s *scriptTx) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, s.queryErr
}

func (s *scriptTx) QueryRow(_ context.Context, _ string, args ...any) pgx.Row {
	s.args = append(s.args, args)
	r := s.rows[0]
	s.rows = s.rows[1:]
	return r
}

func failRow(dest ...any) error { return errScripted }

func boolRow(v bool) scriptRow {
	return func(dest ...any) error { *(dest[0].(*bool)) = v; return nil }
}

func requireSafe(t *testing.T, err error, id string) {
	t.Helper()
	require.Error(t, err)
	msg := err.Error()
	require.Contains(t, msg, format.EventID(id))
	for i := 0; i < len(msg); i++ {
		require.True(t, msg[i] >= 0x20 && msg[i] <= 0x7e, "byte %#x at %d of %q", msg[i], i, msg)
	}
}

func pushEvent(op model.OpType, id string) *model.SyncEvent {
	return &model.SyncEvent{
		EventID: id, ProjectPrefix: "MTIX", NodeID: "MTIX-1", OpType: op,
		Payload:     json.RawMessage(`{"field_name":"title","new_value":"x"}`),
		VectorClock: model.VectorClock{"a": 1},
	}
}

// TestPushOneEvent_HostileEventID_ErrorsRenderSafely: the registry-check,
// insert and conflict-detection failures render the event id safely, wrap
// the cause and leave the event id as it was.
func TestPushOneEvent_HostileEventID_ErrorsRenderSafely(t *testing.T) {
	for idName, id := range hostileIDs {
		t.Run(idName+"/registry check", func(t *testing.T) {
			e := pushEvent(model.OpCreateNode, id)
			err := transport.PushOneEventForTest(context.Background(),
				&scriptTx{rows: []scriptRow{failRow}}, e)
			requireSafe(t, err, id)
			require.Equal(t, id, e.EventID)
		})
		t.Run(idName+"/insert", func(t *testing.T) {
			tx := &scriptTx{execErr: errScripted}
			err := transport.PushOneEventForTest(context.Background(), tx, pushEvent(model.OpUpdateField, id))
			requireSafe(t, err, id)
			require.ErrorIs(t, err, errScripted)
			require.Equal(t, id, tx.args[0][0], "the INSERT binds the raw id, never the rendering")
		})
		t.Run(idName+"/detect conflicts", func(t *testing.T) {
			err := transport.PushOneEventForTest(context.Background(),
				&scriptTx{execTag: pgconn.NewCommandTag("INSERT 0 1"), queryErr: errScripted},
				pushEvent(model.OpUpdateField, id))
			requireSafe(t, err, id)
			require.ErrorIs(t, err, errScripted)
		})
	}
}

// TestPersistConflicts_HostileEventIDs_ErrorRendersBothSafely covers the
// conflict writer.
func TestPersistConflicts_HostileEventIDs_ErrorRendersBothSafely(t *testing.T) {
	for idName, id := range hostileIDs {
		t.Run(idName, func(t *testing.T) {
			other := id + "-other\n"
			err := transport.PersistConflictsForTest(context.Background(),
				&scriptTx{execErr: errScripted},
				[]transport.ConflictDescriptor{{NewEventID: id, ConflictingEventID: other, NodeID: "MTIX-1"}})
			requireSafe(t, err, id)
			require.Contains(t, err.Error(), format.EventID(other))
			require.ErrorIs(t, err, errScripted)
		})
	}
}

// TestRecordCollision_HostileEventIDs_ErrorsRenderBothSafely covers every
// failure branch of the restore-collision recorder.
func TestRecordCollision_HostileEventIDs_ErrorsRenderBothSafely(t *testing.T) {
	branches := []struct {
		name string
		tx   func() *scriptTx
	}{
		{"recorder lookup fails", func() *scriptTx { return &scriptTx{rows: []scriptRow{failRow}} }},
		{"hub recorder fails", func() *scriptTx { return &scriptTx{rows: []scriptRow{boolRow(true), failRow}} }},
		{"hub reports not on record", func() *scriptTx {
			return &scriptTx{rows: []scriptRow{boolRow(true), boolRow(false)}}
		}},
		{"direct insert fails", func() *scriptTx {
			return &scriptTx{rows: []scriptRow{boolRow(false)}, execErr: errScripted}
		}},
	}
	for idName, id := range hostileIDs {
		for _, b := range branches {
			t.Run(idName+"/"+b.name, func(t *testing.T) {
				held := "held\u202e\n" + id
				rc := &transport.RestoreCollision{EventID: id, HeldEventID: held, ProjectPrefix: "MTIX", DisplayPath: "MTIX-1"}
				err := transport.RecordCollisionForTest(context.Background(), b.tx(), rc)
				requireSafe(t, err, id)
				require.Contains(t, err.Error(), format.EventID(held))
				require.Equal(t, id, rc.EventID)
			})
		}
	}
}

// TestRecordCollision_HostileEventIDs_BindsRawIDs: the rendering is for text
// only; the statements bind the exact ids (no stored or wire change).
func TestRecordCollision_HostileEventIDs_BindsRawIDs(t *testing.T) {
	for idName, id := range hostileIDs {
		held := "held-" + id
		rc := &transport.RestoreCollision{EventID: id, HeldEventID: held, ProjectPrefix: "MTIX", DisplayPath: "MTIX-1"}
		t.Run(idName+"/hub recorder", func(t *testing.T) {
			tx := &scriptTx{rows: []scriptRow{boolRow(true), failRow}}
			require.Error(t, transport.RecordCollisionForTest(context.Background(), tx, rc))
			require.Equal(t, id, tx.args[1][2])
		})
		t.Run(idName+"/direct insert", func(t *testing.T) {
			tx := &scriptTx{rows: []scriptRow{boolRow(false)}, execErr: errScripted}
			require.Error(t, transport.RecordCollisionForTest(context.Background(), tx, rc))
			require.Equal(t, id, tx.args[1][2])
			require.Equal(t, held, tx.args[1][6])
		})
	}
}

// TestElsewhereRefusal_HostileEventIDs_RendersEachSafely: the registry-index
// refusal names every duplicate create's event id in the safe rendering.
func TestElsewhereRefusal_HostileEventIDs_RendersEachSafely(t *testing.T) {
	var ids []string
	for _, id := range hostileIDs {
		ids = append(ids, id)
	}
	err := transport.ElsewhereRefusalForTest(ids)
	require.Error(t, err)
	for _, id := range ids {
		require.Contains(t, err.Error(), format.EventID(id))
	}
	requireSafe(t, err, ids[0])
}
