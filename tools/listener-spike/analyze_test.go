// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// stampsOf parses a literal stamp log, failing the test on a parse error.
func stampsOf(t *testing.T, log string) []Stamp {
	t.Helper()
	got, err := ParseStamps(strings.NewReader(log))
	require.NoError(t, err)
	return got
}

func TestParseStamps_ValidLines_ReturnsStampsWithNanosecondTime(t *testing.T) {
	got := stampsOf(t, "# comment\n\n100.250000000 send B/idle 1\n101.5 turn B/idle 1\n102 exit B/idle 1\n")
	require.Len(t, got, 3)
	require.Equal(t, KindSend, got[0].Kind)
	require.Equal(t, "B/idle", got[0].Cell)
	require.Equal(t, "1", got[0].Event)
	require.Equal(t, time.Unix(100, 250000000).UTC(), got[0].At.UTC())
	require.Equal(t, time.Unix(101, 500000000).UTC(), got[1].At.UTC())
	require.Equal(t, KindExit, got[2].Kind)
}

func TestParseStamps_FractionBeyondNanoseconds_TruncatesWithoutError(t *testing.T) {
	got := stampsOf(t, "1793178058.6087419987 send B/idle 1\n")
	require.Equal(t, time.Unix(1793178058, 608741998).UTC(), got[0].At.UTC())
}

func TestParseStamps_MalformedLines_ReturnErrMalformedStamp(t *testing.T) {
	tests := []struct {
		name string
		line string
	}{
		{"too few fields", "100 send B/idle\n"},
		{"too many fields", "100 send B/idle 1 extra\n"},
		{"unknown kind", "100 wake B/idle 1\n"},
		{"non numeric epoch", "abc send B/idle 1\n"},
		{"negative epoch", "-1 send B/idle 1\n"},
		{"empty fraction digits", "100.x send B/idle 1\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseStamps(strings.NewReader(tt.line))
			require.ErrorIs(t, err, ErrMalformedStamp)
			require.Contains(t, err.Error(), "line 1")
		})
	}
}

func TestSummarize_NearestRankPercentiles_OverDeliveredEvents(t *testing.T) {
	var b strings.Builder
	// 20 events whose send-to-turn latency is 1..20 seconds.
	for i := 1; i <= 20; i++ {
		b.WriteString(stampLine(1000+10*i, "send", "A/idle", i))
		b.WriteString(stampLine(1000+10*i+i, "turn", "A/idle", i))
	}
	got, err := Summarize(stampsOf(t, b.String()))
	require.NoError(t, err)
	require.Len(t, got, 1)
	s := got[0]
	require.Equal(t, "A/idle", s.Cell)
	require.Equal(t, 20, s.Sent)
	require.Equal(t, 20, s.Delivered)
	require.Zero(t, s.Lost)
	require.Zero(t, s.Duplicate)
	require.Equal(t, 10*time.Second, s.Turn.P50)
	require.Equal(t, 19*time.Second, s.Turn.P95)
	require.Equal(t, 20*time.Second, s.Turn.Max)
}

func TestSummarize_MissingTurnStamp_CountsLostNotDropped(t *testing.T) {
	log := "100 send B/idle 1\n101 turn B/idle 1\n110 send B/idle 2\n120 send B/idle 3\n121 turn B/idle 3\n"
	got, err := Summarize(stampsOf(t, log))
	require.NoError(t, err)
	require.Equal(t, 3, got[0].Sent)
	require.Equal(t, 2, got[0].Delivered)
	require.Equal(t, 1, got[0].Lost)
}

func TestSummarize_TwoTurnStampsForOneEvent_CountsDuplicateUsingFirst(t *testing.T) {
	log := "100 send B/idle 1\n102 turn B/idle 1\n103 turn B/idle 1\n"
	got, err := Summarize(stampsOf(t, log))
	require.NoError(t, err)
	require.Equal(t, 1, got[0].Duplicate)
	require.Equal(t, 1, got[0].Delivered)
	require.Equal(t, 2*time.Second, got[0].Turn.Max)
}

func TestSummarize_TurnWithoutSend_CountsUnmatchedNotDropped(t *testing.T) {
	log := "100 send B/idle 1\n101 turn B/idle 1\n150 turn B/idle 9\n"
	got, err := Summarize(stampsOf(t, log))
	require.NoError(t, err)
	require.Equal(t, 1, got[0].Unmatched)
	require.Equal(t, 1, got[0].Sent)
}

func TestSummarize_ExitStamps_ReportWaiterExitLatency(t *testing.T) {
	log := "100 send B/idle 1\n100.5 exit B/idle 1\n101 turn B/idle 1\n"
	got, err := Summarize(stampsOf(t, log))
	require.NoError(t, err)
	require.Equal(t, 1, got[0].ExitN)
	require.Equal(t, 500*time.Millisecond, got[0].Exit.P50)
}

func TestSummarize_ErrorCases_ReturnSentinels(t *testing.T) {
	tests := []struct {
		name string
		log  string
		want error
	}{
		{"turn before send", "100 send B/idle 1\n99 turn B/idle 1\n", ErrNegativeLatency},
		{"exit before send", "100 send B/idle 1\n99 exit B/idle 1\n", ErrNegativeLatency},
		{"two sends for one event", "100 send B/idle 1\n101 send B/idle 1\n", ErrDuplicateSend},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Summarize(stampsOf(t, tt.log))
			require.ErrorIs(t, err, tt.want)
			require.Contains(t, err.Error(), "B/idle")
		})
	}
}

func TestSummarize_EmptyInput_ReturnsNoCellsAndNoError(t *testing.T) {
	got, err := Summarize(nil)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestSummarize_MultipleCells_ReturnedSortedByName(t *testing.T) {
	log := "1 send B/idle 1\n2 turn B/idle 1\n1 send A/idle 1\n2 turn A/idle 1\n"
	got, err := Summarize(stampsOf(t, log))
	require.NoError(t, err)
	require.Equal(t, []string{"A/idle", "B/idle"}, []string{got[0].Cell, got[1].Cell})
}

func TestPercentile_Boundaries_UseNearestRank(t *testing.T) {
	one := []time.Duration{7 * time.Second}
	require.Equal(t, 7*time.Second, percentile(one, 50))
	require.Equal(t, 7*time.Second, percentile(one, 95))
	require.Zero(t, percentile(nil, 50))
	thirty := make([]time.Duration, 30)
	for i := range thirty {
		thirty[i] = time.Duration(i+1) * time.Second
	}
	require.Equal(t, 15*time.Second, percentile(thirty, 50))
	require.Equal(t, 29*time.Second, percentile(thirty, 95))
}

// stampLine renders one whole-second stamp log line.
func stampLine(sec int, kind, cell string, event int) string {
	return strconv.Itoa(sec) + " " + kind + " " + cell + " " + strconv.Itoa(event) + "\n"
}
