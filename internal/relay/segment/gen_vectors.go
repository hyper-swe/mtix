// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build ignore

// gen_vectors regenerates testdata/corruption_vectors.json — the shared
// corruption corpus that pins the FR-21 §5.3 wire format byte-for-byte
// across every failure class the reader must judge (FR-21 §9 registry).
//
// Regeneration is a DELIBERATE act: the corpus exists so an accidental
// wire-format change fails vectors_test.go loudly. If this file needs
// re-running for any reason other than adding cases, that is a
// format-major event (FR-21 §10) and the spec moves first.
//
//	go run gen_vectors.go
package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"

	segment "github.com/hyper-swe/mtix/internal/relay/segment"
)

// Fixed, non-secret corpus keys. Never used outside tests.
var (
	fleetKey = bytesOf(0x42, 32)
	wrongKey = bytesOf(0x43, 32)
)

func bytesOf(b byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}

const peer = "0123456789abcdef-corpus"

type expect struct {
	Records     int    `json:"records"`
	Truncated   bool   `json:"truncated,omitempty"`
	Code        string `json:"code,omitempty"`
	ErrContains string `json:"err_contains,omitempty"`
}

type scan struct {
	Sealed   bool   `json:"sealed"`
	Key      string `json:"key"` // "fleet" | "wrong" | "none"
	Tolerate bool   `json:"tolerate,omitempty"`
	Expect   expect `json:"expect"`
}

type vcase struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	SegmentHex  string `json:"segment_hex"`
	// SuccessorHex, when set, is a second segment whose HEADER is fed to
	// the §5.5 successor-explains predicate after a tolerated scan.
	SuccessorHex string `json:"successor_hex,omitempty"`
	Explains     *bool  `json:"explains,omitempty"`
	Scans        []scan `json:"scans"`
}

type manifest struct {
	Comment       string  `json:"_comment"`
	FormatVersion uint16  `json:"format_version"`
	Peer          string  `json:"peer"`
	FleetKeyHex   string  `json:"fleet_key_hex"`
	WrongKeyHex   string  `json:"wrong_key_hex"`
	Cases         []vcase `json:"cases"`
}

func header(segNo, firstRS uint64, authenticated bool) segment.Header {
	h := segment.Header{
		FormatVersion: segment.FormatVersion,
		PeerID:        peer,
		SegmentNo:     segNo,
		FirstRS:       firstRS,
		KeyEpoch:      1,
		PubEpoch:      1,
	}
	if authenticated {
		h.Flags |= segment.FlagAuthenticated
	}
	return h
}

// build writes a segment with payloads starting at firstRS.
func build(h segment.Header, key []byte, payloads ...string) []byte {
	raw, err := h.MarshalBinary()
	must(err)
	rs := h.FirstRS
	for _, p := range payloads {
		raw, err = segment.AppendRecord(raw, h, rs, []byte(p), key)
		must(err)
		rs++
	}
	return raw
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func boolp(b bool) *bool { return &b }

func main() {
	h1 := header(1, 1, true)

	valid3 := build(h1, fleetKey,
		`{"event_id":"corpus-1","op_type":"comment"}`,
		`{"event_id":"corpus-2","op_type":"comment"}`,
		`{"event_id":"corpus-3","op_type":"comment"}`)

	// Torn: cut 7 bytes into the third record's frame.
	two := build(h1, fleetKey,
		`{"event_id":"corpus-1","op_type":"comment"}`,
		`{"event_id":"corpus-2","op_type":"comment"}`)
	torn := append(append([]byte(nil), valid3...)[:len(two)+7], []byte(nil)...)

	// Successor that explains the torn seal: next segment, resumes at
	// the position after the last WHOLE record (rs 3), with overlap
	// allowed — recovery republishes rs 3 itself.
	succOK := build(header(2, 3, true), fleetKey, `{"event_id":"corpus-3","op_type":"comment"}`)
	// Successor that skips a position: starts at rs 5.
	succGap := build(header(2, 5, true), fleetKey, `{"event_id":"corpus-5","op_type":"comment"}`)

	// Payload bit-flip in the second record's payload region.
	flipPayload := append([]byte(nil), valid3...)
	flipPayload[len(valid3)-10] ^= 0x01

	// MAC bit-flip: the MAC of the FIRST record sits at header+4+4+8+4.
	flipMAC := append([]byte(nil), valid3...)
	flipMAC[segment.HeaderSize+20] ^= 0x01

	// Oversize length prefix: corrupt the first record's payload_len to
	// exceed the cap (set the top byte).
	oversize := append([]byte(nil), valid3...)
	oversize[segment.HeaderSize+4] = 0xFF

	// Relocated record: a genuine record from segment 1 framed under a
	// segment-2 header — bytes are authentic, position is not.
	seg2 := header(2, 1, true)
	seg2raw, err := seg2.MarshalBinary()
	must(err)
	relocated := append(seg2raw, valid3[segment.HeaderSize:]...)

	// rs gap inside one segment: rs 1 then rs 3.
	gap, err2 := h1.MarshalBinary()
	must(err2)
	gap, err2 = segment.AppendRecord(gap, h1, 1, []byte(`{"event_id":"corpus-1"}`), fleetKey)
	must(err2)
	gap, err2 = segment.AppendRecord(gap, h1, 3, []byte(`{"event_id":"corpus-3"}`), fleetKey)
	must(err2)

	// Auth flag cleared on otherwise-authenticated bytes: flags live at
	// header offset 6..8 (big-endian u16).
	flagCleared := append([]byte(nil), valid3...)
	flagCleared[6], flagCleared[7] = 0, 0

	hu := header(1, 1, false)
	unauth := build(hu, nil,
		`{"event_id":"corpus-1","op_type":"comment"}`,
		`{"event_id":"corpus-2","op_type":"comment"}`)

	m := manifest{
		Comment: "FR-21 shared corruption corpus. Bytes are NORMATIVE for format " +
			"v1.0 (0x0100): vectors_test.go asserts both the verdicts and that the " +
			"current encoder reproduces case 'valid_sealed_multi' byte-for-byte. " +
			"Regenerating this file is a format-major act — spec first (FR-21 §10).",
		FormatVersion: segment.FormatVersion,
		Peer:          peer,
		FleetKeyHex:   hex.EncodeToString(fleetKey),
		WrongKeyHex:   hex.EncodeToString(wrongKey),
		Cases: []vcase{
			{Name: "valid_sealed_multi", Description: "three authentic records, sealed, clean",
				SegmentHex: hex.EncodeToString(valid3),
				Scans:      []scan{{Sealed: true, Key: "fleet", Expect: expect{Records: 3}}}},
			{Name: "valid_active", Description: "same bytes read as the active segment",
				SegmentHex: hex.EncodeToString(valid3),
				Scans:      []scan{{Sealed: false, Key: "fleet", Expect: expect{Records: 3}}}},
			{Name: "torn_tail", Description: "crash mid-append: active waits, sealed condemns (FR-21 §5.4)",
				SegmentHex: hex.EncodeToString(torn),
				Scans: []scan{
					{Sealed: false, Key: "fleet", Expect: expect{Records: 2, Truncated: true}},
					{Sealed: true, Key: "fleet", Expect: expect{Records: 2, Code: "RELAY_SEGMENT_CORRUPT"}},
				}},
			{Name: "torn_seal_explained", Description: "§5.5: successor resumes at/before clean-prefix+1 — explained",
				SegmentHex: hex.EncodeToString(torn), SuccessorHex: hex.EncodeToString(succOK), Explains: boolp(true),
				Scans: []scan{{Sealed: true, Key: "fleet", Tolerate: true, Expect: expect{Records: 2, Truncated: true}}}},
			{Name: "torn_seal_unexplained", Description: "§5.5: successor skips a position — never papered over",
				SegmentHex: hex.EncodeToString(torn), SuccessorHex: hex.EncodeToString(succGap), Explains: boolp(false),
				Scans: []scan{{Sealed: true, Key: "fleet", Tolerate: true, Expect: expect{Records: 2, Truncated: true}}}},
			{Name: "payload_bitflip", Description: "single bit flipped in a payload: CRC catches it",
				SegmentHex: hex.EncodeToString(flipPayload),
				Scans: []scan{
					{Sealed: true, Key: "fleet", Expect: expect{Records: 2, Code: "RELAY_SEGMENT_CORRUPT"}},
				}},
			{Name: "mac_bitflip", Description: "single bit flipped in a MAC: authentication catches it",
				SegmentHex: hex.EncodeToString(flipMAC),
				Scans: []scan{
					{Sealed: true, Key: "fleet", Expect: expect{Records: 0, Code: "RELAY_SEGMENT_CORRUPT"}},
				}},
			{Name: "oversize_length", Description: "length prefix above the cap: never honored (FR-21 §5.3)",
				SegmentHex: hex.EncodeToString(oversize),
				Scans: []scan{
					{Sealed: false, Key: "fleet", Expect: expect{Records: 0, Truncated: true}},
					{Sealed: true, Key: "fleet", Expect: expect{Records: 0, Code: "RELAY_SEGMENT_CORRUPT"}},
				}},
			{Name: "relocated_records", Description: "authentic bytes under another segment's header: non-relocation holds",
				SegmentHex: hex.EncodeToString(relocated),
				Scans: []scan{
					{Sealed: true, Key: "fleet", Expect: expect{Records: 0, Code: "RELAY_SEGMENT_CORRUPT"}},
				}},
			{Name: "rs_gap", Description: "whole authentic record at the wrong rs: loud even at the active tail (v1.3 §5.4)",
				SegmentHex: hex.EncodeToString(gap),
				Scans: []scan{
					{Sealed: false, Key: "fleet", Expect: expect{Records: 1, Code: "RELAY_GAP"}},
					{Sealed: true, Key: "fleet", Expect: expect{Records: 1, Code: "RELAY_GAP"}},
				}},
			{Name: "wrong_key", Description: "D-R10: wrong key reads as forgery; the right (old) key stays valid",
				SegmentHex: hex.EncodeToString(valid3),
				Scans: []scan{
					{Sealed: true, Key: "wrong", Expect: expect{Records: 0, Code: "RELAY_SEGMENT_CORRUPT"}},
					{Sealed: true, Key: "fleet", Expect: expect{Records: 3}},
				}},
			{Name: "auth_flag_cleared", Description: "flags bit cleared on MAC'd bytes: mode mismatch, loud, and the MACs no longer verify anywhere",
				SegmentHex: hex.EncodeToString(flagCleared),
				Scans: []scan{
					{Sealed: true, Key: "fleet", Expect: expect{Code: "RELAY_SEGMENT_CORRUPT", ErrContains: "unauthenticated"}},
					{Sealed: true, Key: "none", Expect: expect{Records: 0, Code: "RELAY_SEGMENT_CORRUPT"}},
				}},
			{Name: "unauthenticated_valid", Description: "§8.3 mode: zero MACs, no key, valid",
				SegmentHex: hex.EncodeToString(unauth),
				Scans:      []scan{{Sealed: false, Key: "none", Expect: expect{Records: 2}}}},
			{Name: "not_a_segment", Description: "foreign bytes: refused at the header in either state",
				SegmentHex: hex.EncodeToString([]byte("this is not a relay segment file at all........................................................")),
				Scans: []scan{
					{Sealed: true, Key: "fleet", Expect: expect{Code: "RELAY_SEGMENT_CORRUPT"}},
					{Sealed: false, Key: "fleet", Expect: expect{Code: "RELAY_SEGMENT_CORRUPT"}},
				}},
			{Name: "short_header", Description: "segment observed mid-creation: active retries, sealed condemns",
				SegmentHex: hex.EncodeToString(valid3[:10]),
				Scans: []scan{
					{Sealed: false, Key: "fleet", Expect: expect{ErrContains: "not fully written"}},
					{Sealed: true, Key: "fleet", Expect: expect{Code: "RELAY_SEGMENT_CORRUPT"}},
				}},
		},
	}

	out, err := json.MarshalIndent(m, "", " ")
	must(err)
	must(os.MkdirAll("testdata", 0o755))
	must(os.WriteFile("testdata/corruption_vectors.json", append(out, '\n'), 0o644))
	fmt.Printf("wrote testdata/corruption_vectors.json: %d cases\n", len(m.Cases))
}
