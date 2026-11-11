// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Command listener-spike is the non-shipped measurement harness for the
// inbox listener spikes (MTIX-112.40.1). It is built only by explicit
// path (the release pipeline builds ./cmd/mtix alone) and never touches
// a live session: it analyzes logs and checks launch plans.
//
// This file holds the latency analyzer: it turns the sender, waiter-exit
// and turn-start stamps of one run into per-cell p50/p95/max tables with
// explicit lost, duplicate and unmatched counts.
package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Sentinel errors returned (wrapped) by the analyzer.
var (
	// ErrMalformedStamp marks a stamp line that cannot be parsed.
	ErrMalformedStamp = errors.New("malformed stamp")
	// ErrNegativeLatency marks a turn or exit stamp earlier than its send.
	ErrNegativeLatency = errors.New("negative latency")
	// ErrDuplicateSend marks two send stamps for one (cell, event).
	ErrDuplicateSend = errors.New("duplicate send")
)

// Kind names what a stamp records.
type Kind string

// Stamp kinds. A send is the sender's comment commit, an exit is the
// waiter exiting with the wake envelope, a turn is the first
// UserPromptSubmit of the woken session.
const (
	KindSend Kind = "send"
	KindExit Kind = "exit"
	KindTurn Kind = "turn"
)

// Stamp is one parsed log line: when, what, in which matrix cell, for
// which event.
type Stamp struct {
	At    time.Time
	Kind  Kind
	Cell  string
	Event string
}

// Percentiles holds nearest-rank latency statistics over delivered events.
type Percentiles struct {
	P50 time.Duration
	P95 time.Duration
	Max time.Duration
}

// CellSummary is the measured result of one matrix cell (design section
// 6). Sent is N. Lost counts sends with no turn stamp; Duplicate counts
// events with more than one turn stamp (the first one is measured);
// Unmatched counts turn or exit stamps whose send is missing.
type CellSummary struct {
	Cell      string
	Sent      int
	Delivered int
	Lost      int
	Duplicate int
	Unmatched int
	Turn      Percentiles
	ExitN     int
	Exit      Percentiles
}

// ParseStamps reads a stamp log: one stamp per line as
// "<epoch-seconds[.fraction]> <send|exit|turn> <cell> <event>". Blank
// lines and lines starting with '#' are skipped. A bad line is an error
// naming its line number, never skipped silently (MTIX-112.40.1).
func ParseStamps(r io.Reader) ([]Stamp, error) {
	var out []Stamp
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		s, err := parseStampLine(line)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", n, err)
		}
		out = append(out, s)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read stamps: %w", err)
	}
	return out, nil
}

func parseStampLine(line string) (Stamp, error) {
	f := strings.Fields(line)
	if len(f) != 4 {
		return Stamp{}, fmt.Errorf("%w: want 4 fields, got %d", ErrMalformedStamp, len(f))
	}
	at, err := parseEpoch(f[0])
	if err != nil {
		return Stamp{}, err
	}
	k := Kind(f[1])
	if k != KindSend && k != KindExit && k != KindTurn {
		return Stamp{}, fmt.Errorf("%w: unknown kind %q", ErrMalformedStamp, f[1])
	}
	return Stamp{At: at, Kind: k, Cell: f[2], Event: f[3]}, nil
}

// parseEpoch converts "sec[.frac]" to a time without float rounding; digits
// beyond nanoseconds are truncated.
func parseEpoch(s string) (time.Time, error) {
	secPart, frac, hasFrac := strings.Cut(s, ".")
	sec, err := strconv.ParseInt(secPart, 10, 64)
	if err != nil || sec < 0 {
		return time.Time{}, fmt.Errorf("%w: bad epoch %q", ErrMalformedStamp, s)
	}
	var nsec int64
	if hasFrac {
		if frac == "" || strings.Trim(frac, "0123456789") != "" {
			return time.Time{}, fmt.Errorf("%w: bad fraction in %q", ErrMalformedStamp, s)
		}
		if len(frac) > 9 {
			frac = frac[:9]
		}
		frac += strings.Repeat("0", 9-len(frac))
		nsec, _ = strconv.ParseInt(frac, 10, 64) // digits only, length 9: cannot fail
	}
	return time.Unix(sec, nsec), nil
}

// eventRec accumulates the stamps of one (cell, event).
type eventRec struct {
	sent  time.Time
	hasTx bool
	turns []time.Time
	exits []time.Time
}

// Summarize groups stamps by cell and computes the per-cell table. Cells are
// returned sorted by name. A turn or exit stamp earlier than its send
// is ErrNegativeLatency; a second send for one event is ErrDuplicateSend.
func Summarize(stamps []Stamp) ([]CellSummary, error) {
	cells := map[string]map[string]*eventRec{}
	for _, s := range stamps {
		ev := cells[s.Cell]
		if ev == nil {
			ev = map[string]*eventRec{}
			cells[s.Cell] = ev
		}
		rec := ev[s.Event]
		if rec == nil {
			rec = &eventRec{}
			ev[s.Event] = rec
		}
		if err := rec.add(s); err != nil {
			return nil, err
		}
	}
	names := make([]string, 0, len(cells))
	for name := range cells {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]CellSummary, 0, len(names))
	for _, name := range names {
		sum, err := summarizeCell(name, cells[name])
		if err != nil {
			return nil, err
		}
		out = append(out, sum)
	}
	return out, nil
}

func (r *eventRec) add(s Stamp) error {
	switch s.Kind {
	case KindSend:
		if r.hasTx {
			return fmt.Errorf("cell %s event %s: %w", s.Cell, s.Event, ErrDuplicateSend)
		}
		r.sent, r.hasTx = s.At, true
	case KindTurn:
		r.turns = append(r.turns, s.At)
	case KindExit:
		r.exits = append(r.exits, s.At)
	}
	return nil
}

func summarizeCell(name string, events map[string]*eventRec) (CellSummary, error) {
	sum := CellSummary{Cell: name}
	var turnLat, exitLat []time.Duration
	for id, rec := range events {
		if !rec.hasTx {
			sum.Unmatched += len(rec.turns) + len(rec.exits)
			continue
		}
		sum.Sent++
		first, ok, err := firstAfter(name, id, rec.sent, rec.turns)
		if err != nil {
			return CellSummary{}, err
		}
		if !ok {
			sum.Lost++
		} else {
			sum.Delivered++
			turnLat = append(turnLat, first)
		}
		if len(rec.turns) > 1 {
			sum.Duplicate++
		}
		exit, hasExit, err := firstAfter(name, id, rec.sent, rec.exits)
		if err != nil {
			return CellSummary{}, err
		}
		if hasExit {
			exitLat = append(exitLat, exit)
		}
	}
	sum.Turn = statsOf(turnLat)
	sum.ExitN = len(exitLat)
	sum.Exit = statsOf(exitLat)
	return sum, nil
}

// firstAfter returns the earliest stamp's latency from sent. ok is false when
// there are no stamps.
func firstAfter(cell, event string, sent time.Time, at []time.Time) (time.Duration, bool, error) {
	if len(at) == 0 {
		return 0, false, nil
	}
	first := at[0]
	for _, t := range at[1:] {
		if t.Before(first) {
			first = t
		}
	}
	d := first.Sub(sent)
	if d < 0 {
		return 0, false, fmt.Errorf("cell %s event %s: stamp %s before send: %w", cell, event, d, ErrNegativeLatency)
	}
	return d, true, nil
}

func statsOf(lat []time.Duration) Percentiles {
	sorted := append([]time.Duration(nil), lat...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	p := Percentiles{P50: percentile(sorted, 50), P95: percentile(sorted, 95)}
	if len(sorted) > 0 {
		p.Max = sorted[len(sorted)-1]
	}
	return p
}

// percentile returns the nearest-rank p-th percentile of an ascending
// slice, or zero for an empty slice.
func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(math.Ceil(p / 100 * float64(len(sorted))))
	if rank < 1 {
		rank = 1
	}
	return sorted[rank-1]
}
