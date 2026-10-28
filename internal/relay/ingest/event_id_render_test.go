// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package ingest_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/relay/ingest"
)

func requireASCIILine(t *testing.T, s string) {
	t.Helper()
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' && i == len(s)-1 {
			continue
		}
		require.True(t, s[i] >= 0x20 && s[i] <= 0x7e, "byte %#x at %d of %q", s[i], i, s)
	}
}

// TestRelayIngest_HostileEventID_QuarantineLogAndReasonRenderSafely: a
// quarantined record whose event id is hostile logs the id in its
// event_id field in the safe rendering, under the text and the JSON
// handler; the Quarantine record keeps the raw id and its reason names the
// id safely (MTIX-187.4.4).
func TestRelayIngest_HostileEventID_QuarantineLogAndReasonRenderSafely(t *testing.T) {
	ids := map[string]string{
		"newline": "ev\nFAKE LINE", "nul": "ev\x00id", "escape": "ev\x1b[31mred",
		"bidi": "ev\u202eid", "printable-é": "evé-id",
	}
	handlers := map[string]func(*bytes.Buffer) slog.Handler{
		"text": func(b *bytes.Buffer) slog.Handler { return slog.NewTextHandler(b, nil) },
		"json": func(b *bytes.Buffer) slog.Handler { return slog.NewJSONHandler(b, nil) },
	}
	for idName, id := range ids {
		for hName, mk := range handlers {
			t.Run(idName+"/"+hName, func(t *testing.T) {
				r := newPeerRig(t)
				bad := event(t, model.OpLinkDep, "PROJ-1", 1, map[string]any{
					"depends_on_node_id": "PROJ-9.", "dep_type": "blocks",
				})
				bad.EventID = id
				r.publish(t, fleetKey, marshalEvents(t, bad)...)

				var logs bytes.Buffer
				cfg := r.config()
				cfg.Logger = slog.New(mk(&logs))
				in, err := ingest.New(cfg)
				require.NoError(t, err)
				stats, err := in.IngestAll(context.Background())
				require.NoError(t, err)

				require.Len(t, stats.Quarantines, 1)
				q := stats.Quarantines[0]
				require.Equal(t, id, q.EventID, "the record keeps the raw id")
				require.Contains(t, q.Reason, format.EventID(id))
				requireASCIILine(t, q.Reason)
				requireASCIILine(t, logs.String())
				if hName == "json" {
					var rec map[string]any
					require.NoError(t, json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &rec))
					require.Equal(t, format.EventID(id), rec["event_id"])
				}
			})
		}
	}
}
