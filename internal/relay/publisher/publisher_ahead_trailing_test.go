// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package publisher_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/hyper-swe/mtix/internal/relay/publisher"
	"github.com/hyper-swe/mtix/internal/relay/segment"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

// trailingRig writes ten events to the medium with suffix appended to the
// second record's JSON, and a cursor that makes that record an adoption
// candidate outside the §5.7 tail check.
func trailingRig(t *testing.T, suffix string) (*fakeJournal, publisher.Config) {
	t.Helper()
	fake := &fakeJournal{pos: sqlite.RelayPushPosition{PubEpoch: 1, NextRS: 1}}
	for i := 1; i <= 10; i++ {
		fake.rows = append(fake.rows, fakeRow(int64(i), fmt.Sprintf("e%d", i)))
	}
	cfg := fakeConfig(t, fake)
	w, err := segment.NewWriter(segment.WriterConfig{
		Dir: cfg.SegmentsDir, PeerID: cfg.PeerID, Key: cfg.Key, KeyEpoch: cfg.KeyEpoch,
		PubEpoch: 1, NextRS: 1,
	})
	require.NoError(t, err)
	for i, row := range fake.rows {
		payload, mErr := json.Marshal(row.Event)
		require.NoError(t, mErr)
		if i == 1 {
			payload = append(payload, []byte(suffix)...)
		}
		_, aErr := w.Append(payload)
		require.NoError(t, aErr)
	}
	require.NoError(t, w.Close())
	fake.pos = sqlite.RelayPushPosition{Seq: 1, PubEpoch: 1, NextRS: 2}
	return fake, cfg
}

// TestPublishPending_MediumAheadWithTrailingCloser_IsAMismatch is
// FR-21 §6.2a item 3: after the JSON value only whitespace may follow.
// json.Decoder.More is false before ] or }, so those used to be adopted
// as identical while ingest (strict Unmarshal) rejects the record.
func TestPublishPending_MediumAheadWithTrailingCloser_IsAMismatch(t *testing.T) {
	for _, suffix := range []string{"}", "]", "}x", "]]", "}}", "[]", " }", "\n]", "\x00"} {
		t.Run(fmt.Sprintf("%q", suffix), func(t *testing.T) {
			fake, cfg := trailingRig(t, suffix)
			p, err := publisher.New(cfg)
			require.NoError(t, err)
			_, err = p.PublishPending(context.Background())
			require.ErrorIs(t, err, segment.ErrPeerIDConflict, "a record ingest would reject must not be adopted")
			require.ErrorIs(t, err, segment.ErrAheadMismatch)
			require.Equal(t, int64(1), fake.pos.Seq, "a refusal leaves the cursor alone")
		})
	}
}

// TestPublishPending_MediumAheadWithTrailingWhitespace_IsStillAdopted
// keeps the other side of item 3: whitespace after the value is fine.
func TestPublishPending_MediumAheadWithTrailingWhitespace_IsStillAdopted(t *testing.T) {
	for _, suffix := range []string{"", " ", "\n", " \t\r\n"} {
		t.Run(fmt.Sprintf("%q", suffix), func(t *testing.T) {
			fake, cfg := trailingRig(t, suffix)
			p, err := publisher.New(cfg)
			require.NoError(t, err)
			_, err = p.PublishPending(context.Background())
			require.NoError(t, err)
			require.Equal(t, int64(10), fake.pos.Seq)
		})
	}
}
