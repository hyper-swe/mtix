// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/model"
)

// boardShown bounds how many node ids the refusal prints.
const boardShown = 10

// BoardReason says which proof of the enrolment rule failed.
type BoardReason string

const (
	// BoardFileDiffers means .mtix/tasks.json is not what the local store exports.
	BoardFileDiffers BoardReason = "file-differs"
	// BoardHubDiffers means the local store is not what a clone of the hub holds
	// when the clone is built from only the events this store has applied: the
	// store holds something no hub event gives it, or the hub holds an event the
	// store has not applied.
	BoardHubDiffers BoardReason = "hub-differs"
	// BoardLocalValueLost means a value this store wrote itself lost to later
	// hub events: the clone at the hub's head holds another one (MTIX-112.17.16.18).
	BoardLocalValueLost BoardReason = "local-value-lost"
)

// BoardFinding is a failed proof: nothing is enrolled while one exists.
type BoardFinding struct {
	Reason BoardReason
	// Diff maps each differing node id to the names of the fields that differ.
	Diff BoardDiff
	// Snapshot is set for a store that came from a tasks.json snapshot (it never
	// pulled and holds no journal row): nothing it holds can be re-entered as a
	// journaled write, so its refusal prints the accept path.
	Snapshot bool
	// cmp says which side holds each difference and hashes the values on both.
	cmp *boardCmp
	// hubLacks is set by ConfirmAgainstHub: the whole rows (id, field) the full
	// hub clone lacks, out of the rows the bounded clone lacks. Until then the
	// refusal says nothing about what the hub holds.
	hubLacks map[string]bool
	// hubChecked is true once ConfirmAgainstHub ran.
	hubChecked bool
	// heldFields and heldSig are set by ConfirmAgainstHub: for each row the hub
	// already holds, the fields in which the store differs, and a hash of the
	// values on both sides (see recordHeld).
	heldFields map[string][]string
	heldSig    string
}

// ConfirmAgainstHub checks the whole rows only this store holds (against the
// bounded clone) against the full hub clone, the hub at its head. A row the
// bounded clone lacks may still be on the hub, above the store's pull cursor;
// only a row the full clone also lacks is one the hub lacks. The bounded
// comparison itself is unchanged (MTIX-112.17.16.18, ruling r2 point 2).
func (f *BoardFinding) ConfirmAgainstHub(localExport, fullCloneExport []byte) error {
	c, err := compareReplicas(localExport, fullCloneExport)
	if err != nil {
		return err
	}
	f.hubLacks = map[string]bool{}
	for id, fields := range c.diff {
		for _, field := range fields {
			key := id + "\x00" + field
			if strings.HasPrefix(field, "(") && c.rowKinds[key][kindStoreOnly] {
				f.hubLacks[key] = true
			}
		}
	}
	f.recordHeld(c)
	f.hubChecked = true
	return nil
}

// rowIDs lists the ids that have a store-only whole row in the given field,
// split by whether the full hub clone lacks that row too.
func (f BoardFinding) rowIDs(field string, hubLacks bool) []string {
	var out []string
	if f.cmp == nil {
		return out
	}
	for _, id := range f.cmp.rowsOnly(kindStoreOnly) {
		key := id + "\x00" + field
		if f.cmp.rowKinds[key][kindStoreOnly] && f.hubLacks[key] == hubLacks {
			out = append(out, id)
		}
	}
	return out
}

// StoreOnlyRows lists the ids of whole rows the store holds and the hub clone
// lacks (a node, a dependency, a section row), sorted.
func (f BoardFinding) StoreOnlyRows() []string {
	if f.cmp == nil {
		return nil
	}
	return f.cmp.rowsOnly(kindStoreOnly)
}

// HubRowsOnly reports whether the only differences are rows the hub clone
// holds and the store lacks: the store holds nothing the clone lacks and no
// field it holds differently. Only then is it safe to say a pull loses
// nothing (MTIX-112.17.16.6, ruling r5 point 1).
func (f BoardFinding) HubRowsOnly() bool {
	return f.Reason == BoardHubDiffers && f.cmp != nil && !f.cmp.has(kindStoreOnly) && !f.cmp.has(kindChanged)
}

// IDs returns the differing ids, sorted.
func (f BoardFinding) IDs() []string { return f.Diff.IDs() }

// Digest identifies this exact difference: the reason, each differing id, the
// field names and a hash of the values on both sides, so a change of a value
// the operator did not read changes the digest. An accept-hub request names it
// (MTIX-112.17.16.6). The report itself stays ids and field names.
func (f BoardFinding) Digest() string {
	h := sha256.New()
	_, _ = h.Write([]byte(string(f.Reason) + "\n"))
	for _, id := range f.IDs() {
		_, _ = h.Write([]byte(id + "\x00"))
		for _, field := range f.Diff[id] { // add keeps the names sorted
			_, _ = h.Write([]byte(field + "\x01"))
		}
		_, _ = h.Write([]byte("\n"))
	}
	if f.cmp != nil {
		_, _ = h.Write([]byte(f.cmp.signature()))
	}
	_, _ = h.Write([]byte("\x03" + f.heldSig))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// WorktreeBoardError is the refusal when the worktree's local board is not
// provably all on the hub (MTIX-112.17.16.6). Enrolling would swap the store
// for the hub clone, and the next write would rewrite tasks.json from it.
type WorktreeBoardError struct {
	BoardFinding
	// Report is the path of the diff report written under .mtix.
	Report string
	// StaleAccept is set when the operator named a digest and the difference
	// now has another one: the board changed after the report they read.
	StaleAccept bool
}

// Error names the reason, the count, the first ids, the report and the fix.
func (e *WorktreeBoardError) Error() string {
	ids := e.IDs()
	shown, more := ids, ""
	if len(shown) > boardShown {
		shown, more = shown[:boardShown], fmt.Sprintf(" and %d more", len(ids)-boardShown)
	}
	list := fmt.Sprintf("%d node(s): %s%s", len(ids), strings.Join(shown, ", "), more)
	report := "report (node ids and field names, no content): " + format.SafeLine(e.Report)
	if e.Reason == BoardFileDiffers {
		return "tasks.json is not what this worktree's store exports (" + list + "); enrolment refused and nothing changed.\n" + report + "\n" +
			"fix: keep a copy of .mtix/tasks.json if it holds work you need, run mtix sync --fix in this worktree (it rewrites tasks.json from the store), then enroll again; " +
			"work that exists only in the copy is in no store and on no hub, so re-create it with mtix commands after the enrolment"
	}
	return e.hubMessage(list, report)
}

// hubMessage words a hub difference. A pull is printed only when the store is
// merely missing hub rows; every local difference gets the keep-a-copy,
// re-enter, accept sequence and the plain warning that a pull overwrites an
// unjournaled change (MTIX-112.17.16.6, ruling r5 points 1, 2, 3 and 5).
func (e *WorktreeBoardError) hubMessage(list, report string) string {
	stale := ""
	if e.StaleAccept {
		stale = "the digest you named is not this difference's: the board or the hub changed after the report you read, so nothing was accepted.\n"
	}
	head := "this worktree's board differs from the hub as this store last applied it (" + list + "); enrolment refused and nothing changed.\n" + stale + report + "\n"
	if e.HubRowsOnly() {
		return head + "the hub holds events this store has not applied, and the store holds nothing the hub lacks. " +
			"fix: run mtix sync pull, then mtix sync --fix (it rewrites tasks.json from the store), then enroll again"
	}
	copyCmd := "mtix backup " + ShellQuote(e.copyPath())
	accept := "enroll again with accept-hub " + e.Digest() +
		"; the old store stays in an aside directory next to a backup, and doctor warns until you clear it"
	if e.Snapshot {
		return head + "this store came from a tasks.json snapshot and has no journal, so a value written again here cannot be made to win. " +
			"It differs from the hub: the snapshot may hold work the hub lacks, or may be behind the hub.\n" +
			"fix, in this order:\n" +
			"  (1) keep a copy: " + copyCmd + "\n" +
			"  (2) if the difference is meant to be dropped, " + accept + "\n"
	}
	return head + "the store differs from the hub clone; the difference may be local work or may be behind the hub, and the report cannot tell which. " +
		"Do not run mtix sync pull or mtix sync --fix: a pull overwrites a change that is not journaled, and it is lost for good.\n" +
		"fix, in this order:\n" +
		"  (1) keep a copy: " + copyCmd + "\n" +
		"  (2) re-enter each changed field the report names as an ordinary mtix command in this worktree, so it journals, run mtix sync push, then enroll again; " +
		"a value the store already holds cannot be re-entered (the command succeeds and pushes nothing), so step (3) is the way for those; " +
		"if the hub holds later edits the enrolment still goes on, names the nodes where the old store and the hub differ, and the old store keeps its version" + e.rowAdvice() + "\n" +
		"  (3) or, if the difference is meant to be dropped, " + accept + "\n"
}

// copyPath is the named backup path of this refusal. It carries the report's
// stamp, because mtix backup refuses a file that exists and a second refusal
// would otherwise name the first one's copy.
func (e *WorktreeBoardError) copyPath() string {
	stamp := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(e.Report), "enroll-diff-"), ".json")
	return filepath.Join(filepath.Dir(e.Report), "enrol-copy-"+stamp+".db") //nolint:misspell // the copy keeps the "enrol" spelling the aside directory and the CLI wording use (ADR-007 section 4.2)
}

// rowAdvice words the whole rows only this store holds. It speaks only after
// ConfirmAgainstHub: a row is said to be lacking from the hub only when the full
// hub clone lacks it, and a row the hub holds is never advised to be created or
// added again, because that would duplicate it. Node rows and dependency rows
// are worded apart (review F2).
func (e *WorktreeBoardError) rowAdvice() string {
	if !e.hubChecked {
		return ""
	}
	var parts []string
	if ids := e.rowIDs("(node)", true); len(ids) > 0 {
		parts = append(parts, "the hub lacks "+idList(ids)+": create each again with the ordinary create command (mtix create), "+
			"never mtix update a node the hub lacks (that pushes an event for a task the hub does not hold)")
	}
	if ids := e.rowIDs("(dependencies)", true); len(ids) > 0 {
		parts = append(parts, "the hub lacks the dependency row(s) from "+idList(ids)+": mtix dep add is refused while this store holds the link (already exists) "+
			"and a link already in the store cannot be journaled again, so use step (3), then add the link again with mtix dep add in the enrolled worktree")
	}
	held := uniqueIDs(append(e.rowIDs("(node)", false), e.rowIDs("(dependencies)", false)...))
	if len(held) > 0 {
		parts = append(parts, e.heldAdvice(held))
	}
	if len(parts) == 0 {
		return ""
	}
	tail := ""
	if len(e.rowIDs("(node)", true)) > 0 {
		tail = "; a row that is created again stays different on this side until it is accepted, so use step (3) after it"
	}
	return "; " + strings.Join(parts, "; ") + tail
}

// idList joins ids, bounded by boardShown.
func idList(ids []string) string {
	more := ""
	if len(ids) > boardShown {
		ids, more = ids[:boardShown], fmt.Sprintf(" and %d more", len(ids)-boardShown)
	}
	return strings.Join(ids, ", ") + more
}

// Unwrap lets callers match model.ErrConflict.
func (e *WorktreeBoardError) Unwrap() error { return model.ErrConflict }

// boardReport is the diff report file: ids and field names, never content.
type boardReport struct {
	Reason    BoardReason         `json:"reason"`
	Digest    string              `json:"digest"`
	CreatedAt string              `json:"created_at"`
	Count     int                 `json:"count"`
	Nodes     map[string][]string `json:"nodes"`
	// Held lists, for a row the hub already holds, the fields in which the
	// store differs from the hub: names only.
	Held map[string][]string `json:"held_fields,omitempty"`
}

// writeBoardReport writes the diff report under .mtix and returns its path.
func writeBoardReport(mtixDir string, at time.Time, f *BoardFinding) (string, error) {
	return writeNamedReport(mtixDir, "enroll-diff-", at, f)
}

// writeNamedReport writes a report whose file name starts with prefix, so two
// reports of one enrolment in the same second never share a file.
func writeNamedReport(mtixDir, prefix string, at time.Time, f *BoardFinding) (string, error) {
	path := filepath.Join(mtixDir, prefix+at.UTC().Format("20060102T150405Z")+".json")
	body, err := json.MarshalIndent(boardReport{Reason: f.Reason, Digest: f.Digest(), CreatedAt: at.UTC().Format("2006-01-02T15:04:05Z"), Count: len(f.Diff), Nodes: f.Diff, Held: f.HeldFields()}, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode the diff report: %w", err)
	}
	if err := writeFileAtomically(path, append(body, '\n')); err != nil {
		return "", fmt.Errorf("write the diff report: %w", err)
	}
	return path, nil
}

// boardGate turns a proof's outcome into a decision. A failed comparison is an
// error, never a pass. A finding is written to a report and refused, unless it
// is a hub difference and the operator named that difference's digest: then the
// report path is kept in res for doctor and the enrolment goes on.
func (s *WorktreeEnrolment) boardGate(mtixDir, accept string, res *WorktreeEnrolResult, f *BoardFinding, err error) error {
	if err != nil {
		return fmt.Errorf("compare the local board with the hub: %w", err)
	}
	if f == nil || len(f.Diff) == 0 {
		return nil
	}
	path, wErr := writeBoardReport(mtixDir, s.o.Clock(), f)
	if wErr != nil {
		return wErr
	}
	if accept != "" && f.Reason == BoardHubDiffers && accept == f.Digest() {
		res.HubDiffReport = path
		return nil
	}
	return &WorktreeBoardError{BoardFinding: *f, Report: path, StaleAccept: accept != "" && f.Reason == BoardHubDiffers}
}

// BoardFileFinding compares the board file's bytes with the local store's
// export over everything the export carries, with no exclusions: the file and
// the store are the same replica, so a hub clone's limits do not apply to
// them. It returns nil when they hold the same thing.
func BoardFileFinding(file, localExport []byte) (*BoardFinding, error) {
	c, err := compareBoards(file, localExport, nil)
	if err != nil || len(c.diff) == 0 {
		return nil, err
	}
	return &BoardFinding{Reason: BoardFileDiffers, Diff: c.diff, cmp: c}, nil
}

// BoardHubFinding compares the local store's export with the export of a clone
// built from the hub events this store has applied (events at or below its pull
// cursor and events it originated); nil when they hold the same thing. Equal
// means every local state is derivable from hub events.
func BoardHubFinding(localExport, boundedCloneExport []byte) (*BoardFinding, error) {
	return boardHubFinding(localExport, boundedCloneExport, false)
}

// BoardSnapshotHubFinding is the hub rule for a store that came from a
// tasks.json snapshot (it never pulled and holds no journal row): its cursor
// says nothing, so it is compared with the FULL hub clone. Rows only the hub
// holds are not a difference here, because the enrolment replaces the store with
// that clone and loses nothing by it; anything the store holds that the clone
// lacks or holds differently is (ruling r5 point 8).
func BoardSnapshotHubFinding(localExport, fullCloneExport []byte) (*BoardFinding, error) {
	f, err := boardHubFinding(localExport, fullCloneExport, true)
	if f != nil {
		f.Snapshot = true
	}
	return f, err
}

func boardHubFinding(localExport, cloneExport []byte, ignoreHubOnly bool) (*BoardFinding, error) {
	c, err := compareReplicas(localExport, cloneExport)
	if err != nil {
		return nil, err
	}
	if ignoreHubOnly {
		c = c.without(kindHubOnly)
	}
	if len(c.diff) == 0 {
		return nil, nil
	}
	return &BoardFinding{Reason: BoardHubDiffers, Diff: c.diff, cmp: c}, nil
}

// ShellQuote quotes s for a POSIX shell: single quotes, with each embedded
// single quote closed, escaped and reopened. Nothing inside is expanded, so a
// path holding a dollar sign, a backtick, a space or a quote is one literal
// word. Go's %q is not shell quoting: sh expands a dollar sign inside double
// quotes.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
