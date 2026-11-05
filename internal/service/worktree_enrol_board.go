// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// boardShown bounds how many node ids the refusal prints.
const boardShown = 10

// BoardReason says which proof of the enrolment rule failed.
type BoardReason string

const (
	// BoardFileDiffers means .mtix/tasks.json is not what the local store exports.
	BoardFileDiffers BoardReason = "file-differs"
	// BoardHubLacks means the local store holds nodes the hub does not hold in
	// the same state, with no journal entry that explains the difference.
	BoardHubLacks BoardReason = "hub-lacks"
)

// BoardFinding is a failed proof: nothing is enrolled while one exists.
type BoardFinding struct {
	Reason BoardReason
	// IDs are the node ids involved, sorted.
	IDs []string
}

// WorktreeBoardError is the refusal when the worktree's local board is not
// provably all on the hub (MTIX-112.17.16.6). Enrolling would swap the store
// for the hub clone, and the next write would rewrite tasks.json from it.
type WorktreeBoardError struct {
	BoardFinding
}

// Error names the reason, the count, the first ids and a fix that works.
func (e *WorktreeBoardError) Error() string {
	shown, more := e.IDs, ""
	if len(shown) > boardShown {
		shown, more = shown[:boardShown], fmt.Sprintf(" and %d more", len(e.IDs)-boardShown)
	}
	ids := fmt.Sprintf("%d node(s): %s%s", len(e.IDs), strings.Join(shown, ", "), more)
	if e.Reason == BoardFileDiffers {
		return "tasks.json is not what this worktree's store exports (" + ids + "); enrolment refused and nothing changed.\n" +
			"fix: keep a copy of .mtix/tasks.json if it holds work you need, run mtix sync --fix in this worktree (it rewrites tasks.json from the store), then enroll again; " +
			"work that exists only in the copy is in no store and on no hub, so re-create it with mtix commands after the enrolment"
	}
	return "this worktree's store holds work the hub does not hold in the same state (" + ids + "); enrolment refused and nothing changed.\n" +
		"those nodes are in no journal entry the hub has, so mtix sync push cannot send them and mtix import cannot add one.\n" +
		"no command an agent can run unattended journals them (mtix import --mode merge writes no journal row, and mtix sync backfill refuses a store that has one). " +
		"fix, for a person at a terminal: copy .mtix/tasks.json aside and re-create the work with mtix commands in a checkout connected to the hub; then, in this worktree, run: " +
		sqlite.SyncLogRecovery + " (the discard step asks that person to type the ticket count and writes a backup first); then enroll again"
}

// Unwrap lets callers match model.ErrConflict.
func (e *WorktreeBoardError) Unwrap() error { return model.ErrConflict }

// refuseBoard returns a WorktreeBoardError when a finding exists; a failed
// comparison is an error, never a pass.
func refuseBoard(f *BoardFinding, err error) error {
	if err != nil {
		return fmt.Errorf("compare the local board with the hub: %w", err)
	}
	if f != nil && len(f.IDs) > 0 {
		return &WorktreeBoardError{BoardFinding: *f}
	}
	return nil
}

// BoardFileDifferences returns the sorted ids of nodes (and of the sources of
// dependencies) on which the board file and the local store's export differ,
// in either direction. It compares content, never timestamps of the file.
func BoardFileDifferences(file, local *sqlite.ExportData) ([]string, error) {
	localNorm, err := roundTripExport(local)
	if err != nil {
		return nil, err
	}
	diff := map[string]bool{}
	fileNodes, err := encodeByKey(file.Nodes, func(i int) string { return file.Nodes[i].ID })
	if err != nil {
		return nil, err
	}
	localNodes, err := encodeByKey(localNorm.Nodes, func(i int) string { return localNorm.Nodes[i].ID })
	if err != nil {
		return nil, err
	}
	markDifferences(diff, fileNodes, localNodes)
	fileDeps, err := encodeByKey(file.Dependencies, func(i int) string {
		return depKey(file.Dependencies[i].FromID, file.Dependencies[i].ToID, file.Dependencies[i].DepType)
	})
	if err != nil {
		return nil, err
	}
	localDeps, err := encodeByKey(localNorm.Dependencies, func(i int) string {
		return depKey(localNorm.Dependencies[i].FromID, localNorm.Dependencies[i].ToID, localNorm.Dependencies[i].DepType)
	})
	if err != nil {
		return nil, err
	}
	depDiff := map[string]bool{}
	markDifferences(depDiff, fileDeps, localDeps)
	for k := range depDiff {
		diff[strings.SplitN(k, "\x00", 2)[0]] = true
	}
	var out []string
	for id := range diff {
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

func depKey(from, to, kind string) string { return from + "\x00" + to + "\x00" + kind }

// roundTripExport encodes and decodes an export, so a store export and a file
// decoded from disk go through the same normalisation before comparison.
func roundTripExport(d *sqlite.ExportData) (*sqlite.ExportData, error) {
	body, err := json.Marshal(d)
	if err != nil {
		return nil, fmt.Errorf("encode the local export: %w", err)
	}
	var out sqlite.ExportData
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode the local export: %w", err)
	}
	return &out, nil
}

// encodeByKey maps key(i) to the JSON of items[i].
func encodeByKey[T any](items []T, key func(int) string) (map[string]string, error) {
	out := make(map[string]string, len(items))
	for i := range items {
		body, err := json.Marshal(items[i])
		if err != nil {
			return nil, fmt.Errorf("encode %s: %w", key(i), err)
		}
		out[key(i)] = string(body)
	}
	return out, nil
}

// markDifferences adds every key present on one side only, or with different
// content, to diff.
func markDifferences(diff map[string]bool, a, b map[string]string) {
	for k, v := range a {
		if other, ok := b[k]; !ok || other != v {
			diff[k] = true
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			diff[k] = true
		}
	}
}

// BoardNodesHubLacks returns the sorted ids of local nodes the hub does not
// hold in a state the local journal explains (MTIX-112.17.16.6). A node passes
// when the hub holds its uid and either the hub has an event for it that the
// local journal lacks (the hub is ahead, so the hub copy is the newer one), or
// the contents match. A node with no journal entry is never explained by the
// hub being ahead: its content must match the hub's exactly.
func BoardNodesHubLacks(local, hub []sqlite.BoardNodeState) []string {
	byKey := make(map[string]sqlite.BoardNodeState, len(hub))
	for _, h := range hub {
		byKey[h.Key()] = h
	}
	var lacks []string
	for _, l := range local {
		h, ok := byKey[l.Key()]
		switch {
		case !ok:
			lacks = append(lacks, l.ID)
		case len(l.EventIDs) > 0 && hubHoldsMore(l, h):
			// The hub moved past this node: its copy is authoritative.
		case l.Fingerprint != h.Fingerprint || l.Deleted != h.Deleted:
			lacks = append(lacks, l.ID)
		}
	}
	sort.Strings(lacks)
	return lacks
}

// hubHoldsMore reports whether the hub has an event for the node that the local
// journal does not.
func hubHoldsMore(local, hub sqlite.BoardNodeState) bool {
	have := make(map[string]bool, len(local.EventIDs))
	for _, id := range local.EventIDs {
		have[id] = true
	}
	for _, id := range hub.EventIDs {
		if !have[id] {
			return true
		}
	}
	return false
}
