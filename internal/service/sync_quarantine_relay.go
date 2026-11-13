// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// QuarantineSourceRelay is the source of a row for a record relay ingest
// skipped (MTIX-268).
const QuarantineSourceRelay = "relay"

// RelayQuarantined is what `mtix sync quarantine list --json` adds for a relay
// row: where the record sat in the peer's stream, whether it is settled, and the
// record itself so a reviewer can read what was skipped.
type RelayQuarantined struct {
	PeerID        string `json:"peer_id"`
	PubEpoch      uint64 `json:"pub_epoch"`
	RS            uint64 `json:"rs"`
	SegmentNo     uint64 `json:"segment_no"`
	State         string `json:"state"`
	Resolution    string `json:"resolution,omitempty"`
	ResolvedBy    string `json:"resolved_by,omitempty"`
	ResolvedAt    string `json:"resolved_at,omitempty"`
	ResolveReason string `json:"resolve_reason,omitempty"`
	RecreatedAs   string `json:"recreated_as,omitempty"`
	// Payload is the record payload as received; PayloadSHA256 is its digest.
	Payload       string `json:"payload"`
	PayloadSHA256 string `json:"payload_sha256"`
}

// RelayQuarantineKeyString is the string a relay row's handle is the digest of:
// the record's place in the peer's stream, which is unique where an event id may
// be empty or repeated.
func RelayQuarantineKeyString(k sqlite.RelayQuarantineKey) string {
	return fmt.Sprintf("relay:%s:%d:%d", k.PeerID, k.PubEpoch, k.RS)
}

// relayQuarantined lists the relay rows as quarantine rows, with handles unique
// among the relay rows (MTIX-268). It only reads.
func (s *SyncService) relayQuarantined(ctx context.Context) ([]QuarantinedEvent, error) {
	rows, err := s.store.ListRelayQuarantine(ctx)
	if err != nil {
		return nil, fmt.Errorf("list relay quarantine: %w", err)
	}
	keys := make([]string, len(rows))
	for i, r := range rows {
		keys[i] = RelayQuarantineKeyString(r.RelayQuarantineKey)
	}
	handles := QuarantineHandles(keys)
	out := make([]QuarantinedEvent, len(rows))
	for i, r := range rows {
		out[i] = relayQuarantinedEvent(r, handles[i], keys[i])
	}
	return out, nil
}

func relayQuarantinedEvent(r sqlite.RelayQuarantineRow, handle, key string) QuarantinedEvent {
	sum := sha256.Sum256(r.Payload)
	return QuarantinedEvent{
		EventID: r.EventID, Handle: handle, EventIDSHA256: QuarantineHandleFull(key),
		NodeID: r.NodeID, OpType: r.OpType, LamportClock: r.Lamport,
		Source: QuarantineSourceRelay, Reason: r.Reason, Attempts: r.Attempts,
		FirstSeen: r.FirstSeen, LastAttempt: r.LastSeen,
		Relay: &RelayQuarantined{
			PeerID: r.PeerID, PubEpoch: r.PubEpoch, RS: r.RS, SegmentNo: r.SegmentNo,
			State: r.State, Resolution: r.Resolution, ResolvedBy: r.ResolvedBy,
			ResolvedAt: r.ResolvedAt, ResolveReason: r.ResolveReason, RecreatedAs: r.RecreatedAs,
			Payload: strings.ToValidUTF8(string(r.Payload), "�"), PayloadSHA256: hex.EncodeToString(sum[:]),
		},
	}
}

// resolveRelayHandle maps a handle to exactly one relay row (MTIX-268).
func resolveRelayHandle(rows []sqlite.RelayQuarantineRow, handle string) (sqlite.RelayQuarantineRow, error) {
	keys := make([]string, len(rows))
	for i, r := range rows {
		keys[i] = RelayQuarantineKeyString(r.RelayQuarantineKey)
	}
	key, err := MatchQuarantineHandle(keys, handle)
	if err != nil {
		return sqlite.RelayQuarantineRow{}, fmt.Errorf("relay quarantine handle: %w", err)
	}
	for i := range rows {
		if keys[i] == key {
			return rows[i], nil
		}
	}
	return sqlite.RelayQuarantineRow{}, fmt.Errorf("relay quarantine handle %s: %w", handle, model.ErrNotFound)
}

// relayHandleOf is the printed handle of one relay row among all of them.
func relayHandleOf(rows []sqlite.RelayQuarantineRow, key sqlite.RelayQuarantineKey) string {
	keys := make([]string, len(rows))
	for i, r := range rows {
		keys[i] = RelayQuarantineKeyString(r.RelayQuarantineKey)
	}
	handles := QuarantineHandles(keys)
	for i, r := range rows {
		if r.RelayQuarantineKey == key {
			return handles[i]
		}
	}
	return QuarantineHandle(RelayQuarantineKeyString(key))
}

// relayAcceptCommand is the exact accept command for one handle.
func relayAcceptCommand(handle string) string {
	return fmt.Sprintf(`mtix sync quarantine resolve --handle %s --accept --reason "<why>"`, handle)
}
