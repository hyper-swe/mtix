// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package validator_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/sync/validator"
)

// hostileEventIDs are ids a hub row can carry: control characters, a NUL, a
// newline, a bidirectional override and printable non-ASCII (MTIX-187.4.4).
var hostileEventIDs = map[string]string{
	"newline":       "ev\nFAKE LINE",
	"nul":           "ev\x00id",
	"escape":        "ev\x1b[31mred",
	"bidi":          "ev\u202eid",
	"printable-é":   "evé-id",
	"cjk":           "ev-日本",
	"tab-and-quote": "ev\t\"id\"",
}

// requirePrintableASCII fails unless s holds only bytes 0x20-0x7e.
func requirePrintableASCII(t *testing.T, s string) {
	t.Helper()
	for i := 0; i < len(s); i++ {
		require.True(t, s[i] >= 0x20 && s[i] <= 0x7e, "byte %#x at %d of %q", s[i], i, s)
	}
}

// TestValidate_HostileEventID_RendersSafelyInEveryRefusal: every refusal
// that names the event id renders it with format.EventID, keeps its
// sentinel and leaves the event's id untouched (MTIX-187.4.4).
func TestValidate_HostileEventID_RendersSafelyInEveryRefusal(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(e *model.SyncEvent)
		ingest bool
		want   error
	}{
		{"future timestamp", func(e *model.SyncEvent) { e.WallClockTS = refNow.Add(48 * 3600e9).UnixMilli() }, false, validator.ErrTimestampFuture},
		{"payload too large", func(e *model.SyncEvent) {
			e.Payload = json.RawMessage(`{"title":"` + strings.Repeat("x", validator.MaxPayloadBytes) + `"}`)
		}, false, validator.ErrPayloadTooLarge},
		{"payload too deep", func(e *model.SyncEvent) {
			e.Payload = json.RawMessage(strings.Repeat(`{"a":`, validator.MaxPayloadNestingDepth+1) + `1` + strings.Repeat(`}`, validator.MaxPayloadNestingDepth+1))
		}, false, validator.ErrPayloadTooNested},
		{"lamport overflow", func(e *model.SyncEvent) { e.LamportClock = validator.MaxLamportClock }, false, validator.ErrLamportOverflow},
		{"vector clock", func(e *model.SyncEvent) { e.VectorClock = model.VectorClock{"alice": -1} }, false, nil},
		{"malformed hub row", func(e *model.SyncEvent) { e.Malformed = "bad row" }, true, validator.ErrMalformedEvent},
		{"payload does not decode", func(e *model.SyncEvent) {
			e.OpType, e.Payload = model.OpLinkDep, json.RawMessage(`{`)
		}, true, model.ErrInvalidInput},
		{"invalid node id", func(e *model.SyncEvent) {
			e.OpType, e.NodeID, e.Payload = model.OpDelete, "MTIX-1.", json.RawMessage(`{}`)
		}, true, validator.ErrInvalidNodeID},
	}
	for idName, id := range hostileEventIDs {
		for _, tc := range cases {
			t.Run(idName+"/"+tc.name, func(t *testing.T) {
				e := validEvent()
				e.EventID = id
				tc.mutate(e)
				var err error
				if tc.ingest {
					err = validator.ValidateIngest(e, refNow, nil)
				} else {
					err = validator.Validate(e, refNow, nil)
				}
				require.Error(t, err)
				if tc.want != nil {
					require.ErrorIs(t, err, tc.want)
				}
				msg := err.Error()
				require.Contains(t, msg, format.EventID(id))
				// The fixtures name an ASCII node id; hostile node ids are MTIX-187.7.12.
				requirePrintableASCII(t, msg)
				require.Equal(t, id, e.EventID, "the event id must not be modified")
			})
		}
	}
}
