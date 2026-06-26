// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// sampleUID is a representative create_node event_id (UUIDv7) used as a node UID
// per ADR-003 §2. It contains hex letters so the hyphenless form is obviously
// non-numeric; allDigitUID below covers the all-digit corner case.
const sampleUID = "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"

// allDigitUID is a (synthetic) UUID whose hex happens to contain only decimal
// digits. It is the corner case proving the marker is load-bearing: without the
// non-digit marker its hyphenless short form would look like a base-10 number
// and defeat IsProvisional's shape-only detection (ADR-003 §4).
const allDigitUID = "01902130-1234-7050-8091-001020304050"

// ---------------------------------------------------------------------------
// RenderUIDSegment / IsUIDSegment — full UID -> short display segment
// ---------------------------------------------------------------------------

// TestRenderUIDSegment_Deterministic verifies the same UID always renders to the
// same short, hyphenless, marker-prefixed segment (ADR-003 §4, §13).
func TestRenderUIDSegment_Deterministic(t *testing.T) {
	seg1, err := model.RenderUIDSegment(sampleUID)
	require.NoError(t, err)
	seg2, err := model.RenderUIDSegment(sampleUID)
	require.NoError(t, err)
	assert.Equal(t, seg1, seg2, "rendering must be deterministic")
}

// TestRenderUIDSegment_ShapeIsHyphenlessMarkerHex verifies the segment shape:
// the marker prefix followed by hyphenless lowercase hex, and never a base-10
// number (ADR-003 §4, §13, §14/F-6 short-form render).
func TestRenderUIDSegment_ShapeIsHyphenlessMarkerHex(t *testing.T) {
	seg, err := model.RenderUIDSegment(sampleUID)
	require.NoError(t, err)

	assert.True(t, strings.HasPrefix(seg, model.UIDSegmentMarker),
		"segment %q must start with the marker", seg)
	assert.NotContains(t, seg, "-", "segment must be hyphenless")
	assert.True(t, model.IsUIDSegment(seg), "rendered segment must be recognized")

	body := strings.TrimPrefix(seg, model.UIDSegmentMarker)
	assert.NotEmpty(t, body)
	for _, r := range body {
		assert.True(t, (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f'),
			"segment body must be lowercase hex, got %q", string(r))
	}
}

// TestRenderUIDSegment_Short verifies the display segment is short (does not
// embed the full UID), satisfying the "short friendly form" requirement and
// avoiding leaking the full UUIDv7 timestamp (ADR-003 §13, §14/F-6).
func TestRenderUIDSegment_Short(t *testing.T) {
	seg, err := model.RenderUIDSegment(sampleUID)
	require.NoError(t, err)
	hyphenless := strings.ReplaceAll(sampleUID, "-", "")
	assert.Less(t, len(seg), len(hyphenless),
		"display segment must be shorter than the full hyphenless UID")
}

// TestRenderUIDSegment_AllDigitUID_CornerCase verifies that even a UID whose hex
// is all decimal digits renders to a non-numeric segment, so shape-only
// detection still works (ADR-003 §4). This is the key corner case.
func TestRenderUIDSegment_AllDigitUID_CornerCase(t *testing.T) {
	seg, err := model.RenderUIDSegment(allDigitUID)
	require.NoError(t, err)
	assert.True(t, model.IsUIDSegment(seg))
	// The marker guarantees the segment is not a base-10 number, so a path
	// carrying it reads as provisional from the shape alone.
	assert.True(t, model.IsProvisional("PRJX-1."+seg),
		"all-digit UID must not render to a base-10 number")
}

// TestRenderUIDSegment_RejectsMalformed verifies non-UUID input is rejected
// rather than producing a bogus segment (ADR-003 §14: shape integrity).
func TestRenderUIDSegment_RejectsMalformed(t *testing.T) {
	bad := []struct {
		name string
		uid  string
	}{
		{"empty", ""},
		{"not_a_uuid", "hello-world"},
		{"too_short", "0190a1b2"},
		{"non_hex", "zzzzzzzz-c3d4-7e5f-8a9b-0c1d2e3f4a5b"},
	}
	for _, tt := range bad {
		t.Run(tt.name, func(t *testing.T) {
			_, err := model.RenderUIDSegment(tt.uid)
			require.Error(t, err)
			assert.ErrorIs(t, err, model.ErrInvalidInput)
		})
	}
}

// ---------------------------------------------------------------------------
// IsUIDSegment — single-segment classifier
// ---------------------------------------------------------------------------

// TestIsUIDSegment verifies single-segment classification: a rendered segment is
// a uid segment; numbers and spoofed/garbage are not (ADR-003 §4).
func TestIsUIDSegment(t *testing.T) {
	good, err := model.RenderUIDSegment(sampleUID)
	require.NoError(t, err)

	tests := []struct {
		name string
		seg  string
		want bool
	}{
		{"rendered_segment", good, true},
		{"plain_number", "4", false},
		{"zero", "0", false},
		{"empty", "", false},
		{"marker_only", model.UIDSegmentMarker, false},
		{"marker_with_non_hex", model.UIDSegmentMarker + "xyz", false},
		{"marker_with_uppercase", model.UIDSegmentMarker + "ABCDEF", false},
		{"no_marker_hex", "abc123", false},
		{"marker_with_hyphen", model.UIDSegmentMarker + "01-90", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, model.IsUIDSegment(tt.seg))
		})
	}
}

// ---------------------------------------------------------------------------
// BuildProvisionalID — parent display path + full UID -> provisional id
// ---------------------------------------------------------------------------

// TestBuildProvisionalID_SingleLevel verifies a provisional child of a settled
// parent: PRJX-1.<segment> (ADR-003 §4).
func TestBuildProvisionalID_SingleLevel(t *testing.T) {
	id, err := model.BuildProvisionalID("PRJX-1", sampleUID)
	require.NoError(t, err)
	seg, _ := model.RenderUIDSegment(sampleUID)
	assert.Equal(t, "PRJX-1."+seg, id)
	assert.True(t, model.IsProvisional(id))
}

// TestBuildProvisionalID_DeepParent verifies a provisional node under a deeply
// nested settled parent (ADR-003 §4).
func TestBuildProvisionalID_DeepParent(t *testing.T) {
	id, err := model.BuildProvisionalID("PRJX-1.4.2.7", sampleUID)
	require.NoError(t, err)
	assert.True(t, model.IsProvisional(id))
	assert.Equal(t, "PRJX-1.4.2.7", model.ParseIDParent(id))
}

// TestBuildProvisionalID_RejectsBadUID verifies a malformed UID is rejected.
func TestBuildProvisionalID_RejectsBadUID(t *testing.T) {
	_, err := model.BuildProvisionalID("PRJX-1", "not-a-uuid")
	require.Error(t, err)
	assert.ErrorIs(t, err, model.ErrInvalidInput)
}

// TestBuildProvisionalID_RejectsEmptyParent verifies a provisional id requires a
// parent (a root node is always hub-settled in ADR-003's model).
func TestBuildProvisionalID_RejectsEmptyParent(t *testing.T) {
	_, err := model.BuildProvisionalID("", sampleUID)
	require.Error(t, err)
	assert.ErrorIs(t, err, model.ErrInvalidInput)
}

// ---------------------------------------------------------------------------
// IsProvisional — shape-only detection (no DB access)
// ---------------------------------------------------------------------------

// TestIsProvisional_SettledIsNotProvisional verifies fully-numeric ids are NOT
// provisional (ADR-003 §4, §8: a clean path is safe to externalize).
func TestIsProvisional_SettledIsNotProvisional(t *testing.T) {
	settled := []string{
		"PRJX-1",
		"PRJX-1.4",
		"PRJX-1.4.3",
		"PRJX-42.1.3.2.1.4",
		"MY-PROJECT-1.2", // hyphenated prefix, still fully numeric segments
	}
	for _, id := range settled {
		t.Run(id, func(t *testing.T) {
			assert.False(t, model.IsProvisional(id), "%q must be settled", id)
		})
	}
}

// TestIsProvisional_SingleLevelProvisional verifies a single-level provisional id
// is detected from the string alone (ADR-003 §4).
func TestIsProvisional_SingleLevelProvisional(t *testing.T) {
	seg, _ := model.RenderUIDSegment(sampleUID)
	assert.True(t, model.IsProvisional("PRJX-1."+seg))
}

// TestIsProvisional_DeeplyNestedProvisional verifies a uid segment at any depth
// makes the whole path provisional (ADR-003 §4).
func TestIsProvisional_DeeplyNestedProvisional(t *testing.T) {
	seg, _ := model.RenderUIDSegment(sampleUID)
	assert.True(t, model.IsProvisional("PRJX-1.4.2."+seg))
}

// TestIsProvisional_ChildUnderProvisionalParent verifies a numeric child created
// offline under a provisional parent is still provisional because the ancestor
// uid segment remains in the path (ADR-003 §4: PRJX-1.1.<uid>.1).
func TestIsProvisional_ChildUnderProvisionalParent(t *testing.T) {
	seg, _ := model.RenderUIDSegment(sampleUID)
	id := "PRJX-1." + seg + ".1"
	assert.True(t, model.IsProvisional(id),
		"a numeric child under a uid-bearing ancestor must read as provisional")
}

// TestIsProvisional_ComputableFromStringAlone documents that detection is purely
// lexical — any non-base-10 dot-segment after the prefix is provisional, with no
// store and no UID parsing required (ADR-003 §8: tooling relies on shape alone).
func TestIsProvisional_ComputableFromStringAlone(t *testing.T) {
	// An arbitrary non-numeric segment (not even a real uid render) is treated
	// as provisional by shape; settling/validation is a separate concern.
	assert.True(t, model.IsProvisional("PRJX-1.deadbeef"))
	assert.True(t, model.IsProvisional("PRJX-1.1.x.2"))
	assert.False(t, model.IsProvisional("PRJX-1.2.3"))
}

// TestIsProvisional_EdgeCases covers empty/garbage shapes (ADR-003 §4 edge).
func TestIsProvisional_EdgeCases(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want bool
	}{
		{"empty", "", false},
		{"prefix_only_no_seq", "PRJX", false},
		{"prefix_dash_only", "PRJX-", true}, // empty trailing segment is not a number
		{"leading_zero_number", "PRJX-01", false},
		{"negative_looking", "PRJX-1.-2", true}, // "-2" is not a base-10 non-negative seq
		{"trailing_dot", "PRJX-1.", true},
		{"double_dot", "PRJX-1..2", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, model.IsProvisional(tt.id), "id=%q", tt.id)
		})
	}
}

// ---------------------------------------------------------------------------
// ParseUIDSegments — extract the uid-bearing display segment(s) (inverse half)
// ---------------------------------------------------------------------------

// TestParseUIDSegments_RoundTrip verifies that the short segment rendered into a
// provisional id can be recovered from the id (the model-side inverse; full
// short-segment -> full-UID resolution goes through the store in the claim flow,
// ADR-003 §5).
func TestParseUIDSegments_RoundTrip(t *testing.T) {
	seg, err := model.RenderUIDSegment(sampleUID)
	require.NoError(t, err)
	id := "PRJX-1." + seg + ".1"

	got := model.ParseUIDSegments(id)
	require.Len(t, got, 1)
	assert.Equal(t, seg, got[0])
}

// TestParseUIDSegments_None verifies a settled id yields no uid segments.
func TestParseUIDSegments_None(t *testing.T) {
	assert.Empty(t, model.ParseUIDSegments("PRJX-1.4.3"))
}

// TestParseUIDSegments_NotANodeID verifies an unrecognizable id yields nil
// rather than panicking (edge: no PREFIX-<root> shape).
func TestParseUIDSegments_NotANodeID(t *testing.T) {
	assert.Nil(t, model.ParseUIDSegments("garbage"))
	assert.Nil(t, model.ParseUIDSegments(""))
}

// TestParseUIDSegments_Multiple verifies an id with uid segments at multiple
// depths returns all of them in order (corner case: nested provisional ancestors).
func TestParseUIDSegments_Multiple(t *testing.T) {
	s1, _ := model.RenderUIDSegment(sampleUID)
	s2, _ := model.RenderUIDSegment(allDigitUID)
	id := "PRJX-1." + s1 + ".2." + s2
	got := model.ParseUIDSegments(id)
	require.Len(t, got, 2)
	assert.Equal(t, []string{s1, s2}, got)
}

// ---------------------------------------------------------------------------
// ValidateNodeID — grammar accepts settled + provisional, rejects malformed
// ---------------------------------------------------------------------------

// TestValidateNodeID_Settled verifies fully-numeric ids validate (ADR-003 §4).
func TestValidateNodeID_Settled(t *testing.T) {
	valid := []string{
		"PRJX-1",
		"PRJX-1.4",
		"PRJX-1.4.3",
		"A-1",
		"MY-PROJECT-1.2",
		"PROJ-42.1.3.2.1.4",
	}
	for _, id := range valid {
		t.Run(id, func(t *testing.T) {
			assert.NoError(t, model.ValidateNodeID(id))
		})
	}
}

// TestValidateNodeID_Provisional verifies provisional ids (uid-bearing segment)
// validate as well-formed (ADR-003 §4, §8: provisional is valid and resolvable).
func TestValidateNodeID_Provisional(t *testing.T) {
	seg, _ := model.RenderUIDSegment(sampleUID)
	valid := []string{
		"PRJX-1." + seg,            // single-level provisional
		"PRJX-1.4.2." + seg,        // deeply nested provisional
		"PRJX-1." + seg + ".1",     // numeric child under provisional parent
		"PRJX-1." + seg + ".1.2",   // deeper child under provisional parent
	}
	for _, id := range valid {
		t.Run(id, func(t *testing.T) {
			assert.NoError(t, model.ValidateNodeID(id), "id=%q", id)
		})
	}
}

// TestValidateNodeID_Malformed verifies genuinely malformed / spoofed ids are
// still rejected (ADR-003 §14: only the shape is accepted, nothing more).
func TestValidateNodeID_Malformed(t *testing.T) {
	seg, _ := model.RenderUIDSegment(sampleUID)
	tests := []struct {
		name string
		id   string
	}{
		{"empty", ""},
		{"no_dash", "PRJX"},
		{"no_seq_after_dash", "PRJX-"},
		{"lowercase_prefix", "prjx-1"},
		{"bad_prefix_char", "PR JX-1"},
		{"root_is_uid_segment", "PRJX-" + seg}, // uid where root number must be numeric
		{"trailing_dot", "PRJX-1."},
		{"double_dot", "PRJX-1..2"},
		{"empty_middle_segment", "PRJX-1..3"},
		{"spoofed_uppercase_uid", "PRJX-1." + model.UIDSegmentMarker + "ABCDEF"},
		{"spoofed_non_hex_uid", "PRJX-1." + model.UIDSegmentMarker + "ghij"},
		{"marker_only_segment", "PRJX-1." + model.UIDSegmentMarker},
		{"negative_segment", "PRJX-1.-2"},
		{"space_in_segment", "PRJX-1. 2"},
		{"sql_wildcard", "PRJX-1.%"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := model.ValidateNodeID(tt.id)
			require.Error(t, err, "id=%q should be rejected", tt.id)
			assert.ErrorIs(t, err, model.ErrInvalidInput)
		})
	}
}

// TestValidateNodeID_RootMustBeNumeric verifies the root sequence segment must be
// numeric even though deeper segments may be provisional (ADR-003 §4: a project
// root is always hub-settled).
func TestValidateNodeID_RootMustBeNumeric(t *testing.T) {
	seg, _ := model.RenderUIDSegment(sampleUID)
	require.Error(t, model.ValidateNodeID("PRJX-"+seg))
}
