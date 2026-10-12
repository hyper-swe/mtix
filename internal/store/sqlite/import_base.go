// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hyper-swe/mtix/internal/model"
)

// ImportBase is the board two stores last exchanged, one task at a time, as
// a digest of every node field (MTIX-95.31.15.1). The automatic import
// compares the local store and the pulled board with it, per task and per
// field, to tell the teammate's changes from the local ones when the board
// descends from an older form: the base is the last-exchanged record
// (MTIX-95.31.18) or the git merge-base board (MTIX-95.31.20).
type ImportBase struct {
	// Source names where the base came from, for messages.
	Source string
	// Older is true when the base is a 1.x board (mtix 0.5.3 and earlier):
	// the node columns schema 2.0.0 added (annotations, activity,
	// previous_status, deleted_by and the rest) could not be in it.
	Older bool
	byUID map[string]*BaseTask
	byID  map[string]*BaseTask
}

// BaseTask is one task of the base: its identity and the digest of each
// node field by export key (ExchangeFieldKeys), computed as
// FieldDigest computes them.
type BaseTask struct {
	// Kind is "uid" for a task with a uid, "legacy-id" for one without.
	Kind string
	// Identity is the uid, or the id for a legacy-id task.
	Identity string
	// ID is the task's id in the base.
	ID string
	// Fields holds the digest of each field by export key.
	Fields map[string]string
}

// NewImportBase returns the base made of tasks. older marks a 1.x base.
func NewImportBase(source string, older bool, tasks []BaseTask) *ImportBase {
	b := &ImportBase{Source: source, Older: older,
		byUID: make(map[string]*BaseTask, len(tasks)), byID: make(map[string]*BaseTask, len(tasks))}
	for i := range tasks {
		t := &tasks[i]
		if t.Kind == "uid" {
			b.byUID[t.Identity] = t
		} else {
			b.byID[t.ID] = t
		}
	}
	return b
}

// ImportBaseFromBoard returns the base a committed board makes (the git
// merge-base board): each node's field digests, the board's form from its
// schema_version. Returns ErrInvalidInput for a nil board.
func ImportBaseFromBoard(board *ExportData, source string) (*ImportBase, error) {
	if board == nil {
		return nil, fmt.Errorf("base board: missing export: %w", model.ErrInvalidInput)
	}
	tasks := make([]BaseTask, 0, len(board.Nodes))
	for i := range board.Nodes {
		n := &board.Nodes[i]
		_, digests, err := nodeFields(n)
		if err != nil {
			return nil, err
		}
		task := BaseTask{Kind: "legacy-id", Identity: n.ID, ID: n.ID, Fields: digests}
		if n.UID != "" {
			task.Kind, task.Identity = "uid", n.UID
		}
		tasks = append(tasks, task)
	}
	return NewImportBase(source, !carriesNodeColumns(board.SchemaVersion), tasks), nil
}

// Fields returns the field digests of the base's task of the given kind and
// identity, nil when the base holds none.
func (b *ImportBase) Fields(kind, identity string) map[string]string {
	index := b.byID
	if kind == "uid" {
		index = b.byUID
	}
	if t := index[identity]; t != nil {
		return t.Fields
	}
	return nil
}

// ExchangeFieldKeys lists the node fields a base digests, in export order.
func ExchangeFieldKeys() []string {
	return strings.Fields("id parent_id depth seq project title description prompt acceptance node_type issue_type " +
		"priority labels status progress assignee creator agent_state weight content_hash created_at updated_at " +
		"closed_at defer_until deleted_at uid previous_status estimate_min actual_min code_refs commit_refs " +
		"annotations invalidated_at invalidated_by invalidation_reason activity deleted_by metadata session_id")
}

// FieldDigest returns the digest of one encoded field value: the SHA-256 of
// {"present":true,"value":<value>}, or of {"present":false} when the field
// is absent from the encoding (ok false). It is the digest an exchange
// record holds per field (MTIX-95.31.18).
func FieldDigest(value json.RawMessage, ok bool) string {
	field := []byte(`{"present":false}`)
	if ok {
		field = append(append([]byte(`{"present":true,"value":`), value...), '}')
	}
	return fmt.Sprintf("%x", sha256.Sum256(field))
}

// nodeFields returns node n's encoded fields by export key and their digests.
func nodeFields(n *exportNode) (map[string]json.RawMessage, map[string]string, error) {
	fields, err := encodedFields(n)
	if err != nil {
		return nil, nil, err
	}
	digests := make(map[string]string, len(fields))
	for _, key := range ExchangeFieldKeys() {
		value, ok := fields[key]
		digests[key] = FieldDigest(value, ok)
	}
	return fields, digests, nil
}

// encodedFields returns node n's fields as the export encodes them, by key.
func encodedFields(n *exportNode) (map[string]json.RawMessage, error) {
	raw, err := json.Marshal(n)
	if err != nil {
		return nil, fmt.Errorf("encode node %s: %w", n.ID, err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, fmt.Errorf("decode node %s: %w", n.ID, err)
	}
	return fields, nil
}

// taskFor returns the base's task for local node l and the file's copy in
// (nil when the file lacks it): by uid, else by id for a task the base holds
// without a uid. A task without a uid is accepted only when its title in the
// base still matches the local or the file's title, so a different task
// under the same id is never read as the same one.
func (b *ImportBase) taskFor(l, in *exportNode) *BaseTask {
	for _, n := range []*exportNode{l, in} {
		if n != nil && n.UID != "" && b.byUID[n.UID] != nil {
			return b.byUID[n.UID]
		}
	}
	for _, n := range []*exportNode{l, in} {
		if n == nil || b.byID[n.ID] == nil {
			continue
		}
		t := b.byID[n.ID]
		if in == nil || t.Fields["title"] == titleDigest(l) || t.Fields["title"] == titleDigest(in) {
			return t
		}
	}
	return nil
}

// titleDigest returns the digest of node n's encoded title.
func titleDigest(n *exportNode) string {
	value, _ := json.Marshal(n.Title)
	return FieldDigest(value, true)
}
