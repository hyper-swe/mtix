// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/sync/validator"
)

// Tests of the mutation-time wire-cap warning (MTIX-95.12): emitEvent writes
// an event whose payload is over validator.MaxPayloadBytes like any other,
// and reports it to the PayloadWarnings collector of the mutation's context.

// TestEmitEvent_OversizedPayload_ReportsFieldAndLimit: a mutation over the
// cap succeeds and, with a collector in its context, reports one warning
// naming the node, op, field, payload size and limit; under the cap, or
// without a collector, nothing is reported.
func TestEmitEvent_OversizedPayload_ReportsFieldAndLimit(t *testing.T) {
	big := strings.Repeat("p", validator.MaxPayloadBytes+100)
	tests := []struct {
		name      string
		prompt    string
		collector bool
		want      bool
	}{
		{"prompt over the cap with a collector", big, true, true},
		{"prompt under the cap", strings.Repeat("p", 60*1024), true, false},
		{"prompt over the cap without a collector", big, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, raw := mutationTestStore(t)
			collected := &sqlite.PayloadWarnings{}
			ctx := context.Background()
			if tt.collector {
				ctx = sqlite.WithPayloadWarnings(ctx, collected)
			}
			node := makeRootNode("PROJ-1", "PROJ", "node", time.Date(2026, 9, 25, 9, 0, 0, 0, time.UTC))
			node.Prompt = tt.prompt
			require.NoError(t, s.CreateNode(ctx, node), "the mutation succeeds")

			var size int
			require.NoError(t, raw.QueryRow(
				`SELECT length(CAST(payload AS BLOB)) FROM sync_events WHERE node_id = 'PROJ-1'`).Scan(&size))
			got := collected.Drain()
			if !tt.want {
				require.Empty(t, got)
				return
			}
			require.Equal(t, []sqlite.PayloadWarning{{NodeID: "PROJ-1", OpType: model.OpCreateNode,
				Field: "prompt", PayloadBytes: size, Limit: validator.MaxPayloadBytes}}, got)
			line := got[0].String()
			require.True(t, strings.HasPrefix(line, "WARN: PROJ-1: the prompt field"), line)
			require.Contains(t, line, "65536-byte sync limit")
			require.NotContains(t, line, "\n")
			require.Empty(t, collected.Drain(), "Drain empties the collector")
		})
	}
}

// TestEmitEvent_PayloadAtTheCap_WarnsOnlyAbove: the boundary matches the
// hub's rule: a payload of exactly validator.MaxPayloadBytes is valid and
// not reported; one byte more is.
func TestEmitEvent_PayloadAtTheCap_WarnsOnlyAbove(t *testing.T) {
	// A set_prompt payload is {"prompt_text":"<text>"}: the text plus 18 bytes.
	const wrap = len(`{"prompt_text":""}`)
	tests := []struct {
		name string
		size int
		want int
	}{
		{"exactly at the cap", validator.MaxPayloadBytes, 0},
		{"one byte over the cap", validator.MaxPayloadBytes + 1, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, raw := mutationTestStore(t)
			collected := &sqlite.PayloadWarnings{}
			ctx := sqlite.WithPayloadWarnings(context.Background(), collected)
			require.NoError(t, s.CreateNode(ctx, makeRootNode("PROJ-1", "PROJ", "node", time.Now().UTC())))
			prompt := strings.Repeat("p", tt.size-wrap)
			require.NoError(t, s.UpdateNode(ctx, "PROJ-1", &store.NodeUpdate{Prompt: &prompt}))
			var size int
			require.NoError(t, raw.QueryRow(`SELECT length(CAST(payload AS BLOB)) FROM sync_events
				WHERE op_type = 'set_prompt'`).Scan(&size))
			require.Equal(t, tt.size, size)
			require.Len(t, collected.Drain(), tt.want)
		})
	}
}

// TestPayloadWarnings_SameWarningTwice_ReportedOnce: two edits that make
// identical warnings are reported once.
func TestPayloadWarnings_SameWarningTwice_ReportedOnce(t *testing.T) {
	s, _ := mutationTestStore(t)
	collected := &sqlite.PayloadWarnings{}
	ctx := sqlite.WithPayloadWarnings(context.Background(), collected)
	require.NoError(t, s.CreateNode(ctx, makeRootNode("PROJ-1", "PROJ", "node", time.Now().UTC())))
	big := strings.Repeat("p", validator.MaxPayloadBytes+100)
	for i := 0; i < 2; i++ {
		require.NoError(t, s.UpdateNode(ctx, "PROJ-1", &store.NodeUpdate{Prompt: &big}))
	}
	got := collected.Drain()
	require.Len(t, got, 1)
	require.Equal(t, model.OpSetPrompt, got[0].OpType)
	require.Equal(t, "prompt", got[0].Field)
}

// TestPayloadWarning_String_OneLineWithoutControlCharacters: the rendered
// warning stays on one line whatever the node id or field holds.
func TestPayloadWarning_String_OneLineWithoutControlCharacters(t *testing.T) {
	w := sqlite.PayloadWarning{NodeID: "PROJ-1\n\x1b[31m", OpType: model.OpComment,
		Field: "com\tment\r", PayloadBytes: 70000, Limit: validator.MaxPayloadBytes}
	line := w.String()
	require.NotContains(t, line, "\n")
	require.NotContains(t, line, "\r")
	require.NotContains(t, line, "\x1b")
	require.Contains(t, line, "70000 bytes, over the 65536-byte sync limit")
}

// TestPayloadWarning_String_GuidanceMatchesOp: the warning's advice fits the
// op (MTIX-95.12 round 3, review r2 S3). A creation cannot be fixed by an
// edit, because push holds the task's later changes with it; any other op is
// fixed by shortening or splitting the field.
func TestPayloadWarning_String_GuidanceMatchesOp(t *testing.T) {
	tests := []struct {
		name string
		op   model.OpType
		want string
		not  string
	}{
		{"creation", model.OpCreateNode,
			"this task's creation will be held with every later change of it; do not edit it to fix this; see mtix sync doctor",
			"shorten or split"},
		{"prompt edit", model.OpSetPrompt, "shorten or split prompt; see mtix sync doctor", "do not edit"},
		{"comment", model.OpComment, "shorten or split prompt; see mtix sync doctor", "do not edit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			line := sqlite.PayloadWarning{NodeID: "PROJ-1", OpType: tt.op, Field: "prompt",
				PayloadBytes: 70000, Limit: validator.MaxPayloadBytes}.String()
			require.Contains(t, line, "WARN: PROJ-1: the prompt field")
			require.Contains(t, line, "65536-byte sync limit")
			require.Contains(t, line, tt.want)
			require.NotContains(t, line, tt.not)
		})
	}
}
