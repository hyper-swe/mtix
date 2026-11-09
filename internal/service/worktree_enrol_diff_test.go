// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
)

const baseBoard = `{"version":1,"schema_version":"2.0.0","exported_at":"2026-01-01T00:00:00Z","mtix_version":"x","project":"TEST",` +
	`"nodes":[{"id":"TEST-1","title":"one","status":"open","priority":3,"annotations":[{"id":"a1","text":"hello"}]},` +
	`{"id":"TEST-2","title":"two","status":"done","priority":1}],` +
	`"dependencies":[{"from_id":"TEST-2","to_id":"TEST-1","dep_type":"blocks","created_at":"t"}],` +
	`"agents":[{"agent_id":"lane-a","state":"idle"}],"sessions":[{"id":"s1","agent_id":"lane-a","status":"active"}],` +
	`"node_count":2,"checksum":"c1","ids_checksum":"i1"}`

func edit(t *testing.T, from, to string) string {
	t.Helper()
	require.Contains(t, baseBoard, from, "the fixture holds the text being edited")
	out := ""
	for i := 0; i+len(from) <= len(baseBoard); i++ {
		if baseBoard[i:i+len(from)] == from {
			out = baseBoard[:i] + to + baseBoard[i+len(from):]
			break
		}
	}
	return out
}

// TestCompareBoardDocs_WhatDiffers pins that every part of an export is
// compared, in both directions, and that only ids and field names come back.
func TestCompareBoardDocs_WhatDiffers(t *testing.T) {
	tests := []struct {
		name  string
		other string
		want  service.BoardDiff
	}{
		{"identical", baseBoard, service.BoardDiff{}},
		{"stamps differ and nothing else", edit(t, `"exported_at":"2026-01-01T00:00:00Z"`, `"exported_at":"2027-01-01T00:00:00Z"`), service.BoardDiff{}},
		{"node_count is derived", edit(t, `"node_count":2`, `"node_count":3`), service.BoardDiff{}},
		{"checksums differ and nothing else", edit(t, `"checksum":"c1","ids_checksum":"i1"`, `"checksum":"c2","ids_checksum":"i2"`), service.BoardDiff{}},
		{"node field", edit(t, `"status":"open"`, `"status":"done"`), service.BoardDiff{"TEST-1": {"status"}}},
		{"two node fields", edit(t, `"title":"one","status":"open"`, `"title":"uno","status":"done"`), service.BoardDiff{"TEST-1": {"status", "title"}}},
		{"nested annotation text", edit(t, `"text":"hello"`, `"text":"bye"`), service.BoardDiff{"TEST-1": {"annotations"}}},
		{"a number as written", edit(t, `"priority":3`, `"priority":3.0`), service.BoardDiff{"TEST-1": {"priority"}}},
		{"node missing on the other side", edit(t, `{"id":"TEST-2","title":"two","status":"done","priority":1}`, `{"id":"TEST-9","title":"nine"}`),
			service.BoardDiff{"TEST-2": {"(node)"}, "TEST-9": {"(node)"}}},
		{"dependency removed", edit(t, `"dependencies":[{"from_id":"TEST-2","to_id":"TEST-1","dep_type":"blocks","created_at":"t"}]`, `"dependencies":[]`),
			service.BoardDiff{"TEST-2": {"(dependencies)"}}},
		{"dependency stamp is metadata", edit(t, `"created_at":"t"`, `"created_at":"u"`), service.BoardDiff{}},
		{"dependency type is real", edit(t, `"dep_type":"blocks"`, `"dep_type":"related"`), service.BoardDiff{"TEST-2": {"(dependencies)"}}},
		{"agent changed", edit(t, `"state":"idle"`, `"state":"working"`), service.BoardDiff{}},
		{"session changed", edit(t, `"status":"active"`, `"status":"ended"`), service.BoardDiff{}},
		{"session removed", edit(t, `"sessions":[{"id":"s1","agent_id":"lane-a","status":"active"}]`, `"sessions":[]`), service.BoardDiff{}},
		{"a history row on one side only is real", edit(t, `"priority":3,`, `"priority":3,"history":[{"id":"h1","kind":"field"}],`), service.BoardDiff{"TEST-1": {"history"}}},
		{"a top-level history row on one side only is real", edit(t, `"agents":[`, `"history":[{"id":"h1","kind":"field"}],"agents":[`), service.BoardDiff{"history:(row)": {"(history)"}}},
		{"activity is metadata", edit(t, `"priority":3,`, `"priority":3,"activity":[{"x":1}],`), service.BoardDiff{}},
		{"created_at and updated_at are metadata", edit(t, `"priority":3,`, `"priority":3,"created_at":"a","updated_at":"b",`), service.BoardDiff{}},
		{"annotation id and stamp are not compared", edit(t, `{"id":"a1","text":"hello"}`, `{"id":"zz","text":"hello","created_at":"later"}`), service.BoardDiff{}},
		{"annotation author", edit(t, `{"id":"a1","text":"hello"}`, `{"id":"a1","text":"hello","author":"x"}`), service.BoardDiff{"TEST-1": {"annotations"}}},
		{"annotation addressee is content", edit(t, `{"id":"a1","text":"hello"}`, `{"id":"a1","text":"hello","addressee":"lane-a"}`), service.BoardDiff{"TEST-1": {"annotations"}}},
		{"annotation resolved is real", edit(t, `{"id":"a1","text":"hello"}`, `{"id":"a1","text":"hello","resolved":true}`), service.BoardDiff{"TEST-1": {"annotations"}}},
		{"annotation lost", edit(t, `"annotations":[{"id":"a1","text":"hello"}]`, `"annotations":[]`), service.BoardDiff{"TEST-1": {"annotations"}}},
		{"annotation order", edit(t, `"annotations":[{"id":"a1","text":"hello"}]`, `"annotations":[{"id":"a1","text":"hello"},{"id":"a2","text":"b"}]`), service.BoardDiff{"TEST-1": {"annotations"}}},
		{"deleted_at is real", edit(t, `"priority":3,`, `"priority":3,"deleted_at":"d",`), service.BoardDiff{"TEST-1": {"deleted_at"}}},
		{"deleted_by is real", edit(t, `"priority":3,`, `"priority":3,"deleted_by":"d",`), service.BoardDiff{"TEST-1": {"deleted_by"}}},
		{"defer_until is real", edit(t, `"priority":3,`, `"priority":3,"defer_until":"d",`), service.BoardDiff{"TEST-1": {"defer_until"}}},
		{"unknown top-level field", edit(t, `"project":"TEST",`, `"project":"TEST","future_section":{"a":1},`), service.BoardDiff{"(export)": {"future_section"}}},
		{"unknown node field", edit(t, `"title":"one",`, `"title":"one","future":"x",`), service.BoardDiff{"TEST-1": {"future"}}},
		{"newer schema version", edit(t, `"schema_version":"2.0.0"`, `"schema_version":"9.9.9"`), service.BoardDiff{"(export)": {"schema_version"}}},
		{"project", edit(t, `"project":"TEST"`, `"project":"OTHER"`), service.BoardDiff{"(export)": {"project"}}},
		{"a node repeated with an edit in the first copy",
			edit(t, `{"id":"TEST-2","title":"two","status":"done","priority":1}`, `{"id":"TEST-2","title":"edited"},{"id":"TEST-2","title":"two","status":"done","priority":1}`),
			service.BoardDiff{"TEST-2": {"(repeated)"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := service.CompareBoardDocs([]byte(baseBoard), []byte(tt.other))
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
			back, err := service.CompareBoardDocs([]byte(tt.other), []byte(baseBoard))
			require.NoError(t, err)
			assert.Equal(t, tt.want.IDs(), back.IDs(), "the comparison is symmetric in what it flags")
		})
	}
}

func TestCompareBoardDocs_RowOrderAndEmptySections(t *testing.T) {
	a := `{"nodes":[{"id":"TEST-1"},{"id":"TEST-2"}],"agents":null}`
	b := `{"nodes":[{"id":"TEST-2"},{"id":"TEST-1"}],"agents":[]}`

	got, err := service.CompareBoardDocs([]byte(a), []byte(b))

	require.NoError(t, err)
	assert.Empty(t, got, "order, null and empty are not differences")
}

func TestCompareBoardDocs_CarriesNoContent(t *testing.T) {
	a := `{"nodes":[{"id":"TEST-1","title":"SECRET-TITLE"}]}`
	b := `{"nodes":[{"id":"TEST-1","title":"OTHER-SECRET"}]}`

	got, err := service.CompareBoardDocs([]byte(a), []byte(b))

	require.NoError(t, err)
	assert.Equal(t, service.BoardDiff{"TEST-1": {"title"}}, got)
	assert.NotContains(t, got.IDs(), "SECRET-TITLE")
	for _, fields := range got {
		for _, f := range fields {
			assert.NotContains(t, f, "SECRET")
		}
	}
}

func TestCompareBoardDocs_Malformed_IsAnErrorNotAPass(t *testing.T) {
	for name, bad := range map[string]string{
		"not json":         `{not json`,
		"not an object":    `[1,2]`,
		"null":             `null`,
		"trailing data":    `{"nodes":[]} {"nodes":[]}`,
		"empty":            ``,
		"trailing garbage": `{"nodes":[]}x`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := service.CompareBoardDocs([]byte(bad), []byte(`{"nodes":[]}`))
			require.Error(t, err)
			_, err = service.CompareBoardDocs([]byte(`{"nodes":[]}`), []byte(bad))
			require.Error(t, err)
		})
	}
}

func TestBoardFindings_NilWhenEqual_ReasonWhenNot(t *testing.T) {
	same, err := service.BoardFileFinding([]byte(baseBoard), []byte(baseBoard))
	require.NoError(t, err)
	assert.Nil(t, same)
	same, err = service.BoardHubFinding([]byte(baseBoard), []byte(baseBoard), nil)
	require.NoError(t, err)
	assert.Nil(t, same)

	other := edit(t, `"status":"open"`, `"status":"done"`)
	f, err := service.BoardFileFinding([]byte(baseBoard), []byte(other))
	require.NoError(t, err)
	require.NotNil(t, f)
	assert.Equal(t, service.BoardFileDiffers, f.Reason)
	assert.Equal(t, []string{"TEST-1"}, f.IDs())
	h, err := service.BoardHubFinding([]byte(baseBoard), []byte(other), nil)
	require.NoError(t, err)
	require.NotNil(t, h)
	assert.Equal(t, service.BoardHubDiffers, h.Reason)

	_, err = service.BoardFileFinding([]byte("{bad"), []byte(baseBoard))
	require.Error(t, err)
	_, err = service.BoardHubFinding([]byte(baseBoard), []byte("{bad"), nil)
	require.Error(t, err)
}

func TestBoardExclusions_EachHasAReasonAndIsASmallNamedSet(t *testing.T) {
	ex := service.BoardExclusions()
	assert.LessOrEqual(t, len(ex), 8, "growing the set needs a ruling")
	paths := map[string]bool{}
	for _, x := range ex {
		assert.NotEmpty(t, x.Reason, x.Path)
		paths[x.Path] = true
	}
	assert.True(t, paths["nodes.annotations.id"])
	assert.True(t, paths["nodes.annotations.created_at"])
	for _, never := range []string{"nodes.deleted_at", "nodes.deleted_by", "nodes.defer_until", "nodes.status", "nodes.title", "nodes.annotations.text", "nodes.annotations.author", "nodes.annotations.resolved", "nodes.annotations.addressee", "nodes.closed_at"} {
		assert.False(t, paths[never], never+" can hide user content or a deletion")
	}
}

// TestBoardDiff_DeferUntilIsALocalByDesignDifference pins MTIX-112.17.16.11:
// in sync protocol 1 a defer wake time stays on the store that set it, so a
// clone of the hub never holds it and enrolment must report it, not hide it.
// Adding defer_until to either documented list would silently drop a wake
// time on accept-hub and fails here.
func TestBoardDiff_DeferUntilIsALocalByDesignDifference(t *testing.T) {
	for _, list := range [][]service.BoardExclusion{service.BoardExclusions(), service.BoardPresenceOnly()} {
		for _, x := range list {
			assert.NotContains(t, x.Path, "defer_until", "a wake time is never hidden from the comparison")
		}
	}
	local := `{"nodes":[{"id":"TEST-1","status":"deferred","defer_until":"2026-06-04T00:00:00Z"}]}`
	clone := `{"nodes":[{"id":"TEST-1","status":"deferred"}]}`
	moved := `{"nodes":[{"id":"TEST-1","status":"deferred","defer_until":"2026-06-05T00:00:00Z"}]}`

	for name, other := range map[string]string{"clone holds no wake time": clone, "clone holds another wake time": moved} {
		got, err := service.CompareBoardDocs([]byte(local), []byte(other))
		require.NoError(t, err, name)
		assert.Equal(t, service.BoardDiff{"TEST-1": {"defer_until"}}, got, name)
	}
	same, err := service.CompareBoardDocs([]byte(local), []byte(local))
	require.NoError(t, err)
	assert.Empty(t, same, "an identical wake time is not a difference")
}

func TestCompareBoardDocs_DeletedAtComparesAsPresenceNotTime(t *testing.T) {
	a := `{"nodes":[{"id":"TEST-1","deleted_at":"2026-01-01T00:00:01Z"}]}`
	b := `{"nodes":[{"id":"TEST-1","deleted_at":"2026-01-01T00:00:09Z"}]}`
	live := `{"nodes":[{"id":"TEST-1"}]}`

	same, err := service.CompareBoardDocs([]byte(a), []byte(b))
	require.NoError(t, err)
	assert.Empty(t, same, "both deleted, at different clock readings")
	flipped, err := service.CompareBoardDocs([]byte(a), []byte(live))
	require.NoError(t, err)
	assert.Equal(t, service.BoardDiff{"TEST-1": {"deleted_at"}}, flipped, "deleted on one side only is a difference")
	raw, err := service.CompareBoardDocsWith([]byte(a), []byte(b), nil)
	require.NoError(t, err)
	assert.Equal(t, service.BoardDiff{"TEST-1": {"deleted_at"}}, raw, "an unfiltered comparison sees the clock readings")
	require.Len(t, service.BoardPresenceOnly(), 1)
	assert.NotEmpty(t, service.BoardPresenceOnly()[0].Reason)
}

// TestBoardFileFinding_AppliesNoExclusions: tasks.json and the store are the
// same replica, so the hub clone's limits never excuse a file difference. Each
// field differs in the file rule. The hub rule also compares addressees; the
// remaining cases exercise fields excluded only between replicas.
func TestBoardFileFinding_AppliesNoExclusions(t *testing.T) {
	for name, other := range map[string]string{
		"activity":             edit(t, `"priority":3,`, `"priority":3,"activity":[{"x":1}],`),
		"agent row":            edit(t, `"state":"idle"`, `"state":"working"`),
		"session row":          edit(t, `"status":"active"`, `"status":"ended"`),
		"dependency stamp":     edit(t, `"created_at":"t"`, `"created_at":"u"`),
		"annotation addressee": edit(t, `{"id":"a1","text":"hello"}`, `{"id":"a1","text":"hello","addressee":"lane-a"}`),
		"annotation stamp":     edit(t, `{"id":"a1","text":"hello"}`, `{"id":"a1","text":"hello","created_at":"t"}`),
	} {
		t.Run(name, func(t *testing.T) {
			file, err := service.BoardFileFinding([]byte(other), []byte(baseBoard))
			require.NoError(t, err)
			require.NotNil(t, file, "the file rule has no exclusions")
			assert.Equal(t, service.BoardFileDiffers, file.Reason)
			hub, err := service.BoardHubFinding([]byte(other), []byte(baseBoard), nil)
			require.NoError(t, err)
			if name == "annotation addressee" {
				require.NotNil(t, hub)
				assert.Contains(t, hub.Diff["TEST-1"], "annotations")
			} else {
				assert.Nil(t, hub, "the hub rule leaves the same part out")
			}
		})
	}
}

// TestCompareBoardDocs_RowWithoutIdentity_IsKeptDistinct: a row with no id is
// matched on its whole content, so two different rows are never merged.
func TestCompareBoardDocs_RowWithoutIdentity_IsKeptDistinct(t *testing.T) {
	a := `{"extras":[{"x":1},{"x":2}]}`
	b := `{"extras":[{"x":1}]}`
	diff, err := service.CompareBoardDocs([]byte(a), []byte(b))
	require.NoError(t, err)
	assert.Equal(t, service.BoardDiff{"extras:(row)": {"(extras)"}}, diff)
}
