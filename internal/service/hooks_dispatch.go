// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/hyper-swe/mtix/internal/hooks"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// maxHookFiringsPerNodePerHour caps how often one hook may fire on one node — a
// runaway-loop backstop (FR-19.6). Firings past the cap are skipped and logged.
const maxHookFiringsPerNodePerHour = 20

// hookClaimLease bounds how long a 'claimed' ledger row is trusted to be in
// flight. A claim older than this belongs to a trigger that crashed between
// claim and fire, and is reclaimed and re-fired (at-least-once, FR-20 §7 — a
// lost wake is worse than a double wake; wake execs must be idempotent).
const hookClaimLease = 60 * time.Second

// HooksDispatcher fires the delivery adapters of hooks that match journaled
// events (FR-19.3, FR-20/MTIX-56.1). There is ONE dispatch path shared by every
// trigger — the CLI post-command hook, the server on-commit callback (MTIX-53)
// and the daemon tick — and it is origin-independent: a hook fires for an event
// based only on the event being in the journal and the (hook,event) pair not
// yet being in the dispatch ledger on this host, never on who wrote the event
// or how it arrived (local CLI, MCP, sync-arrival, another process). The ledger
// PK serializes concurrent triggers to exactly-once per host in the non-crash
// path; crash recovery is at-least-once via the claim lease. Dispatch runs
// AFTER a mutation commits, never blocks or fails the mutation; adapter errors
// are logged, never propagated.
type HooksDispatcher struct {
	store    *sqlite.Store
	registry *hooks.Registry
	mtixDir  string
	logger   *slog.Logger
}

// NewHooksDispatcher assembles the adapter registry (inbox + webhook +
// append-file + exec) and returns a dispatcher rooted at mtixDir.
func NewHooksDispatcher(store *sqlite.Store, mtixDir string, logger *slog.Logger) *HooksDispatcher {
	if logger == nil {
		logger = slog.Default()
	}
	// append-file paths resolve under the PROJECT root (the parent of .mtix), so
	// a hook can write a human-readable audit trail like FRONTIER-INBOX.md at the
	// project top level (FR-19.3); the adapter clamps traversal to that base. The
	// exec adapter is registered but gated per-Dispatch by the content-hash trust
	// (fire() skips it unless the operator has trusted the current hooks.yaml).
	reg := hooks.NewRegistry(
		&inboxAdapter{store: store},
		hooks.NewWebhookAdapter(nil),
		hooks.NewAppendFileAdapter(filepath.Dir(mtixDir)),
		hooks.NewExecAdapter(),
	)
	return &HooksDispatcher{store: store, registry: reg, mtixDir: mtixDir, logger: logger}
}

// Dispatch runs one pass of the journal-tail dispatcher (FR-20 §4.2): re-fire
// stale claims left by crashed triggers, then scan events past the scan floor
// — any origin — claiming each matching (hook,event) in the ledger before
// firing it, and finally advance the floor (clamped below open claims) and
// prune terminal ledger rows beneath it.
//
// The floor advances over every event read — including ones no hook matches
// and passes with no hooks configured at all — which is what keeps "a hook
// added later fires only on FUTURE events" true (MTIX-47.3 preserved, FR-20
// §8). Never returns an error: a caller on the mutation path must not be
// blocked.
func (d *HooksDispatcher) Dispatch(ctx context.Context) {
	if d == nil || d.store == nil {
		return
	}
	cfg, warns := hooks.Load(d.mtixDir)
	for _, w := range warns {
		d.logger.Warn("hooks.yaml", "warning", w)
	}

	// exec runs only for a hooks.yaml the operator has trusted by content hash
	// (MTIX-49). Evaluated once per pass; a config change since the last trust
	// silently disables exec (fire() logs the skip). Placement of a hook on a
	// host plus that host's trust is the fleet-level designation (FR-20 §5).
	execTrusted := hooks.ExecTrusted(d.mtixDir)

	d.refireStaleClaims(ctx, cfg, execTrusted)

	floor, err := d.store.HookCursor(ctx)
	if err != nil {
		d.logger.Error("hook dispatch: read scan floor", "error", err)
		return
	}
	events, err := d.store.ReadJournalSince(ctx, floor, 500)
	if err != nil {
		d.logger.Error("hook dispatch: read journal", "error", err)
		return
	}

	maxSeq := floor
	for _, je := range events {
		if je.Seq > maxSeq {
			maxSeq = je.Seq
		}
		d.claimAndFireMatching(ctx, cfg, je, execTrusted)
	}
	if maxSeq > floor {
		if err := d.store.AdvanceHookScanFloorClamped(ctx, maxSeq); err != nil {
			d.logger.Error("hook dispatch: advance scan floor", "error", err)
		}
	}
	if err := d.store.PruneHookDispatchLedger(ctx); err != nil {
		d.logger.Error("hook dispatch: prune ledger", "error", err)
	}
}

// claimAndFireMatching normalizes one journal event and, for every configured
// hook it matches, races the ledger claim and fires on a win.
func (d *HooksDispatcher) claimAndFireMatching(ctx context.Context, cfg hooks.Config, je sqlite.JournalEvent, execTrusted bool) {
	if len(cfg.Hooks) == 0 {
		return
	}
	evt := NormalizeEvent(je)
	if evt.Name == "" {
		return // op_type is not a hook trigger (e.g. an unaddressed comment)
	}
	for _, h := range cfg.MatchingHooks(evt) {
		won, err := d.store.ClaimHookDispatch(ctx, h.Name, je.Seq, hookClaimLease)
		if err != nil {
			d.logger.Error("hook dispatch: claim", "hook", h.Name, "seq", je.Seq, "error", err)
			continue
		}
		if !won {
			continue // another trigger owns or already fired this (hook,event)
		}
		d.finishClaim(ctx, h, evt, je, execTrusted)
	}
}

// OnCommitDispatch returns a post-commit callback that runs Dispatch, for a
// long-running server to wire via store.AddOnCommit so an agent's mutation
// dispatches hooks host-side with no per-command PostRun (MTIX-53) — the
// immediate, in-process sibling of the daemon tick, deduped by the ledger.
//
// It is guarded against RE-ENTRANCY: Dispatch itself commits writes (inbox
// delivery, ledger, floor, hook log), and those commits re-invoke on-commit
// callbacks; without the guard the callback would recurse until the stack
// blows. The guard makes a mutation's dispatch run exactly once — nested
// re-entries are dropped, and the events this pass advanced past are handled
// by the next mutation's dispatch or the daemon tick. Safe on a nil
// dispatcher (returns a no-op).
func (d *HooksDispatcher) OnCommitDispatch() func() {
	if d == nil {
		return func() {}
	}
	var running atomic.Bool
	return func() {
		if !running.CompareAndSwap(false, true) {
			return // a dispatch is already in flight; its own writes must not recurse
		}
		defer running.Store(false)
		d.Dispatch(context.Background())
	}
}

// refireStaleClaims is the crash-recovery leg (FR-20 §7): every 'claimed'
// ledger row older than the lease is a trigger that died between claim and
// fire. Deliberately floor-independent — a claim can slip below the floor in a
// narrow advance race, and this scan is what still finds it. A stale claim
// whose event or hook no longer exists is closed as an error (with the reason
// in the audit log) so it cannot park the scan floor forever.
func (d *HooksDispatcher) refireStaleClaims(ctx context.Context, cfg hooks.Config, execTrusted bool) {
	claims, err := d.store.StaleHookClaims(ctx, hookClaimLease)
	if err != nil {
		d.logger.Error("hook dispatch: stale-claim scan", "error", err)
		return
	}
	for _, c := range claims {
		won, err := d.store.ClaimHookDispatch(ctx, c.Hook, c.Seq, hookClaimLease)
		if err != nil {
			d.logger.Error("hook dispatch: reclaim", "hook", c.Hook, "seq", c.Seq, "error", err)
			continue
		}
		if !won {
			continue // another trigger reclaimed it first
		}
		h, found := hookByName(cfg, c.Hook)
		je, ok, err := d.store.ReadJournalEventAt(ctx, c.Seq)
		evt := NormalizeEvent(je)
		if err != nil || !ok || !found || evt.Name == "" {
			d.logger.Warn("hook dispatch: abandoning stale claim — hook or event no longer resolvable",
				"hook", c.Hook, "seq", c.Seq, "error", err)
			d.recordOutcome(ctx, c.Hook, c.Seq, sqlite.OutcomeError)
			d.logFiring(ctx, hooks.Hook{Name: c.Hook}, evt, "", sqlite.OutcomeError, "stale claim abandoned")
			continue
		}
		d.logger.Info("hook dispatch: re-firing stale claim (crashed trigger)", "hook", c.Hook, "seq", c.Seq)
		d.finishClaim(ctx, h, evt, je, execTrusted)
	}
}

// finishClaim completes a claim the caller just won: apply the rate limit,
// fire the adapters, and finalize the ledger row with the aggregate outcome.
func (d *HooksDispatcher) finishClaim(ctx context.Context, h hooks.Hook, evt hooks.Event, je sqlite.JournalEvent, execTrusted bool) {
	outcome := sqlite.OutcomeRateLimited
	if !d.rateLimited(ctx, h, evt) {
		outcome = d.fire(ctx, h, evt, je, execTrusted)
	}
	d.recordOutcome(ctx, h.Name, je.Seq, outcome)
}

func (d *HooksDispatcher) recordOutcome(ctx context.Context, hook string, seq int64, outcome string) {
	if err := d.store.RecordHookDispatchOutcome(ctx, hook, seq, outcome); err != nil {
		d.logger.Error("hook dispatch: record outcome", "hook", hook, "seq", seq, "error", err)
	}
}

// hookByName finds a configured hook by its (unique, load-validated) name.
func hookByName(cfg hooks.Config, name string) (hooks.Hook, bool) {
	for _, h := range cfg.Hooks {
		if h.Name == name {
			return h, true
		}
	}
	return hooks.Hook{}, false
}

// fire delivers one matched event to each adapter the hook names and returns
// the aggregate ledger outcome: error if any adapter failed, else
// skipped-untrusted if exec was gated, else delivered. Per-adapter outcomes go
// to the audit log; adapter errors are logged and never propagated (FR-19.3).
// The outcome is terminal either way — a fire that ran and failed is never
// auto-retried (FR-20 §14.3).
func (d *HooksDispatcher) fire(ctx context.Context, h hooks.Hook, evt hooks.Event, je sqlite.JournalEvent, execTrusted bool) string {
	eventJSON, _ := json.Marshal(map[string]any{
		"seq": je.Seq, "event": evt.Name, "node_id": evt.NodeID,
		"author": evt.Author, "to": evt.ToAgent, "status": evt.StatusTo,
		"synced": evt.Synced, "hook": h.Name,
	})
	del := hooks.Delivery{Hook: h, Event: evt, EventJSON: eventJSON}
	var anyError, anySkipped bool
	for _, name := range h.Deliver {
		if name == hooks.AdapterExec && !execTrusted {
			d.logger.Warn("hook dispatch: exec skipped — hooks.yaml is not trusted; review it and run 'mtix hooks trust'",
				"hook", h.Name)
			d.logFiring(ctx, h, evt, name, "skipped-untrusted", "")
			anySkipped = true
			continue
		}
		adapter, ok := d.registry.Lookup(name)
		if !ok {
			d.logger.Warn("hook dispatch: unknown adapter", "hook", h.Name, "adapter", name)
			d.logFiring(ctx, h, evt, name, "error", "unknown adapter")
			anyError = true
			continue
		}
		if err := adapter.Deliver(ctx, del); err != nil {
			d.logger.Warn("hook dispatch: adapter failed", "hook", h.Name, "adapter", name, "error", err)
			d.logFiring(ctx, h, evt, name, "error", err.Error())
			anyError = true
			continue
		}
		d.logger.Info("hook dispatch: delivered",
			"hook", h.Name, "adapter", name, "event", evt.Name, "node", evt.NodeID, "seq", je.Seq)
		d.logFiring(ctx, h, evt, name, "delivered", "")
	}
	switch {
	case anyError:
		return sqlite.OutcomeError
	case anySkipped:
		return sqlite.OutcomeSkippedUntrusted
	default:
		return sqlite.OutcomeDelivered
	}
}

// rateLimited reports whether hook h has already fired the FR-19.6 cap of times
// on this event's node within the last hour, logging the skip if so. On a count
// error it fails OPEN (fires) rather than silently dropping events.
func (d *HooksDispatcher) rateLimited(ctx context.Context, h hooks.Hook, evt hooks.Event) bool {
	since := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	n, err := d.store.HookFiringCount(ctx, h.Name, evt.NodeID, since)
	if err != nil {
		d.logger.Error("hook dispatch: rate-limit check", "hook", h.Name, "error", err)
		return false
	}
	if n >= maxHookFiringsPerNodePerHour {
		d.logger.Warn("hook dispatch: RATE LIMITED — hook fired too often on this node",
			"hook", h.Name, "node", evt.NodeID, "count", n, "limit", maxHookFiringsPerNodePerHour)
		return true
	}
	return false
}

// logFiring records a hook-firing outcome to the audit log (FR-19.7); a log
// write failure is itself only logged, never propagated.
func (d *HooksDispatcher) logFiring(ctx context.Context, h hooks.Hook, evt hooks.Event, adapter, outcome, detail string) {
	if err := d.store.WriteHookLog(ctx, sqlite.HookLogEntry{
		Hook: h.Name, NodeID: evt.NodeID, Event: evt.Name,
		Adapter: adapter, Outcome: outcome, Detail: detail,
	}); err != nil {
		d.logger.Error("hook dispatch: write hook log", "error", err)
	}
}

// NormalizeEvent maps a journaled mutation to a canonical hook Event, or a zero
// Event (Name == "") when the op_type is not a hook trigger. The comment
// addressee and the transition's new status both live at payload key "to".
func NormalizeEvent(je sqlite.JournalEvent) hooks.Event {
	e := hooks.Event{Seq: je.Seq, NodeID: je.NodeID, Author: je.Author, Synced: je.Synced, ViaHook: je.ViaHook}
	switch je.OpType {
	case "comment":
		var p struct {
			To string `json:"to"`
		}
		_ = json.Unmarshal(je.Payload, &p)
		if p.To == "" {
			return hooks.Event{} // an unaddressed comment is not a hook event
		}
		e.Name = hooks.EventCommentAddressed
		e.ToAgent = p.To
	case "transition_status":
		var p struct {
			To string `json:"to"`
		}
		_ = json.Unmarshal(je.Payload, &p)
		e.Name = hooks.EventStatusChanged
		e.StatusTo = p.To
	case "create_node":
		e.Name = hooks.EventNodeCreated
	default:
		return hooks.Event{}
	}
	return e
}

// inboxAdapter delivers a matched event to an agent's inbox by recording a hook
// delivery (FR-19.4). The target is the hook's to-agent; a comment.addressed
// event falls back to its own addressee. Implements hooks.Adapter.
type inboxAdapter struct {
	store *sqlite.Store
}

func (a *inboxAdapter) Name() string { return hooks.AdapterInbox }

func (a *inboxAdapter) Deliver(ctx context.Context, d hooks.Delivery) error {
	agent := d.Hook.Match.ToAgent
	if agent == "" {
		agent = d.Event.ToAgent
	}
	if agent == "" {
		return fmt.Errorf("inbox adapter: hook %q names no to-agent and event has no addressee", d.Hook.Name)
	}
	return a.store.RecordInboxDelivery(ctx, agent, d.Event.Seq, d.Hook.Name)
}
