// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/hyper-swe/mtix/internal/hooks"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

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
	// project top level (FR-19.3); the adapter clamps traversal to that base.
	reg := hooks.NewRegistry(
		&inboxAdapter{store: store},
		hooks.NewWebhookAdapter(nil),
		hooks.NewAppendFileAdapter(filepath.Dir(mtixDir)),
	)
	return &HooksDispatcher{store: store, registry: reg, mtixDir: mtixDir, logger: logger}
}

// Dispatch processes journal events past the hook cursor, firing matching
// hooks. It ALWAYS advances the cursor over the events it read — even when no
// hook matches or none are configured — so a hook added later fires only on
// FUTURE events, never a backlog. Never returns an error: a caller on the
// mutation path must not be blocked or failed.
func (d *HooksDispatcher) Dispatch(ctx context.Context) {
	if d == nil || d.store == nil {
		return
	}
	cfg, warns := hooks.Load(d.mtixDir)
	for _, w := range warns {
		d.logger.Warn("hooks.yaml", "warning", w)
	}
	cursor, err := d.store.HookCursor(ctx)
	if err != nil {
		d.logger.Error("hook dispatch: read cursor", "error", err)
		return
	}
	events, err := d.store.ReadJournalSince(ctx, cursor, 500)
	if err != nil {
		d.logger.Error("hook dispatch: read journal", "error", err)
		return
	}

	maxSeq := cursor
	for _, je := range events {
		if je.Seq > maxSeq {
			maxSeq = je.Seq
		}
		if len(cfg.Hooks) == 0 {
			continue
		}
		evt := NormalizeEvent(je)
		if evt.Name == "" {
			continue // op_type is not a hook trigger (e.g. an unaddressed comment)
		}
		for _, h := range cfg.MatchingHooks(evt) {
			d.fire(ctx, h, evt, je)
		}
	}
	if maxSeq > cursor {
		if err := d.store.AdvanceHookCursor(ctx, maxSeq); err != nil {
			d.logger.Error("hook dispatch: advance cursor", "error", err)
		}
	}
}

// fire delivers one matched event to each adapter the hook names. Adapter
// errors are logged and never propagated (FR-19.3).
func (d *HooksDispatcher) fire(ctx context.Context, h hooks.Hook, evt hooks.Event, je sqlite.JournalEvent) {
	eventJSON, _ := json.Marshal(map[string]any{
		"seq": je.Seq, "event": evt.Name, "node_id": evt.NodeID,
		"author": evt.Author, "to": evt.ToAgent, "status": evt.StatusTo,
		"synced": evt.Synced, "hook": h.Name,
	})
	del := hooks.Delivery{Hook: h, Event: evt, EventJSON: eventJSON}
	for _, name := range h.Deliver {
		adapter, ok := d.registry.Lookup(name)
		if !ok {
			d.logger.Warn("hook dispatch: unknown adapter", "hook", h.Name, "adapter", name)
			continue
		}
		if err := adapter.Deliver(ctx, del); err != nil {
			d.logger.Warn("hook dispatch: adapter failed", "hook", h.Name, "adapter", name, "error", err)
		}
	}
}

// NormalizeEvent maps a journaled mutation to a canonical hook Event, or a zero
// Event (Name == "") when the op_type is not a hook trigger. The comment
// addressee and the transition's new status both live at payload key "to".
func NormalizeEvent(je sqlite.JournalEvent) hooks.Event {
	e := hooks.Event{Seq: je.Seq, NodeID: je.NodeID, Author: je.Author, Synced: je.Synced}
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
