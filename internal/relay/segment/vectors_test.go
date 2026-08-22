// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package segment_test

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	segment "github.com/hyper-swe/mtix/internal/relay/segment"
)

// The shared corruption corpus (FR-21 §11): fixed byte vectors, one per
// failure class the reader must judge, with the verdict each must
// receive. The bytes are NORMATIVE for format v1.0 — they were produced
// once by gen_vectors.go and committed, so this test pins two things at
// the same time:
//
//  1. the VERDICTS: every corruption class maps to the §9 registry code
//     the spec assigns it, from bytes the current encoder never touched;
//  2. the FORMAT: TestVectors_EncoderReproducesPinnedBytes re-encodes
//     one case from scratch and requires byte equality, so an accidental
//     wire change fails here first — turning a silent format break into
//     a loud, deliberate format-major decision (FR-21 §10).
//
// The release gate (FR-21.7) and any future transport work reuse this
// corpus rather than minting their own corrupt frames.

type vectorExpect struct {
	Records     int    `json:"records"`
	Truncated   bool   `json:"truncated"`
	Code        string `json:"code"`
	ErrContains string `json:"err_contains"`
}

type vectorScan struct {
	Sealed   bool         `json:"sealed"`
	Key      string       `json:"key"`
	Tolerate bool         `json:"tolerate"`
	Expect   vectorExpect `json:"expect"`
}

type vectorCase struct {
	Name         string       `json:"name"`
	Description  string       `json:"description"`
	SegmentHex   string       `json:"segment_hex"`
	SuccessorHex string       `json:"successor_hex"`
	Explains     *bool        `json:"explains"`
	Scans        []vectorScan `json:"scans"`
}

type vectorManifest struct {
	FormatVersion uint16       `json:"format_version"`
	Peer          string       `json:"peer"`
	FleetKeyHex   string       `json:"fleet_key_hex"`
	WrongKeyHex   string       `json:"wrong_key_hex"`
	Cases         []vectorCase `json:"cases"`
}

func loadVectors(t *testing.T) vectorManifest {
	t.Helper()
	raw, err := os.ReadFile("testdata/corruption_vectors.json")
	require.NoError(t, err, "corpus missing — regenerate with go run gen_vectors.go (a format-major act)")
	var m vectorManifest
	require.NoError(t, json.Unmarshal(raw, &m))
	require.Equal(t, segment.FormatVersion, m.FormatVersion,
		"corpus format version differs from this build — a format bump without regenerated, spec-approved vectors")
	require.NotEmpty(t, m.Cases)
	return m
}

func (m vectorManifest) key(t *testing.T, name string) []byte {
	t.Helper()
	switch name {
	case "fleet":
		k, err := hex.DecodeString(m.FleetKeyHex)
		require.NoError(t, err)
		return k
	case "wrong":
		k, err := hex.DecodeString(m.WrongKeyHex)
		require.NoError(t, err)
		return k
	case "none":
		return nil
	default:
		t.Fatalf("unknown corpus key %q", name)
		return nil
	}
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	require.NoError(t, err)
	return b
}

// scanCounting walks a segment with the Scanner so delivered records are
// counted even when a verdict later stops the scan (ScanAll returns an
// empty result on any verdict, which would hide the clean prefix).
func scanCounting(raw []byte, opts segment.ScanOptions) (delivered int, truncated bool, err error) {
	sc, err := segment.NewScanner(bytes.NewReader(raw), opts)
	if err != nil {
		return 0, false, err
	}
	for sc.Next() {
		delivered++
	}
	return delivered, sc.Truncated(), sc.Err()
}

// TestVectors_CorruptionClassesGetTheirRegistryVerdict runs every corpus
// case under every declared scan and asserts the delivered clean prefix,
// the truncation flag, and the §9 registry code.
func TestVectors_CorruptionClassesGetTheirRegistryVerdict(t *testing.T) {
	m := loadVectors(t)
	for _, c := range m.Cases {
		for i, s := range c.Scans {
			t.Run(c.Name+"/"+scanLabel(i, s), func(t *testing.T) {
				delivered, truncated, err := scanCounting(mustHex(t, c.SegmentHex), segment.ScanOptions{
					Sealed:           s.Sealed,
					Key:              m.key(t, s.Key),
					TolerateTornTail: s.Tolerate,
					ExpectPeerID:     m.Peer,
				})
				assert.Equal(t, s.Expect.Records, delivered, "delivered clean prefix")
				assert.Equal(t, s.Expect.Truncated, truncated, "truncation flag")
				if s.Expect.Code == "" && s.Expect.ErrContains == "" {
					assert.NoError(t, err)
					return
				}
				require.Error(t, err, "this scan must produce a verdict")
				if s.Expect.Code != "" {
					assert.Equal(t, s.Expect.Code, segment.CodeOf(err),
						"verdict must carry the registry code; got: %v", err)
				}
				if s.Expect.ErrContains != "" {
					assert.Contains(t, err.Error(), s.Expect.ErrContains)
				}
			})
		}
	}
}

// TestVectors_TornSealReconciliation drives the normative §5.5 order on
// the corpus pairs: tolerated scan of the sealed-torn segment, then the
// successor-explains predicate — explained for a resuming successor,
// refused for one that skips a position.
func TestVectors_TornSealReconciliation(t *testing.T) {
	m := loadVectors(t)
	ran := 0
	for _, c := range m.Cases {
		if c.SuccessorHex == "" {
			continue
		}
		ran++
		t.Run(c.Name, func(t *testing.T) {
			require.NotNil(t, c.Explains, "a successor case must declare the expected outcome")
			sealed, err := segment.ScanAll(bytes.NewReader(mustHex(t, c.SegmentHex)), segment.ScanOptions{
				Sealed:           true,
				Key:              m.key(t, "fleet"),
				TolerateTornTail: true,
				ExpectPeerID:     m.Peer,
			})
			require.NoError(t, err, "tolerated scan yields the clean prefix, pronouncing nothing")
			require.True(t, sealed.Truncated, "corpus pair must actually be torn")

			succ, err := segment.ScanAll(bytes.NewReader(mustHex(t, c.SuccessorHex)), segment.ScanOptions{
				Sealed: false, Key: m.key(t, "fleet"), ExpectPeerID: m.Peer,
			})
			require.NoError(t, err)
			assert.Equal(t, *c.Explains, segment.ExplainsTornSeal(sealed, succ.Header),
				"§5.5 successor-explains predicate")
		})
	}
	require.GreaterOrEqual(t, ran, 2, "corpus must carry both an explained and an unexplained pair")
}

// TestVectors_EncoderReproducesPinnedBytes rebuilds the valid corpus
// segment from scratch through the live encoder and requires byte
// equality with the committed vector. Any wire-format drift — field
// order, width, endianness, MAC pre-image — fails here before it can
// ship as a silent format break.
func TestVectors_EncoderReproducesPinnedBytes(t *testing.T) {
	m := loadVectors(t)
	var pinned []byte
	for _, c := range m.Cases {
		if c.Name == "valid_sealed_multi" {
			pinned = mustHex(t, c.SegmentHex)
		}
	}
	require.NotEmpty(t, pinned, "corpus must carry valid_sealed_multi")

	h := segment.Header{
		FormatVersion: segment.FormatVersion,
		Flags:         segment.FlagAuthenticated,
		PeerID:        m.Peer,
		SegmentNo:     1,
		FirstRS:       1,
		KeyEpoch:      1,
		PubEpoch:      1,
	}
	raw, err := h.MarshalBinary()
	require.NoError(t, err)
	for i, payload := range []string{
		`{"event_id":"corpus-1","op_type":"comment"}`,
		`{"event_id":"corpus-2","op_type":"comment"}`,
		`{"event_id":"corpus-3","op_type":"comment"}`,
	} {
		raw, err = segment.AppendRecord(raw, h, uint64(i+1), []byte(payload), m.key(t, "fleet"))
		require.NoError(t, err)
	}
	assert.True(t, bytes.Equal(pinned, raw),
		"encoder no longer reproduces the pinned v1.0 bytes — a wire-format change; if deliberate, it is format-major: spec first, regenerate the corpus second (FR-21 §10)")
}

func scanLabel(i int, s vectorScan) string {
	label := "active"
	if s.Sealed {
		label = "sealed"
	}
	if s.Tolerate {
		label += "-tolerated"
	}
	return label + "-" + s.Key + "-" + string(rune('a'+i))
}
