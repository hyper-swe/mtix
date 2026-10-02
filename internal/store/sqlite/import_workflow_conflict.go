// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/hyper-swe/mtix/internal/model"
)

// ErrWorkflowConflict is returned by a merge import, which then writes
// nothing, when a task's status, assignee, agent state, wake time or deletion state differ
// between the local store and the file and the caller has not chosen a side
// for every such task (MTIX-95.31.13). No rule guesses which side changed a
// field: several writes leave no activity entry, so the merge detects the
// difference and refuses, as it does for a renumbering without --confirm.
var ErrWorkflowConflict = errors.New("workflow values differ between the store and the file")

// WorkflowChoice names the side whose workflow values a merge keeps for a
// task whose values differ (MTIX-95.31.13).
type WorkflowChoice string

const (
	// WorkflowTheirs takes the file's (the pulled board's) values.
	WorkflowTheirs WorkflowChoice = "theirs"
	// WorkflowOurs keeps the local store's values.
	WorkflowOurs WorkflowChoice = "ours"
)

// WorkflowResolution is the caller's answer to a workflow conflict:
// Prefer settles every conflicting task, Theirs and Ours settle the listed
// tasks and win over Prefer (MTIX-95.31.13). A choice that leaves a
// conflicting task unsettled leaves the import refused.
type WorkflowResolution struct {
	Prefer WorkflowChoice
	Theirs []string
	Ours   []string
}

// WorkflowFieldDiff is one differing workflow field of a task.
type WorkflowFieldDiff struct {
	Field string `json:"field"`
	Local string `json:"local"`
	File  string `json:"file"`
}

// WorkflowConflict is a task whose workflow values differ between the local
// store and the file (MTIX-95.31.13). Hint says what the two activity
// streams show; it is a hint, never a judge. Choice is empty while the
// conflict is unresolved.
type WorkflowConflict struct {
	ID     string              `json:"id"`
	Title  string              `json:"title"`
	Fields []WorkflowFieldDiff `json:"fields"`
	Hint   string              `json:"hint"`
	Choice WorkflowChoice      `json:"choice,omitempty"`
	// at is when the merge resolved it, stamped on its activity entry.
	at time.Time
	// idempotent: the task has the same uid and id on both sides, so the
	// reconcile counted it as an idempotent no-op before the conflict.
	idempotent bool
}

// WorkflowConflictError is the error of a merge refused over workflow
// conflicts; it lists every conflicting task, with the choice made for each
// one already settled (MTIX-95.31.13).
type WorkflowConflictError struct {
	Conflicts []WorkflowConflict
}

// Error names how many tasks conflict and how to settle them.
func (e *WorkflowConflictError) Error() string {
	open := 0
	for i := range e.Conflicts {
		if e.Conflicts[i].Choice == "" {
			open++
		}
	}
	return fmt.Sprintf("nothing was imported: %d task(s) have a status, assignee, agent state, wake time or deletion state that "+
		"differs from the file's (%d still need a choice); rerun with --prefer theirs|ours, or "+
		"--theirs ID,ID and --ours ID,ID", len(e.Conflicts), open)
}

// Unwrap lets errors.Is match ErrWorkflowConflict and model.ErrConflict.
func (e *WorkflowConflictError) Unwrap() []error {
	return []error{ErrWorkflowConflict, model.ErrConflict}
}

// workflowPolicy is what a merge needs to settle workflow conflicts.
type workflowPolicy struct {
	resolution WorkflowResolution
	now        time.Time
}

// withWorkflowResolution makes the import settle workflow conflicts with r
// (MTIX-95.31.13).
func withWorkflowResolution(r WorkflowResolution) ImportOption {
	return func(c *importConfig) { c.workflow = r }
}

// sameOptionalInstant reports whether two optional RFC 3339 times agree:
// both empty, or the same instant.
func sameOptionalInstant(a, b string) bool {
	return a == b || (a != "" && b != "" && sameInstant(a, b))
}

// workflowDiffs lists the workflow fields in which the file's node differs
// from the local one: status, assignee, agent state, wake time and deletion state. Progress
// is left out, it is recalculated (MTIX-95.31.13).
func workflowDiffs(local, file *exportNode) []WorkflowFieldDiff {
	var diffs []WorkflowFieldDiff
	add := func(field, l, f string, same bool) {
		if !same {
			diffs = append(diffs, WorkflowFieldDiff{Field: field, Local: l, File: f})
		}
	}
	add("status", local.Status, file.Status, local.Status == file.Status)
	add("assignee", local.Assignee, file.Assignee, local.Assignee == file.Assignee)
	add("agent_state", local.AgentState, file.AgentState, local.AgentState == file.AgentState)
	add("defer_until", local.DeferUntil, file.DeferUntil, sameOptionalInstant(local.DeferUntil, file.DeferUntil))
	// The deletion state is a workflow value too: a delete must neither be
	// dropped nor undone silently (MTIX-95.31.13).
	add("deleted_at", local.DeletedAt, file.DeletedAt, sameOptionalInstant(local.DeletedAt, file.DeletedAt))
	return diffs
}

// planWorkflow finds every task whose workflow values differ between the
// store (read through tx) and the file, settles each with policy, and
// returns the settled ones by task id. It returns a *WorkflowConflictError
// listing all conflicts when any is left unsettled, and an ErrInvalidInput
// error when the resolution names a task that does not conflict or both
// sides (MTIX-95.31.13). It reads only.
func planWorkflow(ctx context.Context, tx *sql.Tx, data *ExportData, policy workflowPolicy) (
	map[string]*WorkflowConflict, error) {
	if err := validateResolution(policy.resolution); err != nil {
		return nil, err
	}
	carries := carriesNodeColumns(data.SchemaVersion)
	var all []WorkflowConflict
	for i := range data.Nodes {
		c, err := workflowConflictOf(ctx, tx, &data.Nodes[i], carries)
		if err != nil {
			return nil, err
		}
		if c != nil {
			all = append(all, *c)
		}
	}
	return settleWorkflow(all, policy)
}

// workflowConflictOf returns the conflict of one file node against the
// store's node under its id, or nil when there is none: the node is new, is
// a different task (mergeImportNode refuses that), or the values agree.
func workflowConflictOf(ctx context.Context, tx *sql.Tx, n *exportNode, carries bool) (*WorkflowConflict, error) {
	local, err := scanExportNode(tx.QueryRowContext(ctx, exportNodeSelectSQL+" WHERE id = ?", n.ID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read local node %s: %w", n.ID, err)
	}
	if refuseDifferentTask(&local, n) != nil {
		return nil, nil
	}
	diffs := workflowDiffs(&local, n)
	if len(diffs) == 0 {
		return nil, nil
	}
	return &WorkflowConflict{ID: n.ID, Title: local.Title, Fields: diffs, Hint: activityHint(&local, n, carries),
		idempotent: n.UID != "" && n.UID == local.UID}, nil
}

// validateResolution rejects an unknown preference and a task listed on both
// sides.
func validateResolution(r WorkflowResolution) error {
	if r.Prefer != "" && r.Prefer != WorkflowTheirs && r.Prefer != WorkflowOurs {
		return fmt.Errorf("unknown workflow preference %q (use theirs or ours): %w", r.Prefer, model.ErrInvalidInput)
	}
	listed := make(map[string]bool, len(r.Theirs))
	for _, id := range r.Theirs {
		listed[id] = true
	}
	for _, id := range r.Ours {
		if listed[id] {
			return fmt.Errorf("task %s is listed as both theirs and ours: %w", id, model.ErrInvalidInput)
		}
	}
	return nil
}

// settleWorkflow applies the resolution to the conflicts: a listed task
// takes its side, the rest take Prefer. Any conflict left without a choice,
// or a listed task that does not conflict, refuses the merge.
func settleWorkflow(all []WorkflowConflict, policy workflowPolicy) (map[string]*WorkflowConflict, error) {
	r := policy.resolution
	byID := make(map[string]*WorkflowConflict, len(all))
	for i := range all {
		all[i].at = policy.now
		all[i].Choice = r.Prefer
		byID[all[i].ID] = &all[i]
	}
	for _, side := range []struct {
		ids    []string
		choice WorkflowChoice
	}{{r.Theirs, WorkflowTheirs}, {r.Ours, WorkflowOurs}} {
		for _, id := range side.ids {
			c, ok := byID[id]
			if !ok {
				return nil, fmt.Errorf("task %s has no workflow conflict to resolve (%s): %w",
					id, side.choice, model.ErrInvalidInput)
			}
			c.Choice = side.choice
		}
	}
	for i := range all {
		if all[i].Choice == "" {
			return nil, &WorkflowConflictError{Conflicts: all}
		}
	}
	if len(all) == 0 {
		return nil, nil
	}
	return byID, nil
}

// copyWorkflowGroup copies the status group (status, previous status,
// progress, closed time, wake time, assignee, agent state, invalidation)
// and deletion state from src to dst: the group moves together, so a task never holds a done
// status with an open closed time (MTIX-95.31.13). A file older than schema
// 2.0.0 carries no previous status or invalidation, so those stay as they
// are when src is such a file.
func copyWorkflowGroup(dst, src *exportNode, srcCarriesAll bool) {
	dst.Status, dst.Progress, dst.ClosedAt = src.Status, src.Progress, src.ClosedAt
	dst.DeferUntil, dst.Assignee, dst.AgentState = src.DeferUntil, src.Assignee, src.AgentState
	dst.DeletedAt = src.DeletedAt
	if srcCarriesAll {
		dst.DeletedBy = src.DeletedBy
		dst.PreviousStatus = src.PreviousStatus
		dst.InvalidatedAt, dst.InvalidatedBy = src.InvalidatedAt, src.InvalidatedBy
		dst.InvalidationReason = src.InvalidationReason
	}
}

// applyWorkflowChoice makes merged (the file's copy, merged with the local
// node) hold the status group of the chosen side (MTIX-95.31.13).
func applyWorkflowChoice(merged, local, file *exportNode, choice WorkflowChoice, fileCarriesAll bool) {
	if choice == WorkflowOurs {
		copyWorkflowGroup(merged, local, true)
		return
	}
	copyWorkflowGroup(merged, file, fileCarriesAll)
}

// recordWorkflowChoice appends to the node's activity an entry saying which
// side the merge kept for which fields (MTIX-95.31.13), so the choice is
// auditable. The id is a ULID of the merge's clock time.
func recordWorkflowChoice(merged *exportNode, c *WorkflowConflict) error {
	id, err := ulid.New(ulid.Timestamp(c.at), rand.Reader)
	if err != nil {
		return fmt.Errorf("mint activity id for node %s: %w", c.ID, err)
	}
	parts := make([]string, 0, len(c.Fields))
	for _, f := range c.Fields {
		parts = append(parts, fmt.Sprintf("%s: local %q, file %q", f.Field, f.Local, f.File))
	}
	merged.Activity = append(merged.Activity, model.ActivityEntry{
		ID: id.String(), Type: model.ActivityTypeSystem, Author: "import", CreatedAt: c.at.UTC(),
		Text: fmt.Sprintf("merge import kept the %s values for %s", c.Choice, strings.Join(parts, "; ")),
	})
	return nil
}

// activityHint describes what the two activity streams show about a
// conflicting task. It is a hint only: several writes leave no entry
// (update --assignee, automatic blocking, hub-applied changes), so it
// never decides (MTIX-95.31.13).
func activityHint(local, file *exportNode, fileCarriesActivity bool) string {
	if !fileCarriesActivity {
		return "the file is older than schema 2.0.0 and carries no activity history"
	}
	onlyFile := entriesMissingFrom(local.Activity, file.Activity)
	onlyLocal := entriesMissingFrom(file.Activity, local.Activity)
	if len(onlyFile) == 0 && len(onlyLocal) == 0 {
		return "both sides hold the same activity history, so the difference was made without a logged change"
	}
	var parts []string
	if len(onlyFile) > 0 {
		parts = append(parts, fmt.Sprintf("the file holds %d entr(ies) you lack, latest: %s", len(onlyFile), describeEntry(onlyFile)))
	}
	if len(onlyLocal) > 0 {
		parts = append(parts, fmt.Sprintf("you hold %d entr(ies) the file lacks, latest: %s", len(onlyLocal), describeEntry(onlyLocal)))
	}
	return strings.Join(parts, "; ")
}

// entriesMissingFrom returns the entries of have that held lacks.
func entriesMissingFrom(held, have []model.ActivityEntry) []model.ActivityEntry {
	seen := make(map[streamKey]bool, len(held))
	for i := range held {
		seen[activityKey(&held[i])] = true
	}
	var out []model.ActivityEntry
	for i := range have {
		if !seen[activityKey(&have[i])] {
			out = append(out, have[i])
		}
	}
	return out
}

// describeEntry renders the newest of entries as "type by author at time".
func describeEntry(entries []model.ActivityEntry) string {
	sorted := append([]model.ActivityEntry(nil), entries...)
	sort.SliceStable(sorted, func(a, b int) bool { return sorted[a].CreatedAt.After(sorted[b].CreatedAt) })
	e := sorted[0]
	return fmt.Sprintf("%s by %s at %s", e.Type, e.Author, e.CreatedAt.UTC().Format(time.RFC3339))
}

// writeWorkflowGroup stores the status group of n, the columns a merge
// takes from the file for a task whose content hash is unchanged when the
// caller chose the file's values (MTIX-95.31.13).
func writeWorkflowGroup(ctx context.Context, tx *sql.Tx, n *exportNode) error {
	// Parameterized update of one node's status group.
	_, err := tx.ExecContext(ctx,
		`UPDATE nodes SET status = ?, previous_status = ?, progress = ?, closed_at = ?,
		   defer_until = ?, assignee = ?, agent_state = ?, invalidated_at = ?,
		   invalidated_by = ?, invalidation_reason = ?, deleted_at = ?, deleted_by = ?
		 WHERE id = ?`,
		n.Status, nullStr(n.PreviousStatus), n.Progress, nullStr(n.ClosedAt),
		nullStr(n.DeferUntil), nullStr(n.Assignee), nullStr(n.AgentState), nullStr(n.InvalidatedAt),
		nullStr(n.InvalidatedBy), nullStr(n.InvalidationReason), nullStr(n.DeletedAt), nullStr(n.DeletedBy), n.ID)
	return err
}
