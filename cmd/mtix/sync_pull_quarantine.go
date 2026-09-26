// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/sync/validator"
)

// Pulled-event quarantine of `mtix sync pull` (MTIX-95.11; review F-02,
// M21, census D14; FR-18.7, FR-18.9).
//
// Pull used to apply a whole batch in one transaction after format checks
// only, so one malformed or extreme hub row was applied (and its Lamport
// clock adopted, after which this replica's own pushes failed validation),
// and any per-event apply error (a dependency whose target had not
// arrived, a missing field) rolled the batch back and stalled pull for
// good. Now each pulled event, from the cursor pass or the late-event
// sweep, is:
//
//  1. admitted (admitPulledEvent): first its Lamport clock (below 2^53 and
//     at most sync.max_lamport_jump above the local clock,
//     validator.CheckIngestClock), then the FR-18.7 envelope validation
//     with its caps (validator.ValidateIngest; the 24h clock-relative check
//     only warns, review F-25);
//  2. applied in its own savepoint (applyPulledEvent), so a failure undoes
//     only that event's writes;
//  3. quarantined when either fails (quarantinePulledEvent): the raw event,
//     the reason and the pass that pulled it are stored in the local table
//     sync_quarantine, and the batch goes on. A quarantined event is never
//     written to sync_events and never moves the local clock.
//
// An event that is already quarantined is left to the quarantine retry: the
// passes do not rewrite its row. The cursor moves past quarantined events
// (advancePullCursor), except past a Lamport clock at or above 2^53 or
// beyond sync.max_lamport_jump, decided from the clock itself, and it is
// saved in the transaction that applies the batch (MTIX-95.4).
// retryQuarantinedEvents retries every quarantined event, in Lamport order,
// at the start of every pull and again at the end of a pull that applied
// events; an event that applies, or that is already in applied_events,
// leaves the quarantine. Retries are local and contact no hub. `mtix sync status`
// counts the quarantine, `mtix sync quarantine list` shows it, and `mtix
// sync doctor` fails while it is not empty.

const (
	// quarantineSourcePull marks an event the cursor pass quarantined.
	quarantineSourcePull = "pull"
	// quarantineSourceSweep marks an event the late-event sweep quarantined.
	quarantineSourceSweep = "sweep"
	// maxQuarantineReason caps a stored reason, in runes.
	maxQuarantineReason = 500
)

// pullIngest carries what the ingest of pulled events needs: where progress
// and warnings go, the injected clock (the reference time of the FR-18.8
// check and the quarantine's timestamps), the sync.max_lamport_jump bound
// and the CLI version recorded with a quarantined event.
type pullIngest struct {
	stderr     io.Writer
	now        func() time.Time
	maxJump    int64
	cliVersion string
}

// newPullIngest wires a pull's ingest settings for production: this
// machine's clock, the configured sync.max_lamport_jump (its default when
// no config is loaded) and this binary's version.
func newPullIngest(stderr io.Writer) pullIngest {
	maxJump := validator.DefaultMaxLamportJump
	if app.configSvc != nil {
		maxJump = app.configSvc.MaxLamportJump()
	}
	return pullIngest{stderr: stderr, now: time.Now, maxJump: maxJump, cliVersion: version}
}

// heldEvents maps the id of each event a batch quarantined, or left in the
// quarantine, to the reason it was held.
type heldEvents map[string]error

// errAlreadyQuarantined is the reason held for an event a pass met while it
// was already quarantined: the pass leaves it to the quarantine retry.
var errAlreadyQuarantined = errors.New("already quarantined; left to the quarantine retry")

// ingestPulledEvent runs one pulled event through the ingest, in the batch's
// transaction (MTIX-95.11). An event already quarantined is left to the
// quarantine retry, its row untouched. An event already in applied_events
// (for example after a clone) skips the checks: IdempotentApply dedupes it
// with no clock change. Every other event, including the hub copy of an
// own event not yet acknowledged, must pass admitPulledEvent (its Lamport
// clock, then the FR-18.7 envelope caps). It is applied in its own savepoint (applyPulledEvent); one that
// fails the checks or the apply is stored in sync_quarantine with source.
// refused is why the event was held (nil when it applied); err is a
// transaction failure that must abort the batch.
func ingestPulledEvent(ctx context.Context, tx *sql.Tx, in pullIngest,
	source string, e *model.SyncEvent,
) (refused, err error) {
	already, err := sqlite.IsQuarantined(ctx, tx, e.EventID)
	if err != nil || already {
		if already {
			refused = errAlreadyQuarantined
		}
		return refused, err
	}
	applied, err := sqlite.EventApplied(ctx, tx, e.EventID)
	if err != nil {
		return nil, err
	}
	if !applied {
		if refused, err = admitPulledEvent(ctx, tx, in, e); err != nil {
			return nil, err
		}
	}
	if refused == nil {
		if refused, err = applyPulledEvent(ctx, tx, e); err != nil {
			return nil, err
		}
	}
	if refused != nil {
		if err := quarantinePulledEvent(ctx, tx, in, source, e, refused); err != nil {
			return nil, err
		}
	}
	return refused, nil
}

// admitPulledEvent decides, before any write, whether a pulled event may be
// applied (MTIX-95.11). Its Lamport clock is checked first
// (validator.CheckIngestClock: below 2^53 and at most sync.max_lamport_jump
// above the local clock), so an event with an extreme clock is always
// refused for its clock, whatever else is wrong with it; then the FR-18.7
// envelope validation with its caps (payload size and depth, vector-clock
// limits, id grammars, a hub row that did not decode). refused is the
// reason an event is refused, nil when it may be applied. An event stamped
// more than 24h ahead of this machine's clock is admitted with a WARN on
// stderr (review F-25). err is a local failure reading the clock: it
// aborts the batch, never quarantines a hub event.
func admitPulledEvent(ctx context.Context, tx *sql.Tx, in pullIngest, e *model.SyncEvent) (refused, err error) {
	local, err := sqlite.LocalLamport(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("admit %s: %w", e.EventID, err)
	}
	return checkPulledEvent(in, e, local), nil
}

// checkPulledEvent runs the ingest checks on one event against the local
// Lamport clock local, and returns why it is refused, or nil (MTIX-95.11).
// Pull (admitPulledEvent) and clone (preflightClone, applyBatch) share it.
// The Lamport clock is checked first (validator.CheckIngestClock: below
// 2^53 and at most sync.max_lamport_jump above local), then the FR-18.7
// envelope (validator.ValidateIngest, a hub row that did not decode
// included). An event stamped more than 24h ahead of this machine's clock
// passes, with a WARN on stderr (review F-25).
func checkPulledEvent(in pullIngest, e *model.SyncEvent, local int64) error {
	if refused := validator.CheckIngestClock(e.LamportClock, local, in.maxJump); refused != nil {
		return refused
	}
	var res validator.Result
	if refused := validator.ValidateIngest(e, in.now(), &res); refused != nil {
		return fmt.Errorf("envelope validation: %w", refused)
	}
	for _, id := range res.FutureTimestamps {
		fmt.Fprintf(in.stderr,
			"WARN: pull: event %s is stamped more than 24h ahead of this machine's clock (wall_clock_ts %s); applying it (check this machine's clock)\n",
			oneLine(id), time.UnixMilli(e.WallClockTS).UTC().Format(time.RFC3339))
	}
	return nil
}

// applyPulledEvent applies e with IdempotentApply inside its own savepoint
// and, when it applies, removes any quarantine row it had (MTIX-95.11).
// rejected is the apply's error, after the savepoint undid the event's
// writes (its mirror row in sync_events included). err is a failure of the
// savepoint statements themselves, which must abort the batch. A pull that
// is cancelled or times out ends here: the batch transaction is bound to
// the pull's context, so its statements, the savepoint rollback included,
// fail and the whole batch rolls back; the event it was applying is never
// quarantined because of the cancellation.
func applyPulledEvent(ctx context.Context, tx *sql.Tx, e *model.SyncEvent) (rejected, err error) {
	rejected, err = sqlite.WithSavepoint(ctx, tx, func() error {
		if applyErr := sqlite.IdempotentApply(ctx, tx, e); applyErr != nil {
			return applyErr
		}
		return sqlite.RemoveQuarantined(ctx, tx, e.EventID)
	})
	if err != nil {
		return nil, fmt.Errorf("apply %s: %w", e.EventID, err)
	}
	return rejected, nil
}

// quarantinePulledEvent stores a refused pulled event in sync_quarantine,
// in the caller's transaction (MTIX-95.11): its JSON as pulled, the reason,
// the pass that pulled it (source) and this CLI's version. An event already
// quarantined gets one more attempt counted.
func quarantinePulledEvent(ctx context.Context, tx *sql.Tx, in pullIngest,
	source string, e *model.SyncEvent, reason error,
) error {
	raw, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("quarantine %s: encode event: %w", e.EventID, err)
	}
	return recordQuarantine(ctx, tx, in, sqlite.QuarantinedEvent{
		EventID: e.EventID, Source: source, RawEvent: string(raw),
	}, reason)
}

// recordQuarantine writes q with reason, the current time and this CLI's
// version: a new row, or one more failed attempt of an existing one. In a
// pull whose context has ended the write fails with the context's error,
// so the batch rolls back instead of quarantining.
func recordQuarantine(ctx context.Context, tx *sql.Tx, in pullIngest,
	q sqlite.QuarantinedEvent, reason error,
) error {
	at := in.now().UTC().Format(time.RFC3339Nano)
	q.Reason, q.FirstSeen, q.LastAttempt, q.CLIVersion = quarantineReason(reason), at, at, in.cliVersion
	return sqlite.QuarantineEvent(ctx, tx, q)
}

// advancePullCursor returns where to save the pull cursor after a
// cursor-pass batch, and whether to move it at all (MTIX-95.11, MTIX-95.4):
// the keyset position of the last event of the batch whose Lamport clock
// validator.CheckIngestClock accepts against the local clock after the
// batch (below 2^53 and at most maxJump above it). The decision comes from
// the clock itself, never from the reason an event was held, so the cursor
// moves past a quarantined event but an event with an extreme clock that
// also failed another check cannot move it: a cursor at an extreme clock
// would stop the cursor pass from returning any later event. Applied events
// always pass (they passed the same check against a lower local clock), and
// a batch in keyset order has no applied event after a refused one. With
// no accepted event it reports false and the saved cursor stays. The next
// pull fetches a refused event again; pullLoop pages past it in memory, so
// this pull does not fetch it in a loop.
func advancePullCursor(events []*model.SyncEvent, local, maxJump int64) (transport.PullCursor, bool) {
	for i := len(events) - 1; i >= 0; i-- {
		if validator.CheckIngestClock(events[i].LamportClock, local, maxJump) == nil {
			return transport.CursorAt(events[i]), true
		}
	}
	return transport.PullCursor{}, false
}

// quarantineRetry is the outcome of one retry of the quarantine: how many
// quarantined events applied, how many rows were dropped because their
// event was already in applied_events, and how many are still quarantined
// after it.
type quarantineRetry struct{ applied, dropped, held int }

// retryQuarantinedEvents retries every quarantined event (MTIX-95.11): in
// Lamport order, limit rows at a time, each page in one transaction and
// each event in its own savepoint, through the checks and the apply of a
// pulled event. A row whose event is already in applied_events is dropped
// before any check; an event that applies leaves the quarantine; one that
// fails again stays, with the attempt counted. It reads and writes only
// the local store. Pull runs it before contacting the hub and again at the
// end of a pull that applied events. Held push events (source push,
// MTIX-95.12) share the table but are own events push holds back: the
// retry skips them.
func retryQuarantinedEvents(ctx context.Context, in pullIngest, st *sqlite.Store, limit int) (quarantineRetry, error) {
	var out quarantineRetry
	var after *sqlite.QuarantineKey
	for {
		page, err := st.QuarantinePage(ctx, after, limit)
		if err != nil {
			return out, err
		}
		if len(page) == 0 {
			break
		}
		applied, dropped, err := retryQuarantinePage(ctx, in, st, page)
		if err != nil {
			return out, err
		}
		out.applied += applied
		out.dropped += dropped
		last := page[len(page)-1]
		after = &sqlite.QuarantineKey{Lamport: last.Lamport, EventID: last.EventID}
	}
	held, err := st.CountQuarantined(ctx)
	if err != nil {
		return out, err
	}
	out.held = held
	if out.applied > 0 {
		fmt.Fprintf(in.stderr, "pull: %d quarantined events applied on retry; %d still quarantined\n",
			out.applied, out.held)
	}
	if out.dropped > 0 {
		fmt.Fprintf(in.stderr, "pull: %d quarantined events already applied locally; removed from the quarantine\n",
			out.dropped)
	}
	return out, nil
}

// retryOutcome is what one retry did with a quarantined event.
type retryOutcome int

const (
	// retryHeld: the event failed again and stays, its attempt counted.
	retryHeld retryOutcome = iota
	// retryApplied: the event applied and left the quarantine.
	retryApplied
	// retryDropped: the event was already in applied_events; its row was
	// removed without a check.
	retryDropped
)

// retryQuarantinePage retries one page of the quarantine in one transaction
// and returns how many of its events applied and how many rows it dropped.
func retryQuarantinePage(ctx context.Context, in pullIngest, st *sqlite.Store,
	page []sqlite.QuarantinedEvent,
) (applied, dropped int, err error) {
	err = st.WithTx(ctx, func(tx *sql.Tx) error {
		applied, dropped = 0, 0
		for _, q := range page {
			if q.Source == sqlite.QuarantineSourcePush {
				continue // a held push event, not a pulled one (MTIX-95.12)
			}
			outcome, retryErr := retryQuarantined(ctx, tx, in, q)
			if retryErr != nil {
				return retryErr
			}
			switch outcome {
			case retryApplied:
				applied++
			case retryDropped:
				dropped++
			}
		}
		return nil
	})
	return applied, dropped, err
}

// retryQuarantined retries one quarantined event in tx. A row whose event
// is already in applied_events (for example after a clone applied it) is
// removed before any check; a row for an own event only in sync_events is
// checked like any other (MTIX-95.11). Otherwise the raw
// event goes through admitPulledEvent and applyPulledEvent; one that fails
// again, or no longer decodes, stays with the attempt counted.
func retryQuarantined(ctx context.Context, tx *sql.Tx, in pullIngest,
	q sqlite.QuarantinedEvent,
) (retryOutcome, error) {
	applied, err := sqlite.EventApplied(ctx, tx, q.EventID)
	if err != nil {
		return retryHeld, err
	}
	if applied {
		return retryDropped, sqlite.RemoveQuarantined(ctx, tx, q.EventID)
	}
	var e model.SyncEvent
	var refused error
	if decodeErr := json.Unmarshal([]byte(q.RawEvent), &e); decodeErr != nil {
		refused = fmt.Errorf("decode quarantined event: %w", decodeErr)
	} else if refused, err = admitPulledEvent(ctx, tx, in, &e); err != nil {
		return retryHeld, err
	}
	if refused == nil {
		if refused, err = applyPulledEvent(ctx, tx, &e); err != nil {
			return retryHeld, err
		}
	}
	if refused == nil {
		return retryApplied, nil
	}
	return retryHeld, recordQuarantine(ctx, tx, in, q, refused)
}

// resetPullState clears what `mtix sync clone` rebuilds from the hub: the
// late-event sweep state (MTIX-95.5) and the quarantine (MTIX-95.11), whose
// events the clone applies again or later pulls quarantine again.
func resetPullState(ctx context.Context, st *sqlite.Store) error {
	if err := resetLateEventSweep(ctx, st); err != nil {
		return err
	}
	return st.ClearQuarantine(ctx)
}

// reportQuarantined tells stderr, after the batch committed, which of its
// events were quarantined and why; an event already quarantined before this
// batch is not reported again.
func reportQuarantined(w io.Writer, source string, events []*model.SyncEvent, held heldEvents) {
	for _, e := range events {
		if reason, ok := held[e.EventID]; ok && !errors.Is(reason, errAlreadyQuarantined) {
			fmt.Fprintf(w, "pull: quarantined event %s (%s pass): %s; retried on every pull\n",
				oneLine(e.EventID), source, quarantineReason(reason))
		}
	}
}

// printQuarantineHeld reports on stdout, at the end of a pull, how many
// pulled events are still quarantined, if any.
func printQuarantineHeld(ctx context.Context, stdout, stderr io.Writer, st *sqlite.Store) {
	held, err := st.CountQuarantined(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "mtix sync pull: %s\n", err)
		return
	}
	if held > 0 {
		fmt.Fprintf(stdout,
			"quarantine: %d pulled events held, not applied; retried on every pull (list them: mtix sync quarantine list)\n",
			held)
	}
}

// quarantineReason renders a refusal for storage and display: one line,
// with no control characters (an event's content reaches the terminal
// through it), at most maxQuarantineReason runes.
func quarantineReason(err error) string {
	r := oneLine(err.Error())
	if runes := []rune(r); len(runes) > maxQuarantineReason {
		r = string(runes[:maxQuarantineReason]) + "..."
	}
	return r
}

// oneLine removes control characters from s and folds its whitespace runs
// into single spaces.
func oneLine(s string) string {
	return strings.Join(strings.Fields(format.StripControlChars(s)), " ")
}
