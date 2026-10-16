// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/model"
)

// showOptions are the history options of mtix show (MTIX-95.31.19.4).
type showOptions struct {
	history      bool // list the history in the text output
	historyLimit int  // most entries listed; zero reads as the default
}

// showHistoryEntry is one history entry in mtix show --json, named as the
// export names it (ts, not timestamp).
type showHistoryEntry struct {
	ID      string  `json:"id"`
	TaskUID string  `json:"task_uid"`
	TS      string  `json:"ts"`
	Actor   string  `json:"actor"`
	Origin  string  `json:"origin"`
	Kind    string  `json:"kind"`
	Field   string  `json:"field"`
	Old     *string `json:"old,omitempty"`
	New     *string `json:"new,omitempty"`
	EventID string  `json:"event_id,omitempty"`
}

// showHistoryTimeLayout is the fixed-width UTC form task_history stores.
const showHistoryTimeLayout = "2006-01-02T15:04:05.000000000Z"

// writeShowJSON writes the node as mtix show --json: the node record, bytes
// and key order unchanged, plus its change history, newest first, as the last
// key "history" (an empty list when the task has none).
func writeShowJSON(ctx context.Context, out OutputWriter, node *model.Node, opts showOptions) error {
	entries, err := app.nodeSvc.GetNodeHistory(ctx, node.ID, opts.historyLimit)
	if err != nil {
		return fmt.Errorf("read history of %s: %w", node.ID, err)
	}
	list := make([]showHistoryEntry, 0, len(entries))
	for _, e := range entries {
		list = append(list, showHistoryEntry{ID: e.ID, TaskUID: e.TaskUID, TS: e.Timestamp.UTC().Format(showHistoryTimeLayout),
			Actor: e.Actor, Origin: string(e.Origin), Kind: string(e.Kind), Field: e.Field, Old: e.Old, New: e.New, EventID: e.EventID})
	}
	rawNode, err := json.Marshal(node)
	if err != nil {
		return fmt.Errorf("encode %s: %w", node.ID, err)
	}
	rawList, err := json.Marshal(list)
	if err != nil {
		return fmt.Errorf("encode history of %s: %w", node.ID, err)
	}
	// Append the key after the node's own, without re-encoding the node.
	doc := append(append(rawNode[:len(rawNode)-1:len(rawNode)-1], []byte(`,"history":`)...), rawList...)
	return out.WriteJSON(json.RawMessage(append(doc, '}')))
}

// writeShowHistory prints the history lines of mtix show --history, newest first.
func writeShowHistory(ctx context.Context, out OutputWriter, id string, limit int) error {
	entries, err := app.nodeSvc.GetNodeHistory(ctx, id, limit)
	if err != nil {
		return fmt.Errorf("read history of %s: %w", id, err)
	}
	if len(entries) == 0 {
		out.WriteHuman("History:  none\n")
		return nil
	}
	out.WriteHuman("History:\n")
	for _, e := range entries {
		ts := e.Timestamp.UTC().Format("2006-01-02T15:04:05Z")
		line := fmt.Sprintf("  %s  %s  %s (%s)", ts, plainText(e.Field), plainText(e.Actor), plainText(string(e.Origin)))
		if e.Kind != model.HistoryKindChange {
			line = fmt.Sprintf("  %s  %s %s  %s (%s)", ts, plainText(string(e.Kind)), plainText(e.Field), plainText(e.Actor), plainText(string(e.Origin)))
		}
		out.WriteHuman("%s%s\n", line, historyValues(e))
	}
	return nil
}

// historyValues renders the old and new value of a structured entry, and
// nothing for a free-text entry, which carries none.
func historyValues(e model.HistoryEntry) string {
	if e.Old == nil && e.New == nil {
		return ""
	}
	return fmt.Sprintf(": %s -> %s", displayValue(e.Old), displayValue(e.New))
}

// plainText removes every control character, newlines and escapes included,
// and every invisible format character (bidirectional controls, zero-width
// and tag characters, line and paragraph separators) from imported history
// text before it reaches the terminal (MTIX-100.1, MTIX-100.7); --json keeps
// the exact text.
func plainText(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, format.StripControlChars(s))
}

func displayValue(v *string) string {
	if v == nil {
		return "(none)"
	}
	return plainText(*v)
}
