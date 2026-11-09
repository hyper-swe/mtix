// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"encoding/hex"
	"encoding/json"
	"unicode/utf8"
)

type inboxJournalPlain inboxJournalEvent
type inboxJournalWire struct {
	inboxJournalPlain
	EventIDHex string `json:"event_id_hex,omitempty"`
	UIDHex     string `json:"uid_hex,omitempty"`
}

// JSON's replacement rune is not an identity. Match the board's existing
// hex encoding for non-UTF8 IDs (MTIX-187.6, MTIX-187.7.15.1).
func (e inboxJournalEvent) MarshalJSON() ([]byte, error) {
	w := inboxJournalWire{inboxJournalPlain: inboxJournalPlain(e)}
	if !utf8.ValidString(e.EventID) {
		w.EventIDHex = hex.EncodeToString([]byte(e.EventID))
	}
	if !utf8.ValidString(e.UID) {
		w.UIDHex = hex.EncodeToString([]byte(e.UID))
	}
	return json.Marshal(w)
}

func (e *inboxJournalEvent) UnmarshalJSON(data []byte) error {
	var w inboxJournalWire
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	if w.EventIDHex != "" {
		id, err := decodeIDHex("event_id_hex", w.EventIDHex)
		if err != nil {
			return err
		}
		w.EventID = id
	}
	if w.UIDHex != "" {
		id, err := decodeIDHex("uid_hex", w.UIDHex)
		if err != nil {
			return err
		}
		w.UID = id
	}
	*e = inboxJournalEvent(w.inboxJournalPlain)
	return nil
}
