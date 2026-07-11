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

// HooksDispatcher fires the delivery adapters of hooks that match journaled
// events (FR-19.3 / MTIX-47.3). It runs AFTER a mutation commits — post-command
// in the CLI and periodically in the daemon — so it never blocks or fails the
// mutation; adapter errors are logged, never propagated. A local rowid cursor
// makes re-dispatch safe (at-least-once) and lets a restart resume.
type HooksDispatcher struct {
	store    *sqlite.Store
	registry *hooks.Registry
	mtixDir  string
	logger   *slog.Logger
}

// NewHooksDispatcher assembles the adapter registry (inbox + webhook +
// append-file; the exec adapter is added once the content-hash trust gate lands
// in 47.5) and returns a dispatcher rooted at mtixDir.
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

// Dispatch is the LOCAL dispatch path — the CLI post-command path, run on every
// host. It fires matching hooks for LOCAL (origin) events only and NEVER for a
// synced event, even an include-synced one: a synced event fires exactly once,
// on the designated host's DispatchSynced path, so it is not re-delivered by
// every machine that pulled it (MTIX-52). It ALWAYS advances the local cursor
// over the events it read — even the synced ones it skips, and even when no hook
// matches — so a hook added later fires only on FUTURE events, never a backlog.
// Never returns an error: a caller on the mutation path must not be blocked.
func (d *HooksDispatcher) Dispatch(ctx context.Context) {
	d.run(ctx, d.store.HookCursor, d.store.AdvanceHookCursor, func(je sqlite.JournalEvent) bool {
		return !je.Synced
	})
}

// DispatchSynced is the DESIGNATED-host dispatch path (MTIX-52). It fires
// include-synced hooks on SYNCED events (those that arrived via the hub from
// another machine), using a SEPARATE cursor so the local path's advance cannot
// hide them. Only ONE host — the designated dispatcher, typically its sync
// daemon — runs this, so a synced event fires exactly once team-wide. Local
// events are ignored here; the local path owns them. The include-synced opt-in
// still gates matching (a synced event fires only for a hook that asked for it).
func (d *HooksDispatcher) DispatchSynced(ctx context.Context) {
	d.run(ctx, d.store.HookSyncedCursor, d.store.AdvanceHookSyncedCursor, func(je sqlite.JournalEvent) bool {
		return je.Synced
	})
}

// OnCommitDispatch returns a post-commit callback that runs Dispatch (the LOCAL
// path), for a long-running server to wire via store.AddOnCommit so an agent's
// mutation dispatches hooks host-side with no per-command PostRun (MTIX-53) —
// the immediate, in-process sibling of the daemon's designated dispatch.
//
// It is guarded against RE-ENTRANCY: Dispatch itself commits writes (inbox
// delivery, cursor, hook log), and those commits re-invoke on-commit callbacks;
// without the guard the callback would recurse until the stack blows. The guard
// makes a mutation's dispatch run exactly once — nested re-entries are dropped,
// and the events this pass advanced past are handled by the next mutation's
// dispatch. Safe on a nil dispatcher (returns a no-op).
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

// run is the shared dispatch loop: it reads journal events past the cursor read
// by readCursor, fires matching hooks for events accept() selects, and advances
// the cursor (via advanceCursor) over EVERY event read — including those accept
// skips — so skipped events never form a backlog.
func (d *HooksDispatcher) run(
	ctx context.Context,
	readCursor func(context.Context) (int64, error),
	advanceCursor func(context.Context, int64) error,
	accept func(sqlite.JournalEvent) bool,
) {
	if d == nil || d.store == nil {
		return
	}
	cfg, warns := hooks.Load(d.mtixDir)
	for _, w := range warns {
		d.logger.Warn("hooks.yaml", "warning", w)
	}
	cursor, err := readCursor(ctx)
	if err != nil {
		d.logger.Error("hook dispatch: read cursor", "error", err)
		return
	}
	events, err := d.store.ReadJournalSince(ctx, cursor, 500)
	if err != nil {
		d.logger.Error("hook dispatch: read journal", "error", err)
		return
	}

	// exec runs only for a hooks.yaml the operator has trusted by content hash
	// (47.5). Evaluated once per dispatch; a config change since the last trust
	// silently disables exec (fire() logs the skip). On the designated host this
	// is that host's local trust — the multi-machine trust anchor (MTIX-49/52).
	execTrusted := hooks.ExecTrusted(d.mtixDir)

	maxSeq := cursor
	for _, je := range events {
		if je.Seq > maxSeq {
			maxSeq = je.Seq
		}
		if len(cfg.Hooks) == 0 || !accept(je) {
			continue
		}
		d.fireMatching(ctx, cfg, je, execTrusted)
	}
	if maxSeq > cursor {
		if err := advanceCursor(ctx, maxSeq); err != nil {
			d.logger.Error("hook dispatch: advance cursor", "error", err)
		}
	}
}

// fireMatching normalizes one journal event and fires every configured hook it
// matches (subject to the rate limit). A non-trigger op_type is a no-op.
func (d *HooksDispatcher) fireMatching(ctx context.Context, cfg hooks.Config, je sqlite.JournalEvent, execTrusted bool) {
	evt := NormalizeEvent(je)
	if evt.Name == "" {
		return // op_type is not a hook trigger (e.g. an unaddressed comment)
	}
	for _, h := range cfg.MatchingHooks(evt) {
		if d.rateLimited(ctx, h, evt) {
			continue
		}
		d.fire(ctx, h, evt, je, execTrusted)
	}
}

// fire delivers one matched event to each adapter the hook names. Adapter
// errors are logged and never propagated (FR-19.3).
func (d *HooksDispatcher) fire(ctx context.Context, h hooks.Hook, evt hooks.Event, je sqlite.JournalEvent, execTrusted bool) {
	eventJSON, _ := json.Marshal(map[string]any{
		"seq": je.Seq, "event": evt.Name, "node_id": evt.NodeID,
		"author": evt.Author, "to": evt.ToAgent, "status": evt.StatusTo,
		"synced": evt.Synced, "hook": h.Name,
	})
	del := hooks.Delivery{Hook: h, Event: evt, EventJSON: eventJSON}
	for _, name := range h.Deliver {
		if name == hooks.AdapterExec && !execTrusted {
			d.logger.Warn("hook dispatch: exec skipped — hooks.yaml is not trusted; review it and run 'mtix hooks trust'",
				"hook", h.Name)
			d.logFiring(ctx, h, evt, name, "skipped-untrusted", "")
			continue
		}
		adapter, ok := d.registry.Lookup(name)
		if !ok {
			d.logger.Warn("hook dispatch: unknown adapter", "hook", h.Name, "adapter", name)
			d.logFiring(ctx, h, evt, name, "error", "unknown adapter")
			continue
		}
		if err := adapter.Deliver(ctx, del); err != nil {
			d.logger.Warn("hook dispatch: adapter failed", "hook", h.Name, "adapter", name, "error", err)
			d.logFiring(ctx, h, evt, name, "error", err.Error())
			continue
		}
		d.logFiring(ctx, h, evt, name, "delivered", "")
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
