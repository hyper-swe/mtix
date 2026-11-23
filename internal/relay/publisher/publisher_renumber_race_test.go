// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package publisher_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hyper-swe/mtix/internal/relay/publisher"
	"github.com/hyper-swe/mtix/internal/relay/segment"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

// afterReadJournal runs hook once, right after the publisher's first journal
// read: the point where a hub renumber from another process can commit.
type afterReadJournal struct {
	*sqlite.Store
	hook func()
}

func (j *afterReadJournal) ReadRelayJournalSince(ctx context.Context, seq int64, limit int) ([]sqlite.RelayJournalEvent, error) {
	rows, err := j.Store.ReadRelayJournalSince(ctx, seq, limit)
	if h := j.hook; h != nil && err == nil && len(rows) > 0 {
		j.hook = nil
		h()
	}
	return rows, err
}

// framedNodeIDs returns the node id of every framed event, by event id.
func (r *rig) framedNodeIDs(t *testing.T) map[string]string {
	t.Helper()
	segs, _, err := segment.ListSegments(r.segDir)
	require.NoError(t, err)
	out := map[string]string{}
	for i, s := range segs {
		res, scanErr := segment.ScanFile(s.Path, segment.ScanOptions{
			Sealed: i < len(segs)-1, Key: testKey, ExpectPeerID: testPeerID,
		})
		require.NoError(t, scanErr)
		for _, rec := range res.Records {
			var e struct {
				EventID string `json:"event_id"`
				NodeID  string `json:"node_id"`
			}
			require.NoError(t, json.Unmarshal(rec.Payload, &e))
			out[e.EventID] = e.NodeID
		}
	}
	return out
}

// A hub renumber that commits between the publisher's journal read and its
// published mark must not leave the relay carrying a number the journal no
// longer has: the framed number and the task's number never disagree.
func TestPublishPending_RenumberBetweenReadAndMark_NeverFramesTheOldNumber(t *testing.T) {
	ctx := context.Background()
	r := newRig(t)
	r.journalEvents(t, 1)
	rows, err := r.store.ReadRelayJournalSince(ctx, 0, 10)
	require.NoError(t, err)
	uid := rows[0].Event.EventID

	var renumberErr error
	cfg := r.config()
	cfg.Journal = &afterReadJournal{Store: r.store, hook: func() {
		_, renumberErr = r.store.RenumberUnlessRelayPublished(ctx, uid)
	}}
	p, err := publisher.New(cfg)
	require.NoError(t, err)
	_, err = p.PublishPending(ctx)
	require.NoError(t, err)

	now, err := r.store.ReadRelayJournalSince(ctx, 0, 10)
	require.NoError(t, err)
	require.Len(t, now, 1)
	framed := r.framedNodeIDs(t)
	if len(framed) == 1 {
		require.Equal(t, now[0].Event.NodeID, framed[uid],
			"the framed number must be the task's number (renumber err: %v)", renumberErr)
	}
	require.NoError(t, renumberErr, "the renumber committed first, so the publisher frames the new number")
}
