// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
)

// Wake setting keys read by the reply-window accounting (MTIX-112.17.10).
const (
	// ReplyExtensionsKey limits holds one sender may cause per rolling hour.
	ReplyExtensionsKey = "wake.reply_extensions_per_hour"
	// RemoteAwakeCapKey caps the seconds a day that remote replies hold pulls.
	RemoteAwakeCapKey = "wake.remote_awake_daily_cap"
)

// replyExtensionWindow is the rolling period of the extension limit.
const replyExtensionWindow = time.Hour

// ReplyPolicy is the optional operator limits on remote-held pulls. Zero
// means unset, which is unlimited (ADR-007 C1, beta liberal defaults).
type ReplyPolicy struct {
	ExtensionsPerHour int64
	DailyCapSecs      int64
}

// ReplySettings reads the host wake settings.
type ReplySettings interface {
	WakeSettings(agent string) (map[string]model.WakeSetting, []oplocal.Report, error)
}

// ReadReplyPolicy reads the two optional limits from the host wake settings.
func ReadReplyPolicy(src ReplySettings) (ReplyPolicy, error) {
	values, _, err := src.WakeSettings("")
	if err != nil {
		return ReplyPolicy{}, fmt.Errorf("reply policy: %w", err)
	}
	var p ReplyPolicy
	for key, dst := range map[string]*int64{ReplyExtensionsKey: &p.ExtensionsPerHour, RemoteAwakeCapKey: &p.DailyCapSecs} {
		v := values[key].Value
		if v == "" {
			continue
		}
		n, parseErr := strconv.ParseInt(v, 10, 64)
		if parseErr != nil || n <= 0 {
			return ReplyPolicy{}, fmt.Errorf("%s %q: want a positive integer or unset: %w", key, v, model.ErrInvalidInput)
		}
		*dst = n
	}
	return p, nil
}

// ReplyInputs is the reply part of one scheduler snapshot.
type ReplyInputs struct {
	WindowOpen bool // a sender window is open
	HoldActive bool // a recipient hold is active and the daily cap is not spent
}

// ReplyInputsFor derives the scheduler inputs from the stored accounting at now.
func ReplyInputsFor(st oplocal.ReplyState, p ReplyPolicy, now time.Time) ReplyInputs {
	var in ReplyInputs
	for _, w := range st.Windows {
		if now.Before(w.ReplyBy) {
			in.WindowOpen = true
		}
	}
	if capSpent(st, p, now) {
		return in
	}
	for _, h := range st.Holds {
		if now.Before(h.Until) {
			in.HoldActive = true
		}
	}
	return in
}

func dayOf(t time.Time) string { return t.UTC().Format("2006-01-02") }

// capSpent reports whether today's (UTC) remote-held time reached the cap.
func capSpent(st oplocal.ReplyState, p ReplyPolicy, now time.Time) bool {
	return p.DailyCapSecs > 0 && st.Day == dayOf(now) && st.HeldSecs >= p.DailyCapSecs
}

// ClampHold is the end of a recipient hold: min(reply_by, fetched_at + 8h).
func ClampHold(replyBy, fetchedAt time.Time) time.Time {
	limit := fetchedAt.Add(model.MaxReplyWindow)
	if replyBy.Before(limit) {
		return replyBy
	}
	return limit
}

// accrueReplyHeld adds the union of hold time since the last accrual to the
// UTC day's remote-held seconds, then ends holds once the cap is spent.
func accrueReplyHeld(st *oplocal.ReplyState, p ReplyPolicy, now time.Time) {
	from := st.AccountedTo
	if from.IsZero() || from.After(now) {
		from = now
	}
	if floor := now.Add(-(model.MaxReplyWindow + time.Hour)); from.Before(floor) {
		from = floor
	}
	for cur := from; cur.Before(now); {
		next := time.Date(cur.Year(), cur.Month(), cur.Day()+1, 0, 0, 0, 0, time.UTC)
		end := now
		if next.Before(end) {
			end = next
		}
		if d := dayOf(cur); st.Day != d {
			st.Day, st.HeldSecs = d, 0
		}
		st.HeldSecs += heldUnion(st.Holds, cur, end)
		cur = end
	}
	if d := dayOf(now); st.Day != d {
		st.Day, st.HeldSecs = d, 0
	}
	st.AccountedTo = now
	if p.DailyCapSecs > 0 && st.HeldSecs >= p.DailyCapSecs {
		st.HeldSecs = p.DailyCapSecs
		st.Holds = nil // the day's remote-held time is spent: the holds end
	}
}

// heldUnion is the seconds in [from, to) covered by at least one hold.
func heldUnion(holds []oplocal.ReplyHold, from, to time.Time) int64 {
	type span struct{ s, e time.Time }
	var spans []span
	for _, h := range holds {
		s, e := h.Start, h.Until
		if s.Before(from) {
			s = from
		}
		if e.After(to) {
			e = to
		}
		if s.Before(e) {
			spans = append(spans, span{s, e})
		}
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].s.Before(spans[j].s) })
	var total time.Duration
	var curEnd time.Time
	for _, sp := range spans {
		if sp.s.Before(curEnd) {
			sp.s = curEnd
		}
		if sp.s.Before(sp.e) {
			total += sp.e.Sub(sp.s)
			curEnd = sp.e
		}
	}
	return int64(total / time.Second)
}

// parseReplyBy reads a payload deadline; see model.ParseReplyBy.
func parseReplyBy(v string) time.Time { return model.ParseReplyBy(v) }

// ReplyStatus is the doctor view of the reply accounting at one instant.
type ReplyStatus struct {
	OpenWindows         []oplocal.ReplyWindow `json:"open_windows"`
	ActiveHolds         []oplocal.ReplyHold   `json:"active_holds"`
	RemoteHeldSecsToday int64                 `json:"remote_held_secs_today"`
	DailyCapSecs        int64                 `json:"daily_cap_secs,omitempty"`
	ExtensionsPerHour   int64                 `json:"extensions_per_hour,omitempty"`
	CapSpent            bool                  `json:"cap_spent,omitempty"`
	// SkippedWindows counts malformed windows the scan removed recently.
	SkippedWindows int `json:"skipped_windows,omitempty"`
}

// ReplyStatusAt summarizes open windows, active holds and remote-held time
// today (UTC) at now, without writing anything (MTIX-112.20.3).
func ReplyStatusAt(st oplocal.ReplyState, p ReplyPolicy, now time.Time) ReplyStatus {
	spent := capSpent(st, p, now)
	view := st
	view.Holds = append([]oplocal.ReplyHold(nil), st.Holds...)
	accrueReplyHeld(&view, p, now)
	out := ReplyStatus{DailyCapSecs: p.DailyCapSecs, ExtensionsPerHour: p.ExtensionsPerHour,
		CapSpent: spent || (p.DailyCapSecs > 0 && view.HeldSecs >= p.DailyCapSecs), RemoteHeldSecsToday: view.HeldSecs,
		OpenWindows: []oplocal.ReplyWindow{}, ActiveHolds: []oplocal.ReplyHold{}}
	for _, sk := range st.Skipped {
		if now.Sub(sk.At) < skippedWindowAge {
			out.SkippedWindows++
		}
	}
	for _, w := range st.Windows {
		if now.Before(w.ReplyBy) {
			out.OpenWindows = append(out.OpenWindows, w)
		}
	}
	if out.CapSpent {
		return out
	}
	for _, h := range st.Holds {
		if now.Before(h.Until) {
			out.ActiveHolds = append(out.ActiveHolds, h)
		}
	}
	return out
}
