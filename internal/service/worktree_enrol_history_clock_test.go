// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
)

// A deletion's history row carries the deletion time in its value, and each
// clone stamps its own clock there (MTIX-112.17.16.18, ruling r4 point 1). Only
// the deletion time field is reduced to presence, by name; every other part of
// the row is compared.
func deleteRow(newValue, kind string) string {
	return `{"id":"h1","task_uid":"u1","ts":"t1","actor":"cli","origin":"local","kind":"` + kind +
		`","field":"deleted_at","old":"null","new":"` + newValue + `","event_id":""}`
}

func TestCompareBoardDocs_HistoryDeletedAt_TwoClockReadings_AreNoDifference(t *testing.T) {
	a := historyDoc(deleteRow(`\"2026-01-01T00:00:01Z\"`, "change"))
	b := historyDoc(deleteRow(`\"2026-01-01T00:00:09Z\"`, "change"))
	d, err := service.CompareBoardDocs([]byte(a), []byte(b))
	require.NoError(t, err)
	assert.Empty(t, d, "a clone stamps its own clock in the deletion row")
	f, err := service.BoardHubFinding([]byte(a), []byte(b), nil)
	require.NoError(t, err)
	assert.Nil(t, f)
}

func TestCompareBoardDocs_HistoryDeletedAt_OldValueClockReading_IsNoDifference(t *testing.T) {
	undelete := func(old string) string {
		return strings.Replace(deleteRow("null", "change"), `"old":"null"`, `"old":"`+old+`"`, 1)
	}
	d, err := service.CompareBoardDocs([]byte(historyDoc(undelete(`\"2026-01-01T00:00:01Z\"`))), []byte(historyDoc(undelete(`\"2026-01-01T00:00:09Z\"`))))
	require.NoError(t, err)
	assert.Empty(t, d)
}

func TestCompareBoardDocs_HistoryDeletedAt_StillADifferenceWhenPresenceOrAnythingElseDiffers(t *testing.T) {
	set := deleteRow(`\"2026-01-01T00:00:01Z\"`, "change")
	for name, tc := range map[string]struct{ a, b string }{
		"row on one side only":    {historyDoc(set), historyDoc()},
		"set against cleared":     {historyDoc(set), historyDoc(deleteRow("null", "change"))},
		"set against empty":       {historyDoc(set), historyDoc(deleteRow("", "change"))},
		"another kind of row":     {historyDoc(set), historyDoc(deleteRow(`\"2026-01-01T00:00:09Z\"`, "restore"))},
		"another field's value":   {historyDoc(strings.Replace(set, `"field":"deleted_at"`, `"field":"status"`, 1)), historyDoc(strings.Replace(deleteRow(`\"2026-01-01T00:00:09Z\"`, "change"), `"field":"deleted_at"`, `"field":"status"`, 1))},
		"a repeated deletion row": {historyDoc(set, set), historyDoc(set)},
	} {
		t.Run(name, func(t *testing.T) {
			d, err := service.CompareBoardDocs([]byte(tc.a), []byte(tc.b))
			require.NoError(t, err)
			assert.Equal(t, service.BoardDiff{"TEST-1": {"history"}}, d)
		})
	}
}

// Two refusals of an unchanged board seconds apart name the same digest when
// the only change between them is the clone's clock in a deletion row.
func TestBoardHubFinding_HistoryDeletedAtClock_DigestIsStableAcrossRefusals(t *testing.T) {
	doc := func(title, stamp string) string {
		return `{"nodes":[{"id":"TEST-1","uid":"u1","title":"` + title + `","history":[` + deleteRow(stamp, "change") + `]}]}`
	}
	local := doc("mine", `\"2026-01-01T00:00:01Z\"`)
	one, err := service.BoardHubFinding([]byte(local), []byte(doc("hub", `\"2026-01-01T00:00:02Z\"`)), nil)
	require.NoError(t, err)
	two, err := service.BoardHubFinding([]byte(local), []byte(doc("hub", `\"2026-01-01T00:00:09Z\"`)), nil)
	require.NoError(t, err)
	require.NotNil(t, one)
	assert.Equal(t, one.Digest(), two.Digest())
	other, err := service.BoardHubFinding([]byte(local), []byte(doc("hub other", `\"2026-01-01T00:00:02Z\"`)), nil)
	require.NoError(t, err)
	assert.NotEqual(t, one.Digest(), other.Digest(), "the digest still follows the values that matter")
}

// The file rule compares exact completion stamps, including removals, just as
// the hub rule does. Labels spacing is also exact within the same replica.
func TestBoardFileFinding_ClosedAtEditedOrRemoved_StaysADifference(t *testing.T) {
	file := `{"nodes":[{"id":"TEST-1","status":"done","closed_at":"2026-01-01T00:00:01Z"}]}`
	for name, edited := range map[string]string{
		"another time": `{"nodes":[{"id":"TEST-1","status":"done","closed_at":"2026-01-01T00:00:09Z"}]}`,
		"removed":      `{"nodes":[{"id":"TEST-1","status":"done"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			f, err := service.BoardFileFinding([]byte(edited), []byte(file))
			require.NoError(t, err)
			require.NotNil(t, f)
			assert.Equal(t, service.BoardDiff{"TEST-1": {"closed_at"}}, f.Diff)
		})
	}
}

func TestBoardFileFinding_LabelsSpacing_StaysADifference(t *testing.T) {
	lab := func(v string) []byte { return []byte(`{"nodes":[{"id":"TEST-1","labels":` + v + `}]}`) }
	f, err := service.BoardFileFinding(lab(`"a, b"`), lab(`"a,b"`))
	require.NoError(t, err)
	require.NotNil(t, f)
	assert.Equal(t, service.BoardDiff{"TEST-1": {"labels"}}, f.Diff)
	same, err := service.BoardFileFinding(lab(`"a,b"`), lab(`"a,b"`))
	require.NoError(t, err)
	assert.Nil(t, same)
}

// The raw comparison (no exclusions) is not a replica comparison either.
func TestCompareBoardDocsWith_NoExclusions_ClosedAtIsADifference(t *testing.T) {
	a := `{"nodes":[{"id":"TEST-1","status":"open","closed_at":"2026-01-01T00:00:01Z"}]}`
	b := `{"nodes":[{"id":"TEST-1","status":"open"}]}`
	d, err := service.CompareBoardDocsWith([]byte(a), []byte(b), nil)
	require.NoError(t, err)
	assert.Equal(t, service.BoardDiff{"TEST-1": {"closed_at"}}, d)
}
