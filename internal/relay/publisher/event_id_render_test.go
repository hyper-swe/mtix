// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package publisher_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/relay/publisher"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// TestPublishPending_HostileEventID_EncodeErrorRendersSafely: a journal row
// that cannot be encoded names its event id in the safe rendering
// (MTIX-187.4.4).
func TestPublishPending_HostileEventID_EncodeErrorRendersSafely(t *testing.T) {
	ids := map[string]string{
		"newline": "ev\nFAKE LINE", "nul": "ev\x00id", "escape": "ev\x1b[31mred",
		"bidi": "ev\u202eid", "printable-é": "evé-id",
	}
	for name, id := range ids {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			cfg := r.config()
			cfg.Journal = &fakeJournal{rows: []sqlite.RelayJournalEvent{{
				Seq:   1,
				Event: model.SyncEvent{EventID: id, Payload: json.RawMessage("{")},
			}}}
			p, err := publisher.New(cfg)
			require.NoError(t, err)

			_, err = p.PublishPending(context.Background())

			require.Error(t, err)
			var marshalErr *json.MarshalerError
			require.ErrorAs(t, err, &marshalErr, "the encode error keeps the cause in its chain")
			require.Contains(t, err.Error(), "encode event "+format.EventID(id))
			for i := 0; i < len(err.Error()); i++ {
				c := err.Error()[i]
				require.True(t, c >= 0x20 && c <= 0x7e, "byte %#x in %q", c, err.Error())
			}
		})
	}
}
